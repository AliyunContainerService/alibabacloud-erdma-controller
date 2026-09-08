package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/deviceplugin"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/drivers"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/k8s"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
)

var (
	agentLog = ctrl.Log.WithName("Agent")
)

// RDMA links can appear after their network devices.
var deviceProbeBackoff = wait.Backoff{
	Duration: 2 * time.Second,
	Factor:   1,
	Steps:    91,
}

type Agent struct {
	kubernetes           k8s.Kubernetes
	driver               drivers.ERdmaDriver
	allocAllDevices      bool
	devicepluginPreStart bool
	localERIDiscovery    bool
	exposedLocalERIs     []string
	jumboFrameMTU        int
}

func stackTriger() {
	sigchain := make(chan os.Signal, 1)
	go func(_ chan os.Signal) {
		for {
			<-sigchain
			var (
				buf       []byte
				stackSize int
			)
			bufferLen := 16384
			for stackSize == len(buf) {
				buf = make([]byte, bufferLen)
				stackSize = runtime.Stack(buf, true)
				bufferLen *= 2
			}
			buf = buf[:stackSize]
			agentLog.Info("dump stacks: ", "stack buffer", string(buf))
		}
	}(sigchain)

	signal.Notify(sigchain, syscall.SIGUSR1)
}

func NewAgent(preferDriver string, allocAllDevice bool, devicepluginPreStart bool, localERIDiscovery bool, exposedLocalERIs string, erdmaInstallerVersion string, jumboFrameMTU int) (*Agent, error) {
	kubernetes, err := k8s.NewKubernetes()
	if err != nil {
		return nil, err
	}
	agentLog.Info("NewAgent: ", "localERIDiscovery", localERIDiscovery, "erdmaInstallerVersion", erdmaInstallerVersion, "jumboFrameMTU", jumboFrameMTU)
	return &Agent{
		kubernetes:           kubernetes,
		driver:               drivers.GetDriver(preferDriver, erdmaInstallerVersion),
		allocAllDevices:      allocAllDevice,
		devicepluginPreStart: devicepluginPreStart,
		localERIDiscovery:    localERIDiscovery,
		exposedLocalERIs:     strings.Split(exposedLocalERIs, ","),
		jumboFrameMTU:        jumboFrameMTU,
	}, nil
}

func probeDeviceWithRetry(ctx context.Context, driver drivers.ERdmaDriver, eri *types.ERI, backoff wait.Backoff) (*types.ERdmaDeviceInfo, error) {
	var (
		deviceInfo *types.ERdmaDeviceInfo
		lastErr    error
		attempts   int
	)
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(context.Context) (bool, error) {
		attempts++
		deviceInfo, lastErr = driver.ProbeDevice(eri)
		if lastErr == nil {
			return true, nil
		}
		if !errors.Is(lastErr, drivers.ErrERdmaLinkNotFound) {
			return false, lastErr
		}
		if attempts == 1 || attempts%15 == 0 {
			agentLog.Info("erdma link is not ready, retrying", "eri", eri.ID, "attempt", attempts, "error", lastErr)
		}
		return false, nil
	})
	if wait.Interrupted(err) && lastErr != nil {
		return nil, fmt.Errorf("erdma link did not become ready after %d attempts: %w", attempts, lastErr)
	}
	if err != nil {
		return nil, err
	}
	if attempts > 1 {
		agentLog.Info("erdma link became ready", "eri", eri.ID, "attempts", attempts)
	}
	return deviceInfo, nil
}

// Both native and compat eRDMA drivers support the RDMA-CM userspace device.
func allocRdmaCMForDriver(driverName string) bool {
	return driverName == "default" || driverName == "compat"
}

func (a *Agent) Run() error {
	go stackTriger()
	var err error
	var eriInfos *networkv1.ERdmaDevice
	var eri []*types.ERI
	if !a.localERIDiscovery {
		// 1. wait related eri device
		eriInfos, err = a.kubernetes.WaitEriInfo()
		if err != nil {
			return err
		}
	} else {
		if !(len(a.exposedLocalERIs) == 1 && a.exposedLocalERIs[0] == "") {
			a.allocAllDevices = true
			agentLog.Info("LocalERIDiscovery: enable expose ERIs, set allocAllDevices to true")
		}
		eri, err = drivers.SelectERIs(a.exposedLocalERIs)
		if err != nil {
			return fmt.Errorf("LocalERIDiscovery: select eri failed: %v", err)
		}
		eriInfos = &networkv1.ERdmaDevice{
			Spec: networkv1.ERdmaDeviceSpec{
				Devices: lo.Map(eri, func(item *types.ERI, index int) networkv1.DeviceInfo {
					return networkv1.DeviceInfo{
						InstanceID:       item.InstanceID,
						MAC:              item.MAC,
						IsPrimaryENI:     item.IsPrimaryENI,
						ID:               item.ID,
						NetworkCardIndex: item.CardIndex,
						QueuePair:        item.QueuePair,
					}
				}),
			},
		}
	}
	agentLog.Info("eri info", "eriInfo", eriInfos, "driver", a.driver.Name())
	// 2. install eri driver
	err = a.driver.Install()
	if err != nil {
		return fmt.Errorf("install eri driver failed, err: %v", err)
	}
	erdmaDevices := make([]*types.ERdmaDeviceInfo, 0)
	for _, eriInfo := range eriInfos.Spec.Devices {
		deviceInfo, err := probeDeviceWithRetry(context.Background(), a.driver, &types.ERI{
			ID:            eriInfo.ID,
			IsPrimaryENI:  eriInfo.IsPrimaryENI,
			MAC:           eriInfo.MAC,
			InstanceID:    eriInfo.InstanceID,
			CardIndex:     eriInfo.NetworkCardIndex,
			JumboFrame:    eriInfos.Spec.JumboFrame,
			JumboFrameMTU: a.jumboFrameMTU,
		}, deviceProbeBackoff)
		if err != nil {
			return fmt.Errorf("probe device failed, err: %w", err)
		}
		erdmaDevices = append(erdmaDevices, deviceInfo)
	}
	agentLog.Info("eri device info", "erdmaDevices", erdmaDevices)
	// 3. config pnet for rdma device
	// SMC-R pnet setup is best-effort: it is only an acceleration path. On
	// images where the SMC module is not usable (e.g. the MLNX OFED smc.ko is
	// incompatible with smc-tools, so smc_pnet reports "SMC module not loaded"),
	// skip it with a warning instead of failing the whole agent, so basic eRDMA
	// and the device plugin still come up.
	for _, deviceInfo := range erdmaDevices {
		if deviceInfo.Capabilities&types.ERDMA_CAP_SMC_R != 0 {
			if err = drivers.ConfigSMCPnetForDevice(deviceInfo); err != nil {
				agentLog.Info("WARNING: skip SMC-R pnet config for device (best-effort)", "device", deviceInfo.Name, "error", err.Error())
			}
		}
	}
	// 4. enable deviceplugin
	devicePlugin, err := deviceplugin.NewERDMADevicePlugin(erdmaDevices, a.allocAllDevices, a.devicepluginPreStart, allocRdmaCMForDriver(a.driver.Name()))
	if err != nil {
		return fmt.Errorf("new erdma device plugin failed, err: %v", err)
	}
	devicePlugin.Serve()
	// 5. todo watch & config smc-r and verbs devices
	return nil
}
