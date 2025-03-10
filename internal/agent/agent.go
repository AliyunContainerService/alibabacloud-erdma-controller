package agent

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/deviceplugin"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/drivers"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/k8s"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	agentLog = ctrl.Log.WithName("Agent")
)

type Agent struct {
	kubernetes           k8s.Kubernetes
	driver               drivers.ERdmaDriver
	allocAllDevices      bool
	devicepluginPreStart bool
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

func NewAgent(preferDriver string, allocAllDevice bool, devicepluginPreStart bool) (*Agent, error) {
	kubernetes, err := k8s.NewKubernetes()
	if err != nil {
		return nil, err
	}
	return &Agent{
		kubernetes:           kubernetes,
		driver:               drivers.GetDriver(preferDriver),
		allocAllDevices:      allocAllDevice,
		devicepluginPreStart: devicepluginPreStart,
	}, nil
}

func (a *Agent) Run() error {
	go stackTriger()
	// 1. wait related eri device
	eriInfos, err := a.kubernetes.WaitEriInfo()
	if err != nil {
		return err
	}
	agentLog.Info("eri info", "eriInfo", eriInfos, "driver", a.driver.Name())
	// 2. install eri driver
	err = a.driver.Install()
	if err != nil {
		return fmt.Errorf("install eri driver failed, err: %v", err)
	}
	erdmaDevices := make([]*types.ERdmaDeviceInfo, 0)
	for _, eriInfo := range eriInfos.Spec.Devices {
		deviceInfo, err := a.driver.ProbeDevice(&types.ERI{
			ID:           eriInfo.ID,
			IsPrimaryENI: eriInfo.IsPrimaryENI,
			MAC:          eriInfo.MAC,
			InstanceID:   eriInfo.InstanceID,
			CardIndex:    eriInfo.NetworkCardIndex,
		})
		if err != nil {
			return fmt.Errorf("probe device failed, err: %v", err)
		}
		erdmaDevices = append(erdmaDevices, deviceInfo)
	}
	agentLog.Info("eri device info", "erdmaDevices", erdmaDevices)
	// 3. config pnet for rdma device
	for _, deviceInfo := range erdmaDevices {
		if deviceInfo.Capabilities&types.ERDMA_CAP_SMC_R != 0 {
			err = drivers.ConfigSMCPnetForDevice(deviceInfo)
			if err != nil {
				return fmt.Errorf("config smc pnet for device failed, err: %v", err)
			}
		}
	}
	// 4. enable deviceplugin
	devicePlugin, err := deviceplugin.NewERDMADevicePlugin(erdmaDevices, a.allocAllDevices, a.devicepluginPreStart, a.driver.Name() == "default")
	if err != nil {
		return fmt.Errorf("new erdma device plugin failed, err: %v", err)
	}
	devicePlugin.Serve()
	// 5. todo watch & config smc-r and verbs devices
	return nil
}
