//go:build !linux

package drivers

import (
	"fmt"
	"strings"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
)

func EnsureRdmaCM() error {
	return fmt.Errorf("RDMA-CM is not supported on this platform")
}

func ConfigSMCPnetForDevice(info *types.ERdmaDeviceInfo) error {
	driverLog.Error(nil, "erdma driver is not supported on this platform")
	return nil
}

func PNetIDFromDevice(info *types.ERdmaDeviceInfo) string {
	return strings.ReplaceAll(strings.ToUpper(info.MAC), ":", "")
}

func ConfigForNetDevice(pnet string, netDevice string) error {
	driverLog.Error(nil, "smc-pnet network-device configuration is not supported on this platform")
	return nil
}

func ConfigForNetnsNetDevice(pnet string, netDevice string, netns string) error {
	driverLog.Error(nil, "smc-pnet network-namespace configuration is not supported on this platform")
	return nil
}

func SelectERIs(exposedLocalERIs []string) ([]*types.ERI, error) {
	driverLog.Error(nil, "local ERI discovery is not supported on this platform")
	return nil, nil
}

func hostExec(cmd string) (string, error) {
	driverLog.Error(nil, "host exec is not supported on this platform")
	return "", nil
}
