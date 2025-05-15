package deviceplugin

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/consts"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/drivers"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/samber/lo"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	dpSocketPath = "/var/lib/kubelet/device-plugins/%d-erdma.sock"
	rdmaCMDevice = "/dev/infiniband/rdma_cm"
)

// ERDMADevicePlugin implements the Kubernetes device plugin API
type ERDMADevicePlugin struct {
	socket               string
	server               *grpc.Server
	stop                 chan struct{}
	devices              map[string]*types.ERdmaDeviceInfo
	allocAllDevices      bool
	devicepluginPreStart bool
	allocRdmaCM          bool
	sync.Locker
}

// NewERDMADevicePlugin returns an initialized ERDMADevicePlugin
func NewERDMADevicePlugin(devices []*types.ERdmaDeviceInfo, allocAllDevices, devicepluginPreStart, allocRdmaCM bool) (*ERDMADevicePlugin, error) {
	devMap := map[string]*types.ERdmaDeviceInfo{}
	for _, d := range devices {
		devMap[d.Name] = d
	}
	if devicepluginPreStart {
		err := initCriClient(runtimeEndpoints)
		if err != nil {
			return nil, err
		}
	}

	pluginEndpoint := fmt.Sprintf(dpSocketPath, time.Now().Unix())
	if allocRdmaCM {
		_, err := os.Stat(path.Join("/proc/1/root", rdmaCMDevice))
		if err != nil {
			allocRdmaCM = false
		}
	}
	return &ERDMADevicePlugin{
		socket:               pluginEndpoint,
		devices:              devMap,
		Locker:               &sync.Mutex{},
		allocAllDevices:      allocAllDevices,
		devicepluginPreStart: devicepluginPreStart,
		allocRdmaCM:          allocRdmaCM,
		stop:                 make(chan struct{}, 1),
	}, nil
}

// dial establishes the gRPC communication with the registered device plugin.
func dial(unixSocketPath string, timeout time.Duration) (*grpc.ClientConn, func(), error) {
	c, err := grpc.NewClient("passthrough:///"+unixSocketPath, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			fmt.Printf("dial unix socket: %s\n", addr)
			return net.DialTimeout("unix", addr, timeout)
		}), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1024*1024*16)))
	if err != nil {
		return nil, nil, err
	}

	return c, func() {
		err = c.Close()
	}, nil
}

// Start starts the gRPC server of the device plugin
func (m *ERDMADevicePlugin) Start() error {
	if m.server != nil {
		close(m.stop)
		m.server.Stop()
	}
	err := m.cleanup()
	if err != nil {
		return err
	}

	sock, err := net.Listen("unix", m.socket)
	if err != nil {
		return err
	}

	m.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(m.server, m)

	m.stop = make(chan struct{}, 1)
	go func() {
		err := m.server.Serve(sock)
		if err != nil {
			klog.Errorf("error start device plugin server, %+v", err)
		}
	}()
	return nil
}

// GetDevicePluginOptions return device plugin options
func (m *ERDMADevicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{PreStartRequired: m.devicepluginPreStart}, nil
}

