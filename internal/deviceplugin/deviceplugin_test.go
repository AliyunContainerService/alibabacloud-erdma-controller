package deviceplugin

import (
	"context"
	"testing"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestAllocateRdmaCM(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "unsupported"
		if enabled {
			name = "compat"
		}
		t.Run(name, func(t *testing.T) {
			// Startup probing is responsible for readiness. Allocate only returns specs.
			plugin := &ERDMADevicePlugin{
				devices: map[string]*types.ERdmaDeviceInfo{
					"erdma_0": {Name: "erdma_0", MAC: "00:16:3e:00:00:01", DevPaths: []string{"/dev/infiniband/uverbs0"}},
				},
				allocRdmaCM: enabled,
			}
			request := &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: []string{"erdma_0/0"}}}}
			response, err := plugin.Allocate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, device := range response.ContainerResponses[0].Devices {
				if device.ContainerPath == rdmaCMDevice {
					found = true
					if device.HostPath != rdmaCMDevice || device.Permissions != "rw" {
						t.Fatalf("unexpected device spec: %v", device)
					}
				}
			}
			if found != enabled {
				t.Fatalf("RDMA-CM allocated=%v, want %v", found, enabled)
			}
		})
	}
}

func TestSMCRDeviceCapability(t *testing.T) {
	plugin := &ERDMADevicePlugin{devices: map[string]*types.ERdmaDeviceInfo{
		"supported":  {Name: "supported", Capabilities: types.ERDMA_CAP_VERBS | types.ERDMA_CAP_SMC_R},
		"verbs-only": {Name: "verbs-only", Capabilities: types.ERDMA_CAP_VERBS},
	}}
	for _, tc := range []struct {
		name      string
		ids       []string
		wantError bool
	}{
		{name: "supported", ids: []string{"supported/0"}},
		{name: "unsupported kernel", ids: []string{"verbs-only/0"}, wantError: true},
		{name: "mixed capabilities", ids: []string{"supported/0", "verbs-only/1"}, wantError: true},
		{name: "unknown", ids: []string{"missing/0"}, wantError: true},
		{name: "malformed", ids: []string{"supported"}, wantError: true},
		{name: "empty", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plugin.smcrDevice(tc.ids)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tc.wantError)
			}
		})
	}
}
