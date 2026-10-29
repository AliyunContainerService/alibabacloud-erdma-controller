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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/utils"
	"github.com/samber/lo"
	"github.com/vishvananda/netlink"
)

func checkExpose(instanceID string, exposedLocalERIs []string, rdmaDevice string) (bool, error) {
	var unMatchExposeERIs []string
	isMatched := false
	if len(exposedLocalERIs) == 1 && exposedLocalERIs[0] == "" {
		return true, nil
	}
	pattern := `^(i-(?:\w+|\*))\s+((?:(?:\w+)(?:\/\w+)*))$`
	re := regexp.MustCompile(pattern)
	for _, exposeInfo := range exposedLocalERIs {
		if !re.MatchString(exposeInfo) {
			return false, fmt.Errorf("invalid format %s", exposeInfo)
		}
		id := strings.SplitN(exposeInfo, " ", 2)[0]
		if instanceID == id {
			isMatched = true
			exposeERIs := strings.Split(strings.TrimSpace(strings.SplitN(exposeInfo, " ", 2)[1]), "/")
			for _, dev := range exposeERIs {
				if dev == rdmaDevice {
					return true, nil
				}
			}
		}
		if id == "i-*" {
			unMatchExposeERIs = strings.Split(strings.TrimSpace(strings.SplitN(exposeInfo, " ", 2)[1]), "/")
		}
	}
	if !isMatched {
		driverLog.Info("no matched instanceID found, using unMatchExposeERIs", "instanceID", instanceID)
		for _, dev := range unMatchExposeERIs {
			if dev == "erdma_*" || dev == rdmaDevice {
				return true, nil
			}
		}
	}
	return false, nil
}

// EnsureRdmaCM loads and probes the host userspace connection-manager device.
func EnsureRdmaCM() error {
	// Load explicitly rather than racing rdma-core's asynchronous udev service.
	if _, err := nodeExec()("modprobe rdma_ucm"); err != nil {
		return fmt.Errorf("ensure host RDMA-CM device: %w", err)
	}
	// Lifsea loads modules from the container, whose /dev is not the host's.
	devicePath := "/proc/1/root/dev/infiniband/rdma_cm"
	info, err := os.Stat(devicePath)
	if err != nil {
		return fmt.Errorf("check host RDMA-CM device %s: %w", devicePath, err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("host RDMA-CM device %s is not a character device", devicePath)
	}
	return nil
}

// installAndConfigureDriver holds the shared lock through installation and
// module configuration, including the reuse path.
func installAndConfigureDriver(profile, version string) error {
	started := time.Now()
	driverLog.Info("starting eRDMA setup", "profile", profile)
	output, err := nodeExec()(getDriverSetupScript(profile, version, isContainerOS()))
	driverLog.Info("eRDMA setup finished", "profile", profile, "success", err == nil, "duration", time.Since(started).String())
	if err != nil {
		return fmt.Errorf("eRDMA setup failed: %w", err)
	}
	// CombinedOutput buffers these result summaries until setup finishes;
	// their log timestamps are not the individual stage execution times.
	for _, line := range strings.Split(output, "\n") {
		if msg, ok := strings.CutPrefix(line, "erdma-controller: "); ok {
			driverLog.Info(msg, "profile", profile)
		}
	}
	return nil
}

func isContainerOS() bool {
	output, err := exec.Command("uname", "-r").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "lifsea")
}

//nolint:unparam
func hostExec(cmd string) (string, error) {
	// Enter PID 1's mount (-m) AND cgroup (-C) namespaces. The cgroup namespace
	// matters for the driver installer: it moves its parent process into a host
	// cgroup, and cgroup v2 forbids moving a task into a cgroup above the
	// writer's own cgroup-namespace root. Without -C the process stays in the
	// container's cgroup namespace and the move fails (ENOENT); -C is a no-op
	// for the other host commands. nsenter -C works on cgroup v1 hosts too.
	output, err := exec.Command("nsenter", "-t", "1", "-m", "-C", "--", "bash", "-c", cmd).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("exec error: %v, output: %s", err, string(output))
	}
	return string(output), nil
}

func containerExec(cmd string) (string, error) {
	// Lifsea runs the agent with the host userspace already, and its OS forbids
	// nsenter into PID 1's namespaces, so run the command directly (as before the
	// unified flow). Lifsea therefore does not perform the host-cgroup migration
	// that the normal-OS installer uses.
	output, err := exec.Command("bash", "-c", cmd).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("exec error: %v, output: %s", err, string(output))
	}
	return string(output), nil
}