// PreStartContainer return container prestart hook
func (m *ERDMADevicePlugin) PreStartContainer(ctx context.Context, req *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	klog.Infof("Prestart Request Devices: %v", req.DevicesIDs)
	if len(req.DevicesIDs) == 0 {
		return &pluginapi.PreStartContainerResponse{}, nil
	}
	pod, found, err := getDevPod(req.DevicesIDs[0])
	if err != nil {
		return nil, err
	}
	if !found {
		return &pluginapi.PreStartContainerResponse{}, fmt.Errorf("can not find pod %s", pod)
	}
	podConfig, err := getPodConfig(pod)
	if err != nil {
		return &pluginapi.PreStartContainerResponse{}, fmt.Errorf("can not get pod config %s, err, %v", pod, err)
	}
	if podConfig.SMCR {
		configSysctl := func(sysctl string) error {
			output, err := exec.Command("nsenter", []string{
				"-n/proc/1/root/" + podConfig.Netns,
				"sysctl", "-w", sysctl,
			}...).CombinedOutput()

			if err != nil {
				return fmt.Errorf("can not exec nsenter %s, err: %v", output, err)
			}
			return nil
		}
		ensureSysctlFSRW, err := exec.Command("bash", "-c",
			"mount | grep ' /proc/sys ' | grep rw || mount -o remount,rw /proc/sys").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("can not ensure sysctl fs rw permission %s, err: %v", ensureSysctlFSRW, err)
		}

		err = configSysctl("net.smc.tcp2smc=1")
		if err != nil {
			return &pluginapi.PreStartContainerResponse{}, err
		}
		err = configSysctl("net.ipv6.conf.all.disable_ipv6=1")
		if err != nil {
			return &pluginapi.PreStartContainerResponse{}, err
		}
		var erdmaInfo *types.ERdmaDeviceInfo

		for _, devID := range req.DevicesIDs {
			devPath := strings.Split(devID, "/")
			if len(devPath) <= 1 {
				continue
			}
			erdmaInfo = m.devices[devPath[0]]
		}
		err = drivers.ConfigForNetnsNetDevice(drivers.PNetIDFromDevice(erdmaInfo), "eth0", podConfig.Netns)
		if err != nil {
			return &pluginapi.PreStartContainerResponse{}, err
		}
	}

	return &pluginapi.PreStartContainerResponse{}, nil
}

// Stop stops the gRPC server
func (m *ERDMADevicePlugin) Stop() error {
	if m.server == nil {
		return nil
	}

	m.server.Stop()
	m.server = nil
	close(m.stop)

	return m.cleanup()
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (m *ERDMADevicePlugin) Register(request pluginapi.RegisterRequest) error {
	conn, closeConn, err := dial(pluginapi.KubeletSocket, 5*time.Second)
	if err != nil {
		return err
	}
	defer closeConn()

	client := pluginapi.NewRegistrationClient(conn)

	_, err = client.Register(context.Background(), &request)
	if err != nil {
		return err
	}
	return nil
}

// ListAndWatch lists devices and update that list according to the health status
func (m *ERDMADevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	var devs []*pluginapi.Device

	for _, d := range m.devices {
		for i := 0; i < 200; i++ {
			devs = append(devs, &pluginapi.Device{ID: fmt.Sprintf("%s/%d", d.Name, i), Health: pluginapi.Healthy,
				Topology: &pluginapi.TopologyInfo{
					Nodes: []*pluginapi.NUMANode{
						{
							ID: d.NUMA,
						},
					},
				}})
		}
	}
	err := s.Send(&pluginapi.ListAndWatchResponse{Devices: devs})
	if err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second * 5)
	for {
		select {
		case <-ticker.C:
			err := s.Send(&pluginapi.ListAndWatchResponse{Devices: devs})
			if err != nil {
				klog.Errorf("error send device informance: error: %v", err)
			}
		case <-m.stop:
			return nil
		}
	}
}

func (m *ERDMADevicePlugin) GetPreferredAllocation(context.Context, *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return nil, fmt.Errorf("unsupported")
}

