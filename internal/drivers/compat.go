//go:build linux

package drivers

import (
	"fmt"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/vishvananda/netlink"
)

func init() {
	Register("compat", &CompatDriver{})
}

var compatInstallScript = `
if [ -d /sys/fs/cgroup/cpu/ ]; then cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/cpu/tasks && cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/memory/tasks; else 
cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/cgroup.procs; fi &&
if grep -q "Alibaba Cloud Linux Lifsea" /etc/os-release; then lifseacli pkg install kernel-modules-$(uname -r); modprobe erdma compat_mode=Y; else cd /tmp && rm -f erdma_installer-1.4.0.tar.gz &&
wget 'http://mirrors.cloud.aliyuncs.com/erdma/erdma_installer-1.4.0.tar.gz' && tar -xzvf erdma_installer-1.4.0.tar.gz && cd erdma_installer && yum install -y kernel-devel-$(uname -r) gcc-c++ dkms cmake && ERDMA_CM_NO_BOUND_IF=1 ERDMA_FORCE_MAD_ENABLE=1 ./install.sh --batch; fi
`

type CompatDriver struct{}

func (d *CompatDriver) Install() error {
	exist := driverExists()
	if !exist {
		_, err := hostExec(compatInstallScript)
		if err != nil {
			return err
		}
	}
	_, err := hostExec("if [ -f /sys/module/erdma/parameters/compat_mode ] && [ \"N\" == $(cat /sys/module/erdma/parameters/compat_mode) ]; then rmmod erdma && modprobe erdma compat_mode=Y; else modprobe erdma compat_mode=Y; fi")
	if err != nil {
		return fmt.Errorf("install erdma driver failed: %v", err)
	}
	return EnsureSMCR()
}

func (d *CompatDriver) ProbeDevice(eri *types.ERI) (*types.ERdmaDeviceInfo, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list link failed: %v", err)
	}
	for _, link := range links {
		if _, ok := link.(*netlink.Device); !ok {
			continue
		}
		if link.Attrs().HardwareAddr.String() == eri.MAC {
			err := EnsureNetDevice(link, eri)
			if err != nil {
				return nil, fmt.Errorf("ensure net device failed: %v", err)
			}
			rdmaLink, err := GetERdmaFromLink(link)
			if err != nil {
				return nil, fmt.Errorf("get erdma link failed: %v", err)
			}

			devPaths, err := GetERdmaDevPathsFromRdmaLink(rdmaLink)
			if err != nil {
				return nil, fmt.Errorf("get erdma dev paths failed: %v", err)
			}

			numa, err := GetERDMANumaNode(rdmaLink)
			if err != nil {
				return nil, fmt.Errorf("get erdma dev numa failed: %v", err)
			}

			return &types.ERdmaDeviceInfo{
				Name:         rdmaLink.Attrs.Name,
				MAC:          eri.MAC,
				DevPaths:     devPaths,
				NUMA:         numa,
				Capabilities: types.ERDMA_CAP_VERBS | types.ERDMA_CAP_OOB | types.ERDMA_CAP_SMC_R,
			}, nil
		}
	}
	return nil, fmt.Errorf("erdma device not found")
}

func (d *CompatDriver) Name() string {
	return "compat"
}