// nodeExec returns the exec context for driver install and kernel-module
// operations, picked by the node OS. On a container-optimized OS (lifsea) the
// agent already runs with the host userspace, so containerExec is correct;
// everywhere else enter the host namespaces via hostExec so the host toolchain
// is used. This matters most for module loading: modules live in the host's
// /lib/modules (a hostPath mount) and load into the shared host kernel, so they
// must be decompressed by the host's kmod. Using the host kmod avoids
// "could not insert 'erdma': Exec format error" when the host ships
// ZSTD-compressed modules (e.g. Ubuntu 24.04, kmod +ZSTD) that the agent
// container's older kmod (e.g. Alibaba Linux 8, kmod-25, only +XZ +ZLIB)
// cannot decompress.
func nodeExec() func(string) (string, error) {
	if isContainerOS() {
		return containerExec
	}
	return hostExec
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
	rdmaLinks, err := netlink.RdmaLinkList()
	if err != nil {
		return nil, fmt.Errorf("error list rdma links, %v", err)
	}
	linkHwAddr := link.Attrs().HardwareAddr
	// erdma guid first byte is ^= 0x2
	new_linkHwAddr := make(net.HardwareAddr, len(linkHwAddr))
	copy(new_linkHwAddr, linkHwAddr)
	new_linkHwAddr[0] ^= 0x2
	for _, rl := range rdmaLinks {
		rdmaHwAddr, err := parseERdmaLinkHwAddr(rl.Attrs.NodeGuid)
		if err != nil {
			return nil, err
		}
		driverLog.Info("check rdma link", "rdmaLink", rl.Attrs.Name, "rdmaHwAddr", rdmaHwAddr.String(), "linkHwAddr", linkHwAddr.String())
		if rdmaHwAddr.String() == new_linkHwAddr.String() {
			return rl, nil
		}
	}
	return nil, fmt.Errorf("%w for %s", ErrERdmaLinkNotFound, link.Attrs().Name)
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

func ConfigForNetnsNetDevice(pnet string, netDevice string, netns string) error {
	output, err := exec.Command("nsenter", "-n/proc/1/root/"+netns, "--", smcPnet, "-s").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get smc-pnet stat for net device: %v, output: %v", err, string(output))
	}
	if bytes.Contains(output, []byte(netDevice)) {
		return nil
	}
	output, err = exec.Command("nsenter", "-n/proc/1/root/"+netns, "--", smcPnet, "-a", pnet, "-I", netDevice).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to config smc-pnet net device: %v, output: %v", err, string(output))
	}
	return nil
}

func GetERDMANumaNode(info *netlink.RdmaLink) (int64, error) {
	devNumaPath := path.Join("/sys/class/infiniband/", info.Attrs.Name, "device/numa_node")
	numaStr, err := os.ReadFile(devNumaPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Virtual (non-PCI) devices expose no numa_node; treat as node 0,
			// consistent with the negative-value clamp below. Erroring here
			// would bubble up through ProbeDevice and panic the agent.
			driverLog.Info("no numa_node for device, defaulting to numa 0", "device", info.Attrs.Name)
			return 0, nil
		}
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

const (
	instanceIDAddr = "http://100.100.100.200/latest/meta-data/instance-id"
)

func SelectERIs(exposedLocalERIs []string) ([]*types.ERI, error) {
	var selectEriList []*types.ERI
	var isExposed bool
	instanceID, _ := utils.GetStrFromMetadata(instanceIDAddr)
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list link failed: %v", err)
	}

	for _, link := range links {
		if _, ok := link.(*netlink.Device); !ok {
			continue
		}
		if link.Attrs().HardwareAddr != nil {
			rdmaLink, _ := GetERdmaFromLink(link)
			if rdmaLink != nil {
				rdmadevice := rdmaLink.Attrs.Name
				isExposed, err = checkExpose(instanceID, exposedLocalERIs, rdmadevice)
				if isExposed {
					driverLog.Info("LocalERIDiscovery: expose eri", "rdmadevice", rdmadevice, "link name", link.Attrs().Name)
					eri := &types.ERI{
						ID:           rdmadevice,
						IsPrimaryENI: link.Attrs().Name == "eth0",
						MAC:          link.Attrs().HardwareAddr.String(),
						InstanceID:   instanceID,
						CardIndex:    -1,
						QueuePair:    -1,
					}
					selectEriList = append(selectEriList, eri)
					driverLog.Info("Simple mode SelectERIs: eri", "eri", eri)
				} else if err != nil {
					return nil, err
				}
			} else {
				driverLog.Info("LocalERIDiscovery: link is not rdma device, skip", "link_name", link.Attrs().Name)
			}
		}
	}

	return selectEriList, nil
}
