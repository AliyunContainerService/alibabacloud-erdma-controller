package controller

import (
	"context"
	"errors"
	"sync"
	"testing"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

type fakeEriAPI struct {
	describeInstancesCalls          int
	describeInstanceTypesCalls      int
	describeNetworkInterfacesCalls  int
	tagResourcesCalls               int
	describeInstanceTypesMu         sync.Mutex
	describeInstanceTypesStarted    chan struct{}
	describeInstanceTypesRelease    chan struct{}
	describeInstanceTypesStartedOne sync.Once
	describeInstanceTypesErr        error
}

func (f *fakeEriAPI) DescribeInstances(context.Context, *ecs.DescribeInstancesRequest) (*ecs.DescribeInstancesResponse, error) {
	f.describeInstancesCalls++
	return &ecs.DescribeInstancesResponse{
		Body: &ecs.DescribeInstancesResponseBody{
			TotalCount: ptr.To(int32(1)),
			Instances: &ecs.DescribeInstancesResponseBodyInstances{Instance: []*ecs.DescribeInstancesResponseBodyInstancesInstance{{
				InstanceId:   ptr.To("i-test"),
				InstanceType: ptr.To("ecs.g8i.xlarge"),
			}}},
		},
	}, nil
}

func (f *fakeEriAPI) DescribeInstanceTypes(context.Context, *ecs.DescribeInstanceTypesRequest) (*ecs.DescribeInstanceTypesResponse, error) {
	f.describeInstanceTypesMu.Lock()
	f.describeInstanceTypesCalls++
	f.describeInstanceTypesMu.Unlock()
	if f.describeInstanceTypesStarted != nil {
		f.describeInstanceTypesStartedOne.Do(func() { close(f.describeInstanceTypesStarted) })
		<-f.describeInstanceTypesRelease
	}
	if f.describeInstanceTypesErr != nil {
		return nil, f.describeInstanceTypesErr
	}
	return &ecs.DescribeInstanceTypesResponse{
		Body: &ecs.DescribeInstanceTypesResponseBody{
			InstanceTypes: &ecs.DescribeInstanceTypesResponseBodyInstanceTypes{InstanceType: []*ecs.DescribeInstanceTypesResponseBodyInstanceTypesInstanceType{{
				InstanceTypeId:      ptr.To("ecs.g8i.xlarge"),
				EriQuantity:         ptr.To(int32(1)),
				NetworkCardQuantity: ptr.To(int32(1)),
				QueuePairNumber:     ptr.To(int32(2)),
			}}},
		},
	}, nil
}

func (f *fakeEriAPI) DescribeNetworkInterfaces(context.Context, *ecs.DescribeNetworkInterfacesRequest) (*ecs.DescribeNetworkInterfacesResponse, error) {
	f.describeNetworkInterfacesCalls++
	return &ecs.DescribeNetworkInterfacesResponse{
		Body: &ecs.DescribeNetworkInterfacesResponseBody{
			NetworkInterfaceSets: &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSets{NetworkInterfaceSet: []*ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{{
				NetworkInterfaceId:          ptr.To("eni-primary"),
				NetworkInterfaceTrafficMode: ptr.To("HighPerformance"),
				Status:                      ptr.To("InUse"),
				QueuePairNumber:             ptr.To(int32(2)),
				Type:                        ptr.To("Primary"),
				MacAddress:                  ptr.To("00:16:3e:00:00:01"),
				Attachment: &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
					NetworkCardIndex: ptr.To(int32(0)),
				},
			}}},
		},
	}, nil
}

func (f *fakeEriAPI) TagResources(context.Context, *ecs.TagResourcesRequest) (*ecs.TagResourcesResponse, error) {
	f.tagResourcesCalls++
	return &ecs.TagResourcesResponse{}, nil
}

func (f *fakeEriAPI) CreateNetworkInterface(context.Context, *ecs.CreateNetworkInterfaceRequest) (*ecs.CreateNetworkInterfaceResponse, error) {
	return nil, errors.New("unexpected CreateNetworkInterface call")
}

func (f *fakeEriAPI) ModifyNetworkInterfaceAttribute(context.Context, *ecs.ModifyNetworkInterfaceAttributeRequest) (*ecs.ModifyNetworkInterfaceAttributeResponse, error) {
	return nil, errors.New("unexpected ModifyNetworkInterfaceAttribute call")
}

func (f *fakeEriAPI) AttachNetworkInterface(context.Context, *ecs.AttachNetworkInterfaceRequest) (*ecs.AttachNetworkInterfaceResponse, error) {
	return nil, errors.New("unexpected AttachNetworkInterface call")
}

func (f *fakeEriAPI) DescribeInstanceAttribute(context.Context, *ecs.DescribeInstanceAttributeRequest) (*ecs.DescribeInstanceAttributeResponse, error) {
	return nil, errors.New("unexpected DescribeInstanceAttribute call")
}

