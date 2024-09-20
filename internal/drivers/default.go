//go:build linux

package drivers

import (
	"fmt"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/vishvananda/netlink"
)

func init() {
	Register(defaultDriver, &DefaultDriver{})
}

var defaultInstallScript = `
if [ -d /sys/fs/cgroup/cpu/ ]; then cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/cpu/tasks && cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/memory/tasks; else 
cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/cgroup.procs; fi && cd /tmp &&
wget 'https://mirrors.aliyun.com/erdma/erdma_installer-1.4.0.tar.gz' && tar -xzvf erdma_installer-1.4.0.tar.gz && cd erdma_installer && yum install -y kernel-devel-$(uname -r) gcc-c++ dkms cmake && ERDMA_CM_NO_BOUND_IF=1 ./install.sh --batch
`

type DefaultDriver struct{}

func (d *DefaultDriver) Install() error {
	exist := driverExists()
	if !exist {
		_, err := hostExec(defaultInstallScript)
		if err != nil {
			return err
		}
	}
	_, err := hostExec("if [ -f /sys/module/erdma/parameters/compat_mode ] && [ \"Y\" == $(cat /sys/module/erdma/parameters/compat_mode) ]; then rmmod erdma &&  modprobe erdma compat_mode=N; else modprobe erdma compat_mode=N; fi")
	if err != nil {
		return fmt.Errorf("install erdma driver failed: %v", err)
	}
	return EnsureSMCR()
}

func (d *DefaultDriver) ProbeDevice(eri *types.ERI) (*types.ERdmaDeviceInfo, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list link failed: %v", err)
	}
	for _, link := range links {
		if _, ok := link.(*netlink.Device); !ok {
			continue
		}
		if link.Attrs().HardwareAddr.String() == eri.MAC {
			rdmaLink, err := GetERdmaFromLink(link)
			if err != nil {
				return nil, fmt.Errorf("get erdma link failed: %v", err)
			}

			devPaths, err := GetERdmaDevPathsFromRdmaLink(rdmaLink)
			if err != nil {
				return nil, fmt.Errorf("get erdma dev paths failed: %v", err)
			}

			return &types.ERdmaDeviceInfo{
				Name:         rdmaLink.Attrs.Name,
				MAC:          eri.MAC,
				DevPaths:     devPaths,
				Capabilities: types.ERDMA_CAP_VERBS | types.ERDMA_CAP_RDMA_CM | types.ERDMA_CAP_SMC_R,
			}, nil
		}
	}
	return nil, fmt.Errorf("erdma device not found")
}

func (d *DefaultDriver) Name() string {
	return defaultDriver
}