// Allocate which return list of devices.
func (m *ERDMADevicePlugin) Allocate(ctx context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	response := pluginapi.AllocateResponse{
		ContainerResponses: []*pluginapi.ContainerAllocateResponse{},
	}

	klog.Infof("Request Containers: %v", r.GetContainerRequests())
	occupied := map[string]interface{}{}
	for _, req := range r.GetContainerRequests() {
		devices := map[string][]string{}
		var erdmaInfo *types.ERdmaDeviceInfo
		if !m.allocAllDevices {
			for _, devID := range req.DevicesIDs {
				devPath := strings.Split(devID, "/")
				if len(devPath) <= 1 {
					continue
				}
				if occupied[devPath[0]] != nil {
					continue
				}
				erdmaInfo = m.devices[devPath[0]]
				devices[devPath[0]] = m.devices[devPath[0]].DevPaths
				occupied[devPath[0]] = struct{}{}
			}
		} else {
			for _, dev := range m.devices {
				if occupied[dev.Name] != nil {
					continue
				}
				if erdmaInfo == nil {
					erdmaInfo = dev
				}
				devices[dev.Name] = dev.DevPaths
				occupied[dev.Name] = struct{}{}
			}
		}
		var (
			devicePaths []*pluginapi.DeviceSpec
		)

		lo.ForEach(lo.Values(devices), func(item []string, _ int) {
			devicePaths = append(devicePaths, lo.Map(item, func(item string, _ int) *pluginapi.DeviceSpec {
				return &pluginapi.DeviceSpec{
					ContainerPath: item,
					HostPath:      item,
					Permissions:   "rw",
				}
			})...)
		})
		if m.allocRdmaCM {
			devicePaths = append(devicePaths, &pluginapi.DeviceSpec{
				ContainerPath: rdmaCMDevice,
				HostPath:      rdmaCMDevice,
				Permissions:   "rw",
			})
		}
		if len(devicePaths) > 0 {
			response.ContainerResponses = append(response.ContainerResponses,
				&pluginapi.ContainerAllocateResponse{
					Devices: devicePaths,
					Envs: map[string]string{
						consts.SMCRPNETEnv: drivers.PNetIDFromDevice(erdmaInfo),
					},

					// todo support cdi device for containerd >= 1.7
				},
			)
		}
	}

	return &response, nil
}

func (m *ERDMADevicePlugin) cleanup() error {
	preSocks, err := os.ReadDir(pluginapi.DevicePluginPath)
	if err != nil {
		return err
	}

	for _, preSock := range preSocks {
		klog.Infof("device plugin file info: %+v", preSock)
		if regexp.MustCompile(".*-erdma.sock").Match([]byte(preSock.Name())) {
			err = syscall.Unlink(path.Join(pluginapi.DevicePluginPath, preSock.Name()))
			if err != nil {
				klog.Errorf("error on clean up previous device plugin listens, %+v", err)
			}
		}
	}
	return nil
}

func (m *ERDMADevicePlugin) watchKubeletRestart() {
	wait.Until(func() {
		_, err := os.Stat(m.socket)
		if err == nil {
			return
		}
		if os.IsNotExist(err) {
			klog.Infof("device plugin socket %s removed, restarting.", m.socket)
			err := m.Stop()
			if err != nil {
				klog.Errorf("stop current device plugin server with error: %v", err)
			}
			err = m.Start()
			if err != nil {
				klog.Fatalf("error restart device plugin after kubelet restart %+v", err)
			}
			err = m.Register(
				pluginapi.RegisterRequest{
					Version:      pluginapi.Version,
					Endpoint:     path.Base(m.socket),
					ResourceName: types.ResourceName,
					Options: &pluginapi.DevicePluginOptions{
						PreStartRequired: m.devicepluginPreStart,
					},
				},
			)
			if err != nil {
				klog.Fatalf("error register device plugin after kubelet restart %+v", err)
			}
			return
		}
		klog.Fatalf("error stat socket: %+v", err)
	}, time.Second*10, make(chan struct{}, 1))
}

// Serve starts the gRPC server and register the device plugin to Kubelet
func (m *ERDMADevicePlugin) Serve() {
	err := m.Start()
	if err != nil {
		klog.Fatalf("Could not start device plugin: %v", err)
	}
	klog.Infof("Starting to serve on %s", m.socket)

	err = m.Register(
		pluginapi.RegisterRequest{
			Version:      pluginapi.Version,
			Endpoint:     path.Base(m.socket),
			ResourceName: types.ResourceName,
			Options: &pluginapi.DevicePluginOptions{
				PreStartRequired: m.devicepluginPreStart,
			},
		},
	)
	if err != nil {
		klog.Errorf("Could not register device plugin: %v", err)
		stopErr := m.Stop()
		if stopErr != nil {
			klog.Fatalf("stop current device plugin server with error: %v", stopErr)
		}
	}
	klog.Infof("Registered device plugin with Kubelet")
	m.watchKubeletRestart()
}