func TestSelectERIsAvoidsRedundantOpenAPICalls(t *testing.T) {
	api := &fakeEriAPI{}
	client := &EriClient{
		client:          api,
		regionID:        "cn-hangzhou",
		ManagedNonOwned: true,
	}
	node := &corev1.Node{Spec: corev1.NodeSpec{ProviderID: "cn-hangzhou.i-test"}}

	instance, err := client.InstanceFromNode(context.Background(), node)
	if err != nil {
		t.Fatalf("InstanceFromNode() error = %v", err)
	}
	for range 2 {
		eris, err := client.SelectERIs(context.Background(), instance)
		if err != nil {
			t.Fatalf("SelectERIs() error = %v", err)
		}
		if len(eris) != 1 || eris[0].ID != "eni-primary" {
			t.Fatalf("SelectERIs() = %#v", eris)
		}
		status, err := client.EnsureEriForInstance(context.Background(), []networkv1.DeviceInfo{{
			ID:           eris[0].ID,
			InstanceID:   eris[0].InstanceID,
			IsPrimaryENI: eris[0].IsPrimaryENI,
			QueuePair:    eris[0].QueuePair,
		}})
		if err != nil {
			t.Fatalf("EnsureEriForInstance() error = %v", err)
		}
		if len(status) != 1 || status[0].Status != networkv1.DeviceStatusReady {
			t.Fatalf("EnsureEriForInstance() = %#v", status)
		}
	}

	if api.describeInstancesCalls != 1 {
		t.Fatalf("DescribeInstances calls = %d, want 1", api.describeInstancesCalls)
	}
	if api.describeInstanceTypesCalls != 1 {
		t.Fatalf("DescribeInstanceTypes calls = %d, want one cached lookup", api.describeInstanceTypesCalls)
	}
	if api.describeNetworkInterfacesCalls != 4 {
		t.Fatalf("DescribeNetworkInterfaces calls = %d, want one per SelectERIs and EnsureEriForInstance call", api.describeNetworkInterfacesCalls)
	}
	if api.tagResourcesCalls != 2 {
		t.Fatalf("TagResources calls = %d, want 2", api.tagResourcesCalls)
	}
}

func TestEriCapacityForInstanceTypeCoalescesConcurrentLookups(t *testing.T) {
	api := &fakeEriAPI{
		describeInstanceTypesStarted: make(chan struct{}),
		describeInstanceTypesRelease: make(chan struct{}),
	}
	client := &EriClient{client: api}
	const callers = 20
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)

	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			capacity, err := client.eriCapacityForInstanceType(context.Background(), "ecs.g8i.xlarge")
			if err == nil && (!capacity.supported || capacity.cardCount != 1 || capacity.queuePairCount != 2) {
				err = errors.New("unexpected cached capacity")
			}
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	<-api.describeInstanceTypesStarted
	close(api.describeInstanceTypesRelease)
	done.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("eriCapacityForInstanceType() error = %v", err)
		}
	}
	api.describeInstanceTypesMu.Lock()
	calls := api.describeInstanceTypesCalls
	api.describeInstanceTypesMu.Unlock()
	if calls != 1 {
		t.Fatalf("DescribeInstanceTypes calls = %d, want 1", calls)
	}
}
func TestEriCapacityForInstanceTypeDoesNotCacheErrors(t *testing.T) {
	api := &fakeEriAPI{describeInstanceTypesErr: errors.New("temporary failure")}
	client := &EriClient{client: api}

	if _, err := client.eriCapacityForInstanceType(context.Background(), "ecs.g8i.xlarge"); err == nil {
		t.Fatal("eriCapacityForInstanceType() error = nil, want temporary failure")
	}
	api.describeInstanceTypesErr = nil
	if _, err := client.eriCapacityForInstanceType(context.Background(), "ecs.g8i.xlarge"); err != nil {
		t.Fatalf("eriCapacityForInstanceType() retry error = %v", err)
	}
	if api.describeInstanceTypesCalls != 2 {
		t.Fatalf("DescribeInstanceTypes calls = %d, want retry after error", api.describeInstanceTypesCalls)
	}
}

func TestInstanceFromNodeDoesNotCacheLookup(t *testing.T) {
	api := &fakeEriAPI{}
	client := &EriClient{client: api}
	node := &corev1.Node{Spec: corev1.NodeSpec{ProviderID: "cn-hangzhou.i-test"}}

	for range 2 {
		instance, err := client.InstanceFromNode(context.Background(), node)
		if err != nil {
			t.Fatalf("InstanceFromNode() error = %v", err)
		}
		if instance.InstanceId == nil || *instance.InstanceId != "i-test" {
			t.Fatalf("InstanceFromNode() = %#v", instance)
		}
	}
	if api.describeInstancesCalls != 2 {
		t.Fatalf("DescribeInstances calls = %d, want one per lookup", api.describeInstancesCalls)
	}
}

