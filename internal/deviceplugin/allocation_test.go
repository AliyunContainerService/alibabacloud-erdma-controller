package deviceplugin

import (
	"context"
	"testing"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestAllocateAllExposesOnlySelectedCharacterDevices(t *testing.T) {
	m, err := NewERDMADevicePlugin([]*types.ERdmaDeviceInfo{
		{Name: "erdma_0", DevPaths: []string{"/dev/infiniband/uverbs0"}},
		{Name: "erdma_1", DevPaths: []string{"/dev/infiniband/uverbs1"}},
	}, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	// Test allocation independently of the host's rdma_cm presence check.
	m.allocRdmaCM = true
	response, err := m.Allocate(context.Background(), &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: []string{"erdma_0/0"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ContainerResponses) != 1 {
		t.Fatalf("unexpected allocation: %+v", response)
	}
	container := response.ContainerResponses[0]
	if len(container.Mounts) != 0 || len(container.Devices) != 3 {
		t.Fatalf("must return individual devices, no directory mounts: %+v", container)
	}
	paths := map[string]bool{}
	for _, device := range container.Devices {
		if device.HostPath != device.ContainerPath || device.Permissions != "rw" {
			t.Fatalf("unexpected device mapping: %+v", device)
		}
		paths[device.HostPath] = true
	}
	for _, want := range []string{"/dev/infiniband/uverbs0", "/dev/infiniband/uverbs1", rdmaCMDevice} {
		if !paths[want] {
			t.Fatalf("missing %s in %v", want, paths)
		}
	}
}
