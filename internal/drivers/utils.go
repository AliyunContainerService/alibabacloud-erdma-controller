//go:build linux

package drivers

import (
	"bytes"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/samber/lo"
	"github.com/vishvananda/netlink"
)

func driverExists() bool {
	_, err := hostExec("stat /bin/eadm && modinfo erdma")
	if err != nil {
		driverLog.Info("driver not exists", "checklog", err)
		return false
	}
	return true
}

func hostExec(cmd string) (string, error) {
	output, err := exec.Command("nsenter", "-t", "1", "-m", "--", "bash", "-c", cmd).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("exec error: %v, output: %s", err, string(output))
	}
	return string(output), nil
}

func EnsureSMCR() error {
	_, err := hostExec("which smcss || yum install -y smc-tools || apt install -y smc-tools")
	if err != nil {
		return err
	}
	_, err = hostExec("modprobe smc")
	if err != nil {
		return err
	}
	return nil
}

func GetERdmaDevPathsFromRdmaLink(rdmaLink *netlink.RdmaLink) ([]string, error) {
	var devPaths []string
	ibUverbsDevs, err := os.ReadDir("/sys/class/infiniband_verbs/")
	if err != nil {
		return nil, fmt.Errorf("read dir /sys/class/infiniband_verbs/ failed: %v", err)
	}
	lo.ForEach(ibUverbsDevs, func(ibUverbsDev fs.DirEntry, _ int) {
		ibDevPath := filepath.Join("/sys/class/infiniband_verbs/", ibUverbsDev.Name(), "ibdev")
		driverLog.Info("check infiniband path", "path", ibDevPath)
		if _, err = os.Stat(ibDevPath); err == nil {
			if devName, err := os.ReadFile(ibDevPath); err == nil {
				devNameStr := strings.Trim(string(devName), "\n")
				driverLog.Info("infiniband device", "devName", devNameStr)
				if devNameStr == rdmaLink.Attrs.Name {
					devPaths = append(devPaths, filepath.Join("/dev/infiniband", ibUverbsDev.Name()))
				}
			}
		}
	})
	if len(devPaths) == 0 {
		return nil, fmt.Errorf("can not find dev path for %s", rdmaLink.Attrs.Name)
	}

	if _, err := os.Stat("/dev/infiniband/rdma_cm"); err == nil {
		devPaths = append(devPaths, "/dev/infiniband/rdma_cm")
	}
	return devPaths, nil
}

func GetERdmaFromLink(link netlink.Link) (*netlink.RdmaLink, error) {
	if link.Attrs().OperState != netlink.OperUp {
		driverLog.Info("link down, try to up it", "link", link.Attrs().Name)
		_, err := hostExec("dhclient " + link.Attrs().Name)
		if err != nil {
			return nil, fmt.Errorf("dhclient failed for %s, %v", link.Attrs().Name, err)
		}
	}
	rdmaLinks, err := netlink.RdmaLinkList()
	if err != nil {
		return nil, fmt.Errorf("error list rdma links, %v", err)
	}
	linkHwAddr := link.Attrs().HardwareAddr
	// erdma guid first byte is ^= 0x2
	linkHwAddr[0] ^= 0x2
	for _, rl := range rdmaLinks {
		rdmaHwAddr, err := parseERdmaLinkHwAddr(rl.Attrs.NodeGuid)
		if err != nil {
			return nil, err
		}
		driverLog.Info("check rdma link", "rdmaLink", rl.Attrs.Name, "rdmaHwAddr", rdmaHwAddr.String(), "linkHwAddr", linkHwAddr.String())
		if rdmaHwAddr.String() == linkHwAddr.String() {
			return rl, nil
		}
	}
	return nil, fmt.Errorf("cannot found rdma link for %s", link.Attrs().Name)
}

func parseERdmaLinkHwAddr(guid string) (net.HardwareAddr, error) {
	hwAddrSlice := make([]byte, 8)
	guidSlice := strings.Split(guid, ":")
	if len(guidSlice) != 8 {
		return nil, fmt.Errorf("invalid rdma guid: %s", guid)
	}
	for i, s := range guidSlice {
		sint, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("invalid rdma guid: %s, err: %v", guid, err)
		}
		hwAddrSlice[7-i] = uint8(sint)
	}
	return append(hwAddrSlice[0:3], hwAddrSlice[5:8]...), nil
}

const (
	smcPnet = "smc_pnet"
)

func ConfigSMCPnetForDevice(info *types.ERdmaDeviceInfo) error {
	output, err := exec.Command(smcPnet, "-s").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get smc-pnet stat: %v, output: %v", err, string(output))
	}
	if bytes.Contains(output, []byte(PNetIDFromDevice(info))) {
		return nil
	}
	output, err = exec.Command(smcPnet, "-a", PNetIDFromDevice(info), "-D", info.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to config smc-pnet rdma device: %v, output: %v", err, string(output))
	}
	return nil
}

func PNetIDFromDevice(info *types.ERdmaDeviceInfo) string {
	return strings.ReplaceAll(strings.ToUpper(info.MAC), ":", "")
}

func ConfigForNetDevice(pnet string, netDevice string) error {
	output, err := exec.Command(smcPnet, "-s").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get smc-pnet stat for net device: %v, output: %v", err, string(output))
	}
	if bytes.Contains(output, []byte(netDevice)) {
		return nil
	}
	output, err = exec.Command(smcPnet, "-a", pnet, "-I", netDevice).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to config smc-pnet net device: %v, output: %v", err, string(output))
	}
	return nil
}

func GetERDMANumaNode(info *netlink.RdmaLink) (int64, error) {
	devNumaPath := path.Join("/sys/class/infiniband/", info.Attrs.Name, "device/numa_node")
	numaStr, err := os.ReadFile(devNumaPath)
	if err != nil {
		return -1, fmt.Errorf("failed to get numa node for %s: %v", info.Attrs.Name, err)
	}
	numaStr = bytes.Trim(numaStr, "\n")
	numa, err := strconv.Atoi(string(numaStr))
	if err != nil {
		return -1, fmt.Errorf("failed to parse numa node for %s: %v", info.Attrs.Name, err)
	}
	if numa < 0 {
		numa = 0
	}
	return int64(numa), nil
}
