package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/consts"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type lingjunENI = ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet

type lingjunTestAPI struct {
	fakeEriAPI
	enis       []*lingjunENI
	err        error
	requests   []*ecs.DescribeNetworkInterfacesRequest
	tagRequest *ecs.TagResourcesRequest
}

func (f *lingjunTestAPI) TagResources(ctx context.Context, req *ecs.TagResourcesRequest) (*ecs.TagResourcesResponse, error) {
	f.tagRequest = req
	return f.fakeEriAPI.TagResources(ctx, req)
}

func (f *lingjunTestAPI) DescribeNetworkInterfaces(_ context.Context, req *ecs.DescribeNetworkInterfacesRequest) (*ecs.DescribeNetworkInterfacesResponse, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	page := int(ptr.Deref(req.PageNumber, int32(1)))
	start := (page - 1) * 100
	end := min(start+100, len(f.enis))
	return &ecs.DescribeNetworkInterfacesResponse{Body: &ecs.DescribeNetworkInterfacesResponseBody{
		TotalCount:           ptr.To(int32(len(f.enis))),
		NetworkInterfaceSets: &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSets{NetworkInterfaceSet: f.enis[start:end]},
	}}, nil
}

func attachedLingjunENI(id, mode string) *lingjunENI {
	return &lingjunENI{
		NetworkInterfaceId: ptr.To(id), InstanceId: ptr.To("e01-test"),
		Type: ptr.To("Secondary"), Status: ptr.To(types.ENIStatusInUse),
		MacAddress: ptr.To("00:16:3e:00:00:01"), NetworkInterfaceTrafficMode: ptr.To(mode),
		QueuePairNumber: ptr.To(int32(8)),
	}
}

func TestLingjunNodeIdentity(t *testing.T) {
	for _, tc := range []struct {
		provider string
		labels   map[string]string
		want     bool
	}{
		{provider: "e01-test", want: true},
		{provider: "cn-wulanchabu.i-test", want: false},
		{provider: "another-provider", want: false},
		{provider: "e01-other-node", want: false},
		{provider: "node-id", labels: map[string]string{consts.LingjunWorkerLabel: consts.LingjunWorkerLabelValue}, want: true},
		{provider: "e01-test", labels: map[string]string{consts.LingjunWorkerLabel: "false"}, want: false},
	} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "e01-test", Labels: tc.labels}, Spec: corev1.NodeSpec{ProviderID: tc.provider}}
		if got := isLingjunNode(node); got != tc.want {
			t.Errorf("provider=%q labels=%v: got %v want %v", tc.provider, tc.labels, got, tc.want)
		}
	}
}