func TestCreateEriForInstanceWithoutMissingCardsSkipsOpenAPI(t *testing.T) {
	client := &EriClient{}
	eris, err := client.CreateEriForInstance(context.Background(), nil, nil, 0)
	if err != nil {
		t.Fatalf("CreateEriForInstance() error = %v", err)
	}
	if len(eris) != 0 {
		t.Fatalf("CreateEriForInstance() = %#v, want no ERIs", eris)
	}
}

type retryEriAPI struct {
	fakeEriAPI
	attached, pending *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
	created           *ecs.CreateNetworkInterfaceRequest
	modified          *ecs.ModifyNetworkInterfaceAttributeRequest
	attachedRequest   *ecs.AttachNetworkInterfaceRequest
}

type lostCreateResponseAPI struct {
	retryEriAPI
	token    string
	response *ecs.CreateNetworkInterfaceResponse
}

func (f *lostCreateResponseAPI) CreateNetworkInterface(ctx context.Context, req *ecs.CreateNetworkInterfaceRequest) (*ecs.CreateNetworkInterfaceResponse, error) {
	token := ptr.Deref(req.ClientToken, "")
	if token == "" || len(token) > 64 {
		return nil, errors.New("missing or invalid client token")
	}
	if f.token == "" {
		f.token = token
		var err error
		f.response, err = f.retryEriAPI.CreateNetworkInterface(ctx, req)
		if err != nil {
			return nil, err
		}
		f.pending = nil // The tag lookup still cannot see the new ENI.
		return nil, context.DeadlineExceeded
	}
	if token != f.token {
		return nil, errors.New("create retry used a different client token")
	}
	return f.response, nil
}

func TestCreateERIRetryAfterLostResponseReusesClientToken(t *testing.T) {
	ctx := context.Background()
	api := &lostCreateResponseAPI{retryEriAPI: retryEriAPI{
		attached: &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{},
	}}
	instance := &ecs.DescribeInstancesResponseBodyInstancesInstance{
		InstanceId:       ptr.To("i-test"),
		SecurityGroupIds: &ecs.DescribeInstancesResponseBodyInstancesInstanceSecurityGroupIds{},
		VpcAttributes:    &ecs.DescribeInstancesResponseBodyInstancesInstanceVpcAttributes{},
	}
	first := &EriClient{client: api, regionID: "cn-hangzhou"}
	if _, err := first.CreateEriForInstance(ctx, instance, []int{1}, 60); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first create error = %v, want a lost response", err)
	}
	restarted := &EriClient{client: api, regionID: first.regionID}
	eris, err := restarted.CreateEriForInstance(ctx, instance, []int{1}, 60)
	if err != nil || len(eris) != 1 || eris[0].CardIndex != 1 {
		t.Fatalf("create after restart = %v, %v", eris, err)
	}
}

func (f *retryEriAPI) DescribeNetworkInterfaces(_ context.Context, req *ecs.DescribeNetworkInterfacesRequest) (*ecs.DescribeNetworkInterfacesResponse, error) {
	var interfaces []*ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
	switch {
	case len(req.NetworkInterfaceId) > 0:
		interfaces = append(interfaces, f.pending)
	case req.InstanceId != nil:
		interfaces = append(interfaces, f.attached)
	default:
		interfaces = append(interfaces, f.attached)
		if f.pending != nil {
			interfaces = append(interfaces, f.pending)
		}
	}
	return &ecs.DescribeNetworkInterfacesResponse{
		StatusCode: ptr.To(int32(200)),
		Body: &ecs.DescribeNetworkInterfacesResponseBody{
			NetworkInterfaceSets: &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSets{NetworkInterfaceSet: interfaces},
		},
	}, nil
}

func (f *retryEriAPI) CreateNetworkInterface(_ context.Context, req *ecs.CreateNetworkInterfaceRequest) (*ecs.CreateNetworkInterfaceResponse, error) {
	f.created = req
	f.pending = &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
		NetworkInterfaceId: ptr.To("eni-secondary"), Status: ptr.To("Available"),
		NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA),
	}
	return &ecs.CreateNetworkInterfaceResponse{Body: &ecs.CreateNetworkInterfaceResponseBody{
		NetworkInterfaceId: f.pending.NetworkInterfaceId, MacAddress: ptr.To("00:16:3e:00:00:02"),
	}}, nil
}

func (f *retryEriAPI) ModifyNetworkInterfaceAttribute(_ context.Context, req *ecs.ModifyNetworkInterfaceAttributeRequest) (*ecs.ModifyNetworkInterfaceAttributeResponse, error) {
	f.modified = req
	return &ecs.ModifyNetworkInterfaceAttributeResponse{}, nil
}

