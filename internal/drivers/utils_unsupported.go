//go:build !linux

package drivers

import "github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"

func ConfigSMCPnetForDevice(info *types.ERdmaDeviceInfo) error {
	driverLog.Error(nil, "erdma driver is not supported on this platform")
	return nil
}