func TestLingjunReconcileUsesENIsWithoutECSProvisioning(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "e01-test", UID: "node-uid"},
		Spec:       corev1.NodeSpec{ProviderID: "e01-test"},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
	// The real Lingjun layout has two pre-provisioned eRDMA ENIs plus
	// ordinary ENIs. Keep synthetic identities while covering that topology.
	primary := attachedLingjunENI("eni-primary", trafficModeRDMA)
	primary.Type = ptr.To("Primary")
	primary.QueuePairNumber = ptr.To(int32(120))
	secondary := attachedLingjunENI("eni-secondary", trafficModeRDMA)
	secondary.MacAddress = ptr.To("00:16:3e:00:00:02")
	secondary.QueuePairNumber = ptr.To(int32(120))
	secondary.Attachment = &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{NetworkCardIndex: ptr.To(int32(1))}
	api := &lingjunTestAPI{enis: []*lingjunENI{secondary, attachedLingjunENI("eni-normal", "Standard"), primary}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&networkv1.ERdmaDevice{}).WithObjects(node).Build()
	eri := &EriClient{client: api, regionID: "cn-wulanchabu"}
	nodes := &NodeReconciler{Client: kube, EriClient: eri, CtrlConfig: &types.Config{}}
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: node.Name}}
	for range 3 {
		if _, err := nodes.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if api.tagResourcesCalls != 2 {
		t.Fatalf("expected selection tagging and one existing-CR backfill, got %d calls", api.tagResourcesCalls)
	}
	device := &networkv1.ERdmaDevice{}
	if err := kube.Get(ctx, req.NamespacedName, device); err != nil {
		t.Fatal(err)
	}
	if len(device.Spec.Devices) != 2 || device.Spec.Devices[0].ID != "eni-primary" || device.Spec.Devices[0].InstanceID != node.Name ||
		device.Spec.JumboFrame {
		t.Fatalf("unexpected device: %+v", device)
	}
	if !device.Spec.Devices[0].IsPrimaryENI || device.Spec.Devices[0].QueuePair != 120 ||
		device.Spec.Devices[1].ID != "eni-secondary" || device.Spec.Devices[1].NetworkCardIndex != 1 ||
		device.Spec.Devices[1].IsPrimaryENI || device.Spec.Devices[1].QueuePair != 120 {
		t.Fatalf("lost primary/secondary eRDMA topology: %+v", device.Spec.Devices)
	}
	devices := &ERdmaDeviceReconciler{Client: kube, EriClient: eri}
	if _, err := devices.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, req.NamespacedName, device); err != nil {
		t.Fatal(err)
	}
	if len(device.Status.Devices) != 2 || device.Status.Devices[0].Status != networkv1.DeviceStatusReady ||
		device.Status.Devices[1].Status != networkv1.DeviceStatusReady {
		t.Fatalf("unexpected status: %+v", device.Status)
	}
	if api.describeInstancesCalls != 0 || api.describeInstanceTypesCalls != 0 {
		t.Fatalf("Lingjun invoked ECS instance/capacity APIs: %+v", &api.fakeEriAPI)
	}
	checkLingjunTagRequest(t, api.tagRequest, device)
	if len(api.requests) != 2 || ptr.Deref(api.requests[0].InstanceId, "") != node.Name {
		t.Fatalf("unexpected ENI queries: %+v", api.requests)
	}
}

func checkLingjunTagRequest(t *testing.T, req *ecs.TagResourcesRequest, device *networkv1.ERdmaDevice) {
	t.Helper()
	if req == nil || len(req.ResourceId) != 2 {
		t.Fatalf("unexpected ENI tag request: %+v", req)
	}
	if *req.ResourceId[0] != device.Spec.Devices[0].ID || *req.ResourceId[1] != device.Spec.Devices[1].ID {
		t.Fatalf("must only tag selected eRDMA ENIs: %+v", req.ResourceId)
	}
	wantTags := []*ecs.TagResourcesRequestTag{
		{Key: ptr.To(eriTagExcludedKey), Value: ptr.To(eriTagExcludedValue)},
		{Key: ptr.To(eriTagInstanceIdKey), Value: ptr.To(device.Spec.Devices[0].InstanceID)},
	}
	if !reflect.DeepEqual(req.Tag, wantTags) {
		t.Fatalf("must tag Terway exclusion and instance identity only: %+v", req.Tag)
	}
}

func TestSelectLingjunERIsPaginationAndFailures(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "e01-test"}}
	api := &lingjunTestAPI{}
	for range 100 {
		api.enis = append(api.enis, attachedLingjunENI("eni-normal", "Normal"))
	}
	api.enis = append(api.enis, attachedLingjunENI("eni-z", trafficModeRDMA), attachedLingjunENI("eni-a", trafficModeRDMA))
	e := &EriClient{client: api}
	got, err := e.SelectLingjunERIs(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.requests) != 2 || len(got) != 2 || got[0].ID != "eni-a" || got[1].ID != "eni-z" {
		t.Fatalf("unexpected selection: %+v", got)
	}
	api.enis = nil
	if got, err = e.SelectLingjunERIs(context.Background(), node); err != nil || len(got) != 0 {
		t.Fatalf("unsupported node: %v %v", got, err)
	}
	api.err = errors.New("API failure")
	if _, err = e.SelectLingjunERIs(context.Background(), node); !errors.Is(err, api.err) {
		t.Fatalf("lost API error: %v", err)
	}
	api.err = nil
	bad := attachedLingjunENI("eni-bad", trafficModeRDMA)
	bad.MacAddress = nil
	api.enis = []*lingjunENI{bad}
	if _, err = e.SelectLingjunERIs(context.Background(), node); err == nil {
		t.Fatal("must reject incomplete ENI identity")
	}
}
