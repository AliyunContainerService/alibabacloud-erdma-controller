//go:build !linux

package drivers

import (
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/vishvananda/netlink"
)

func EnsureNetDevice(link netlink.Link, eri *types.ERI) error {
	return nil
}
