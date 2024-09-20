//go:build linux

package drivers

import (
	"fmt"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/vishvananda/netlink"
)

func init() {
	Register(defaultGPUDriver, &OFEDDriver{})
}

var gpuInstallScript = `
cd /tmp && wget http://mirrors.cloud.aliyuncs.com/erdma/env_setup.sh && bash env_setup.sh --egs
`

type OFEDDriver struct{}

func (d *OFEDDriver) Install() error {
	exist := driverExists()
	if !exist {
		_, err := hostExec(gpuInstallScript)
		if err != nil {
			return err
		}
	}
	_, err := hostExec("modprobe erdma")
	if err != nil {
		return fmt.Errorf("install erdma driver failed: %v", err)
	}
	return nil
}

func (d *OFEDDriver) ProbeDevice(eri *types.ERI) (*types.ERdmaDeviceInfo, error) {
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
				Capabilities: types.ERDMA_CAP_VERBS | types.ERDMA_CAP_OOB,
			}, nil
		}
	}
	return nil, fmt.Errorf("erdma device not found")
}

func (d *OFEDDriver) Name() string {
	return defaultGPUDriver
}
