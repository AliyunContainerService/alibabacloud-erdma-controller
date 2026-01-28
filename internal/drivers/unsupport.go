//go:build !linux

package drivers

import (
	"fmt"
	"runtime"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
)

func init() {
	Register("unsupported", &UnSupportDriver{})
}

type UnSupportDriver struct{}

func (u *UnSupportDriver) SetERdmaInstallerVersion(_ string) {}

func (u *UnSupportDriver) Install() error {
	return nil
}

func (u *UnSupportDriver) ProbeDevice(eri *types.ERI) (*types.ERdmaDeviceInfo, error) {
	return nil, fmt.Errorf("unsupport eri on %v", runtime.GOOS)
}

func (u *UnSupportDriver) Name() string {
	return "unsupported"
}
