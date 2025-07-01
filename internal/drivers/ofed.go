//go:build linux

package drivers

import (
	"fmt"
	"strings"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/vishvananda/netlink"
)

func init() {
	Register(defaultGPUDriver, &OFEDDriver{})
}

var gpuInstallScript = `
cd /tmp && rm -f env_setup.sh && wget http://mirrors.cloud.aliyuncs.com/erdma/env_setup.sh && bash env_setup.sh --egs
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
	_, err := hostExec("if [ -f /sys/module/erdma/parameters/compat_mode ] && [ \"N\" == $(cat /sys/module/erdma/parameters/compat_mode) ]; then rmmod erdma && modprobe erdma compat_mode=Y; else modprobe erdma compat_mode=Y; fi")
	if err != nil {
		return fmt.Errorf("install erdma driver failed: %v", err)
	}

	_, err = hostExec("modprobe erdma")
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
				Capabilities: types.ERDMA_CAP_VERBS | types.ERDMA_CAP_OOB,
			}, nil
		}
	}
	return nil, fmt.Errorf("erdma device not found")
}

func (d *OFEDDriver) Name() string {
	return defaultGPUDriver
}

func (d *OFEDDriver) SelectERIs(exposeERIs []string) []*types.ERI {
	var selectEriList []*types.ERI
	instanceID, _ := hostExec("curl -s http://100.100.100.200/latest/meta-data/instance-id")
	rdmadevices, _ := hostExec("ls /sys/class/infiniband/")
	eriList := strings.Fields(strings.TrimSpace(rdmadevices))
	nics, _ := hostExec("ls /sys/class/net/")
	nicList := strings.Fields(strings.TrimSpace(nics))

	for _, rdmadevice := range eriList {
		if checkExpose(exposeERIs, rdmadevice) {
			node_guid, _ := hostExec(fmt.Sprintf("cat /sys/class/infiniband/%s/node_guid", rdmadevice))
			for _, nic := range nicList {
				driverLog.Info("SimpleMode: ", "rdmadevice", rdmadevice, "nic", nic)
				macAddress, _ := hostExec(fmt.Sprintf("cat /sys/class/net/%s/address", nic))
				if isErdmaNetworkInterface(strings.TrimSpace(macAddress), strings.TrimSpace(node_guid)) {
					eri := &types.ERI{
						ID:           rdmadevice,
						IsPrimaryENI: nic == "eth0",
						MAC:          strings.TrimSpace(macAddress),
						InstanceID:   instanceID,
						CardIndex:    -1,
						QueuePair:    -1,
					}
					selectEriList = append(selectEriList, eri)
					driverLog.Info("Simple mode SelectERIs: eri", "eri", eri)
				}
			}
		}
	}
	return selectEriList
}