func (f *retryEriAPI) AttachNetworkInterface(_ context.Context, req *ecs.AttachNetworkInterfaceRequest) (*ecs.AttachNetworkInterfaceResponse, error) {
	f.attachedRequest = req
	return &ecs.AttachNetworkInterfaceResponse{}, nil
}

func TestSelectERIsCreatesOrRepairsSecondaryBeforeAttaching(t *testing.T) {
	const secondaryID = "eni-secondary"
	for _, tt := range []struct {
		name    string
		pending bool
		qp      *int32
	}{
		{name: "create with attached ERI in tagged results"},
		{name: "repair stale pending QP", pending: true, qp: ptr.To(int32(30))},
		{name: "set unreported pending QP", pending: true},
		{name: "reuse matching pending QP", pending: true, qp: ptr.To(int32(60))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			api := &retryEriAPI{attached: &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				NetworkInterfaceName: ptr.To("eri-i-test-0"), NetworkInterfaceId: ptr.To("eni-primary"),
				Type: ptr.To(primaryENIType), MacAddress: ptr.To("00:16:3e:00:00:01"),
				NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA), QueuePairNumber: ptr.To(int32(60)),
				Status: ptr.To("InUse"), InstanceId: ptr.To("i-test"),
			}}
			if tt.pending {
				api.pending = &ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
					NetworkInterfaceName: ptr.To("eri-i-test-1"), NetworkInterfaceId: ptr.To(secondaryID),
					Type: ptr.To("Secondary"), MacAddress: ptr.To("00:16:3e:00:00:02"),
					NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA), QueuePairNumber: tt.qp, Status: ptr.To("Available"),
				}
			}
			client := &EriClient{client: api}
			client.instanceTypeCapacities.Store("test-gpu", eriCapacity{supported: true, cardCount: 2, queuePairCount: 120})
			devices, err := client.SelectERIs(ctx, &ecs.DescribeInstancesResponseBodyInstancesInstance{
				InstanceId: ptr.To("i-test"), InstanceType: ptr.To("test-gpu"),
				SecurityGroupIds: &ecs.DescribeInstancesResponseBodyInstancesInstanceSecurityGroupIds{},
				VpcAttributes:    &ecs.DescribeInstancesResponseBodyInstancesInstanceVpcAttributes{},
			})
			if err != nil || len(devices) != 2 {
				t.Fatalf("SelectERIs() = %v, %v", devices, err)
			}
			if !tt.pending && (api.created == nil || ptr.Deref(api.created.QueuePairNumber, 0) != 60 || ptr.Deref(api.created.NetworkInterfaceName, "") != "eri-i-test-1") {
				t.Fatalf("unexpected create request: %v", api.created)
			}
			if tt.pending && api.created != nil {
				t.Fatalf("unexpected duplicate create: %v", api.created)
			}
			if tt.pending && ptr.Deref(tt.qp, 0) != 60 {
				if api.modified == nil || ptr.Deref(api.modified.NetworkInterfaceId, "") != secondaryID || ptr.Deref(api.modified.NetworkInterfaceTrafficConfig.QueuePairNumber, 0) != 60 {
					t.Fatalf("unexpected modify request: %v", api.modified)
				}
			} else if api.modified != nil {
				t.Fatalf("unexpected modify request: %v", api.modified)
			}
			dev := devices[1]
			if dev.ID != secondaryID || dev.CardIndex != 1 || dev.QueuePair != 60 || devices[0].QueuePair != 60 {
				t.Fatalf("unexpected allocation: primary=%+v, secondary=%+v", devices[0], dev)
			}
			_, err = client.EnsureEriForInstance(ctx, []networkv1.DeviceInfo{{
				ID: dev.ID, InstanceID: dev.InstanceID, NetworkCardIndex: dev.CardIndex, QueuePair: dev.QueuePair,
			}})
			if err != nil || api.attachedRequest == nil || ptr.Deref(api.attachedRequest.NetworkInterfaceId, "") != secondaryID ||
				ptr.Deref(api.attachedRequest.NetworkCardIndex, 0) != 1 || ptr.Deref(api.attachedRequest.InstanceId, "") != "i-test" {
				t.Fatalf("unexpected attach request: %v, %v", api.attachedRequest, err)
			}
		})
	}
}

func TestSelectEriFromExistRejectsUnknownQPUsage(t *testing.T) {
	client := &EriClient{}
	_, _, _, err := client.SelectEriFromExist([]*ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{{
		NetworkInterfaceId: ptr.To("eni-missing-qp"), NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA),
	}}, 120, 2)
	if err == nil {
		t.Fatal("SelectEriFromExist() accepted an ERI with unknown QP usage")
	}
}
