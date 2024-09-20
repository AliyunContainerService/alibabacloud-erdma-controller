//go:build fake

package drivers

import (
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
)

func init() {
	Register("fake", &FakeDriver{})
}

type FakeDriver struct{}

func (f *FakeDriver) Install() error {
	return nil
}

func (f *FakeDriver) ProbeDevice(eri *types.ERI) (*types.ERdmaDeviceInfo, error) {
	return &types.ERdmaDeviceInfo{
		Name:         "erdma_0",
		MAC:          eri.MAC,
		DevPaths:     []string{"/dev/infiniband/uverbs0"},
		Capabilities: types.ERDMA_CAP_GDR | types.ERDMA_CAP_SMC_R | types.ERDMA_CAP_VERBS | types.ERDMA_CAP_RDMA_CM | types.ERDMA_CAP_OOB,
	}, nil
}

func (f *FakeDriver) Name() string {
	return "fake"
}
