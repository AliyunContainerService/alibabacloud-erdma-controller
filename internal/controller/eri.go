package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
	aliyunclient "github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/aliyun/client"
	"github.com/alibabacloud-go/endpoint-util/service"
	"github.com/alibabacloud-go/tea/tea"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/config"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/samber/lo"
	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

var eriLog = ctrl.Log.WithName("ERI")

const (
	eriTagCreatorKey    = "creator"
	eriTagCreatorValue  = "alibabacloud-erdma-controller"
	eriTagInstanceIdKey = "instance-id"
	// eriTagExcludedKey marks an ENI as out-of-scope for other ENI managers
	// (notably terway CNI), so they will not allocate Pod IPs from it.
	eriTagExcludedKey   = "terway.alibabacloud.com/excluded"
	eriTagExcludedValue = "true"

	eniResourceType = "eni"

	trafficModeRDMA = "HighPerformance"
	primaryENIType  = "Primary"
)

type eriCapacity struct {
	supported      bool
	cardCount      int
	queuePairCount int
}

type eriAPI interface {
	DescribeInstances(context.Context, *ecs.DescribeInstancesRequest) (*ecs.DescribeInstancesResponse, error)
	DescribeNetworkInterfaces(context.Context, *ecs.DescribeNetworkInterfacesRequest) (*ecs.DescribeNetworkInterfacesResponse, error)
	CreateNetworkInterface(context.Context, *ecs.CreateNetworkInterfaceRequest) (*ecs.CreateNetworkInterfaceResponse, error)
	ModifyNetworkInterfaceAttribute(context.Context, *ecs.ModifyNetworkInterfaceAttributeRequest) (*ecs.ModifyNetworkInterfaceAttributeResponse, error)
	TagResources(context.Context, *ecs.TagResourcesRequest) (*ecs.TagResourcesResponse, error)
	DescribeInstanceTypes(context.Context, *ecs.DescribeInstanceTypesRequest) (*ecs.DescribeInstanceTypesResponse, error)
	AttachNetworkInterface(context.Context, *ecs.AttachNetworkInterfaceRequest) (*ecs.AttachNetworkInterfaceResponse, error)
	DescribeInstanceAttribute(context.Context, *ecs.DescribeInstanceAttributeRequest) (*ecs.DescribeInstanceAttributeResponse, error)
}

type EriClient struct {
	client                       eriAPI
	regionID                     string
	ManagedNonOwned              bool
	instanceTypeCapacities       sync.Map
	instanceTypeCapacityRequests singleflight.Group
}

func NewEriClient(k8sClient client.Client) (*EriClient, error) {
	cred, err := getCredential(k8sClient)
	if err != nil {
		return nil, err
	}
	network := "vpc"
	if os.Getenv("PUBLIC_NETWORK") == "true" {
		network = "public"
	}

	ecsEndpoint, err := service.GetEndpointRules(tea.String("ecs"), tea.String(config.GetConfig().Region), tea.String("regional"), tea.String(network), nil)
	if err != nil {
		return nil, err
	}
	client, err := ecs.NewClient(&openapi.Config{
		RegionId:     &config.GetConfig().Region,
		UserAgent:    ptr.To("AlibabaCloud/ERdma-Controller/0.1"),
		Credential:   cred,
		EndpointType: tea.String("regional"),
		Network:      tea.String(network),
		Endpoint:     ecsEndpoint,
	})
	if err != nil {
		return nil, err
	}
	ecsService, err := aliyunclient.NewECSService(client, config.GetConfig().RateLimit)
	if err != nil {
		return nil, fmt.Errorf("configure ECS OpenAPI rate limiter: %w", err)
	}
	return &EriClient{
		regionID:        config.GetConfig().Region,
		ManagedNonOwned: config.GetConfig().ManageNonOwnedERIs,
		client:          ecsService,
	}, nil
}

func (e *EriClient) InstanceFromNode(ctx context.Context, node *corev1.Node) (*ecs.DescribeInstancesResponseBodyInstancesInstance, error) {
	var instanceID string
	if node.Spec.ProviderID != "" {
		providerIDs := strings.Split(node.Spec.ProviderID, ".")
		if len(providerIDs) == 2 {
			instanceID = providerIDs[1]
		}
	}
	if instanceID != "" {
		resp, err := e.client.DescribeInstances(ctx, &ecs.DescribeInstancesRequest{
			RegionId:    ptr.To(e.regionID),
			InstanceIds: ptr.To(fmt.Sprintf("[\"%s\"]", instanceID)),
		})
		if err != nil {
			return nil, fmt.Errorf("cannot found instance %s, %w", instanceID, err)
		}
		if *resp.Body.TotalCount > 0 {
			return resp.Body.Instances.Instance[0], nil
		}
		eriLog.Info("cannot found instance from providerID", "provider-id", node.Spec.ProviderID)
	}
	internalIP, ok := lo.Find(node.Status.Addresses, func(address corev1.NodeAddress) bool {
		return address.Type == corev1.NodeInternalIP
	})
	if !ok {
		return nil, fmt.Errorf("cannot found instance from node internal ip")
	}
	resp, err := e.client.DescribeInstances(ctx, &ecs.DescribeInstancesRequest{
		RegionId:           ptr.To(e.regionID),
		PrivateIpAddresses: ptr.To(fmt.Sprintf("[\"%s\"]", internalIP.Address)),
	})
	if err != nil {
		return nil, fmt.Errorf("cannot found instance %s, %w", internalIP.Address, err)
	}
	if *resp.Body.TotalCount == 0 {
		return nil, fmt.Errorf("cannot found instance from node internal ip %s", internalIP.Address)
	}
	if *resp.Body.TotalCount > 1 {
		return nil, fmt.Errorf("found multiple instance from node internal ip %s", internalIP.Address)
	}
	return resp.Body.Instances.Instance[0], nil
}

func (e *EriClient) CreateEriForInstance(ctx context.Context, instanceInfo *ecs.DescribeInstancesResponseBodyInstancesInstance, cardIndex []int, queuePair int) ([]*types.ERI, error) {
	if len(cardIndex) == 0 {
		return nil, nil
	}
	resp, err := e.client.DescribeNetworkInterfaces(ctx, &ecs.DescribeNetworkInterfacesRequest{
		RegionId: ptr.To(e.regionID),
		Tag: []*ecs.DescribeNetworkInterfacesRequestTag{{
			Key:   ptr.To(eriTagCreatorKey),
			Value: ptr.To(eriTagCreatorValue),
		}, {
			Key:   ptr.To(eriTagInstanceIdKey),
			Value: instanceInfo.InstanceId,
		}},
		PageSize: ptr.To(int32(100)),
	})
	if err != nil {
		return nil, err
	}
	if status := ptr.Deref(resp.StatusCode, 0); status != 200 {
		return nil, fmt.Errorf("describe network interface failed, status code: %d", status)
	}
	eris := make([]*types.ERI, 0, len(cardIndex))
	for _, index := range cardIndex {
		name := fmt.Sprintf("eri-%s-%d", *instanceInfo.InstanceId, index)
		var pending *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
		if resp.Body != nil && resp.Body.NetworkInterfaceSets != nil {
			for _, eni := range resp.Body.NetworkInterfaceSets.NetworkInterfaceSet {
				if ptr.Deref(eni.NetworkInterfaceName, "") != name {
					continue
				}
				if pending != nil {
					return nil, fmt.Errorf("multiple ERIs match card %d on instance %s", index, *instanceInfo.InstanceId)
				}
				pending = eni
			}
		}
		if pending != nil {
			id, mac := ptr.Deref(pending.NetworkInterfaceId, ""), ptr.Deref(pending.MacAddress, "")
			if ptr.Deref(pending.Type, "") != "Secondary" || ptr.Deref(pending.NetworkInterfaceTrafficMode, "") != trafficModeRDMA ||
				ptr.Deref(pending.Status, "") != types.ENIStatusAvailable ||
				ptr.Deref(pending.InstanceId, "") != "" || id == "" || mac == "" {
				return nil, fmt.Errorf("matching ERI %s for card %d is not an available secondary ERI", name, index)
			}
			// Apply the planned QP count if the reported value is missing or differs.
			if int(ptr.Deref(pending.QueuePairNumber, 0)) != queuePair {
				if err := e.configureENIForRDMA(ctx, id, queuePair); err != nil {
					return nil, fmt.Errorf("configure QPs on unattached ERI %s: %w", id, err)
				}
			}
			eris = append(eris, &types.ERI{ID: id, MAC: mac, InstanceID: *instanceInfo.InstanceId, CardIndex: index, QueuePair: queuePair})
			continue
		}
		// Keep the token stable when a successful create is not visible to the next lookup.
		token := sha256.Sum256([]byte("erdma:create-eni:" + e.regionID + ":" + name))
		eriResp, err := e.client.CreateNetworkInterface(ctx, &ecs.CreateNetworkInterfaceRequest{
			ClientToken:                 ptr.To(fmt.Sprintf("%x", token)),
			NetworkInterfaceName:        ptr.To(name),
			NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA),
			QueuePairNumber:             ptr.To(int32(queuePair)),
			RegionId:                    ptr.To(e.regionID),
			SecurityGroupIds:            instanceInfo.SecurityGroupIds.SecurityGroupId,
			Tag: []*ecs.CreateNetworkInterfaceRequestTag{{
				Key:   ptr.To(eriTagCreatorKey),
				Value: ptr.To(eriTagCreatorValue),
			}, {
				Key:   ptr.To(eriTagInstanceIdKey),
				Value: instanceInfo.InstanceId,
			}, {
				Key:   ptr.To(eriTagExcludedKey),
				Value: ptr.To(eriTagExcludedValue),
			}},
			VSwitchId: instanceInfo.VpcAttributes.VSwitchId,
		})
		if err != nil {
			return nil, err
		}
		eris = append(eris, &types.ERI{
			ID:           *eriResp.Body.NetworkInterfaceId,
			IsPrimaryENI: false,
			MAC:          *eriResp.Body.MacAddress,
			InstanceID:   *instanceInfo.InstanceId,
			CardIndex:    index,
			QueuePair:    queuePair,
		})
	}
	return eris, nil
}

func (e *EriClient) ConvertPrimaryENI(ctx context.Context, primaryENI string, instanceID string, queuePair int) error {
	if err := e.configureENIForRDMA(ctx, primaryENI, queuePair); err != nil {
		return err
	}
	if err := e.EnsureEriTags(ctx, []string{primaryENI}, instanceID); err != nil {
		// Best-effort terway-compat tagging; not fatal to RDMA conversion.
		eriLog.Info("WARNING: skipped terway-compat tags on primary ENI after RDMA convert (best-effort)", "eni", primaryENI, "error", err.Error())
	}
	return nil
}

// configureENIForRDMA sets the ENI traffic mode to HighPerformance and configures its QP count.
func (e *EriClient) configureENIForRDMA(ctx context.Context, eniID string, queuePair int) error {
	_, err := e.client.ModifyNetworkInterfaceAttribute(ctx, &ecs.ModifyNetworkInterfaceAttributeRequest{
		RegionId:           ptr.To(e.regionID),
		NetworkInterfaceId: ptr.To(eniID),
		NetworkInterfaceTrafficConfig: &ecs.ModifyNetworkInterfaceAttributeRequestNetworkInterfaceTrafficConfig{
			NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA),
			QueuePairNumber:             ptr.To(int32(queuePair)),
		},
	})
	return err
}

// EnsureEriTags adds the terway-excluded and instance-id tags to the given ENIs.
// It deliberately does NOT add the creator tag because this method is called on
// ENIs that may not have been created by erdma-controller (e.g. ECS-console
// pre-bound ERDMA NICs or Primary ENIs converted to RDMA). The creator tag is
// only set at CreateNetworkInterface time for ENIs this controller actually creates.
// TagResources is idempotent on the cloud side: re-applying the same (key,value)
// pair is a no-op, so this is safe to call repeatedly. instanceID may be empty,
// in which case the instance-id tag is skipped.
func (e *EriClient) EnsureEriTags(ctx context.Context, eniIDs []string, instanceID string) error {
	if len(eniIDs) == 0 {
		return nil
	}
	tags := []*ecs.TagResourcesRequestTag{{
		Key:   ptr.To(eriTagExcludedKey),
		Value: ptr.To(eriTagExcludedValue),
	}}
	if instanceID != "" {
		tags = append(tags, &ecs.TagResourcesRequestTag{
			Key:   ptr.To(eriTagInstanceIdKey),
			Value: ptr.To(instanceID),
		})
	}
	_, err := e.client.TagResources(ctx, &ecs.TagResourcesRequest{
		RegionId:     ptr.To(e.regionID),
		ResourceType: ptr.To(eniResourceType),
		ResourceId:   lo.Map(eniIDs, func(id string, _ int) *string { return ptr.To(id) }),
		Tag:          tags,
	})
	if err != nil {
		return fmt.Errorf("tag ERI resources %v: %w", eniIDs, err)
	}
	return nil
}

func (e *EriClient) eriCapacityForInstanceType(ctx context.Context, instanceTypeID string) (eriCapacity, error) {
	if cached, ok := e.instanceTypeCapacities.Load(instanceTypeID); ok {
		return cached.(eriCapacity), nil
	}

	value, err, _ := e.instanceTypeCapacityRequests.Do(instanceTypeID, func() (any, error) {
		if cached, ok := e.instanceTypeCapacities.Load(instanceTypeID); ok {
			return cached.(eriCapacity), nil
		}
		capacity, err := e.loadERICapacityForInstanceType(ctx, instanceTypeID)
		if err != nil {
			return eriCapacity{}, err
		}
		e.instanceTypeCapacities.Store(instanceTypeID, capacity)
		return capacity, nil
	})
	if err != nil {
		return eriCapacity{}, err
	}
	return value.(eriCapacity), nil
}

func (e *EriClient) loadERICapacityForInstanceType(ctx context.Context, instanceTypeID string) (eriCapacity, error) {
	resp, err := e.client.DescribeInstanceTypes(ctx, &ecs.DescribeInstanceTypesRequest{
		InstanceTypes: []*string{ptr.To(instanceTypeID)},
	})
	if err != nil {
		return eriCapacity{}, fmt.Errorf("cannot found instance type %s, %w", instanceTypeID, err)
	}
	for _, instanceType := range resp.Body.InstanceTypes.InstanceType {
		if instanceType.InstanceTypeId == nil || *instanceType.InstanceTypeId != instanceTypeID {
			continue
		}
		capacity := eriCapacity{}
		if instanceType.EriQuantity == nil || *instanceType.EriQuantity == 0 {
			return capacity, nil
		}
		if instanceType.QueuePairNumber == nil {
			return eriCapacity{}, fmt.Errorf("instance type %s has no ERI queue-pair capacity", instanceTypeID)
		}
		capacity.supported = true
		if instanceType.NetworkCardQuantity == nil || *instanceType.NetworkCardQuantity < 2 {
			capacity.cardCount = 1
		} else {
			capacity.cardCount = int(min(*instanceType.NetworkCardQuantity, *instanceType.EriQuantity))
		}
		capacity.queuePairCount = int(*instanceType.QueuePairNumber)
		// GPU instance max queue pair number is card count * queue pair number
		if instanceType.GPUAmount != nil && *instanceType.GPUAmount > 0 {
			capacity.queuePairCount *= capacity.cardCount
		}
		return capacity, nil
	}
	return eriCapacity{}, fmt.Errorf("instance type %s was not returned by DescribeInstanceTypes", instanceTypeID)
}

func (e *EriClient) SelectERIs(ctx context.Context, instanceInfo *ecs.DescribeInstancesResponseBodyInstancesInstance) ([]*types.ERI, error) {
	if instanceInfo == nil || instanceInfo.InstanceId == nil || instanceInfo.InstanceType == nil {
		return nil, fmt.Errorf("instance response is missing ID or instance type")
	}
	instanceID := *instanceInfo.InstanceId
	capacity, err := e.eriCapacityForInstanceType(ctx, *instanceInfo.InstanceType)
	if err != nil {
		return nil, err
	}
	if !capacity.supported {
		return nil, nil
	}

	describeENIResponse, err := e.client.DescribeNetworkInterfaces(ctx, &ecs.DescribeNetworkInterfacesRequest{
		RegionId:   ptr.To(e.regionID),
		InstanceId: ptr.To(instanceID),
		PageSize:   ptr.To(int32(100)),
	})
	if err != nil {
		return nil, fmt.Errorf("cannot found node eni: %w", err)
	}
	existENIs := describeENIResponse.Body.NetworkInterfaceSets.NetworkInterfaceSet
	selectEriList, needCreate, queuePairNumberConfig, err := e.SelectEriFromExist(existENIs, capacity.queuePairCount, capacity.cardCount)
	if err != nil {
		return nil, fmt.Errorf("cannot generate eri config list from exist enis: %w", err)
	}
	eris, err := e.CreateEriForInstance(ctx, instanceInfo, needCreate, queuePairNumberConfig)
	if err != nil {
		return nil, err
	}
	selectEriList = append(selectEriList, eris...)

	// Backfill the terway-compat tags on every managed ERI (covers ECS-console
	// pre-bound ENIs and ENIs created by older versions that only had two tags).
	// Failure is non-fatal: ERDMA still works, terway compatibility is just
	// degraded until the next reconcile retries.
	if err := e.EnsureEriTags(ctx, lo.Map(selectEriList, func(item *types.ERI, _ int) string { return item.ID }), instanceID); err != nil {
		eriLog.Info("WARNING: skipped terway-compat tags on selected ERIs (best-effort)", "instanceID", instanceID, "error", err.Error())
	}
	return selectEriList, nil
}

func (e *EriClient) SelectEriFromExist(existENIs []*ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, queuePairCount, cardCount int) ([]*types.ERI, []int, int, error) {
	var existQueuePairCount int
	var primary *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
	existERIs := make([]*ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, 0, len(existENIs))
	for _, eni := range existENIs {
		if primary == nil && ptr.Deref(eni.Type, "") == primaryENIType {
			primary = eni
		}
		if ptr.Deref(eni.NetworkInterfaceTrafficMode, "") != trafficModeRDMA {
			continue
		}
		qps := ptr.Deref(eni.QueuePairNumber, 0)
		if qps <= 0 {
			return nil, nil, 0, fmt.Errorf("missing or invalid QP count for existing ERI %s", ptr.Deref(eni.NetworkInterfaceId, ""))
		}
		existQueuePairCount += int(qps)
		existERIs = append(existERIs, eni)
	}
	eriLog.Info("exist eri", "existERIs", lo.Map(existERIs, func(item *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, _ int) *types.ERI {
		return toEri(item, 0)
	}), "existQueuePairCount", existQueuePairCount, "osMaxQueuePairCount", queuePairCount, "cardCount", cardCount)

	eris := make([]*types.ERI, 0, len(existERIs))
	selectedCards := make(map[int]bool)
	for _, eri := range existERIs {
		index := eniCardIndex(eri)
		if !selectedCards[index] && e.OwnENI(eri) {
			selectedCards[index] = true
			eris = append(eris, toEri(eri, 0))
		}
	}
	// Reuse an enabled primary before counting slots that need QPs.
	if cardCount > 0 && !selectedCards[0] && primary != nil && ptr.Deref(primary.NetworkInterfaceTrafficMode, "") == trafficModeRDMA {
		selectedCards[0] = true
		eris = append(eris, toEri(primary, 0))
	}
	var needCreateOrConvert []int
	if existQueuePairCount <= queuePairCount {
		for i := 0; i < cardCount; i++ {
			if !selectedCards[i] {
				needCreateOrConvert = append(needCreateOrConvert, i)
			}
		}
	}

	var remainQueuePairCountPerCardIndex int
	if len(needCreateOrConvert) > 0 {
		remainQueuePairCountPerCardIndex = (queuePairCount - existQueuePairCount) / len(needCreateOrConvert)
		if remainQueuePairCountPerCardIndex > 0 {
			if needCreateOrConvert[0] == 0 && primary != nil {
				// Reserve the primary's share for conversion.
				eris = append(eris, toEri(primary, remainQueuePairCountPerCardIndex))
				needCreateOrConvert = needCreateOrConvert[1:]
			}
			if len(eris) == 0 {
				return nil, nil, 0, fmt.Errorf("cannot find node primary ENI or existing ENI")
			}
		} else {
			needCreateOrConvert = nil
		}
	}

	if len(eris) == 0 && len(needCreateOrConvert) == 0 {
		return nil, nil, 0, fmt.Errorf("cannot create ERI for instance due to no available slot")
	}
	return eris, needCreateOrConvert, remainQueuePairCountPerCardIndex, nil
}

func (e *EriClient) EnsureEriForInstance(ctx context.Context, devices []networkv1.DeviceInfo) ([]networkv1.DeviceStatus, error) {
	eniIDs := lo.Map(devices, func(item networkv1.DeviceInfo, _ int) string {
		return item.ID
	})
	resp, err := e.client.DescribeNetworkInterfaces(ctx, &ecs.DescribeNetworkInterfacesRequest{
		NetworkInterfaceId: lo.Map(eniIDs, func(id string, _ int) *string { return ptr.To(id) }),
		PageSize:           ptr.To(int32(100)),
		RegionId:           ptr.To(e.regionID),
	})
	if err != nil {
		return nil, err
	}
	eniList := resp.Body.NetworkInterfaceSets.NetworkInterfaceSet
	eniMap := lo.SliceToMap(eniList,
		func(item *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet) (string, *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet) {
			return *item.NetworkInterfaceId, item
		},
	)
	var devStatus []networkv1.DeviceStatus
	for _, device := range devices {
		eniStatus, ok := eniMap[device.ID]
		if !ok {
			return nil, fmt.Errorf("cannot found eni %s", device.ID)
		}
		if eniStatus.Status == nil {
			return nil, fmt.Errorf("cannot found eni %s status", device.ID)
		}
		if *eniStatus.Status == types.ENIStatusInUse && *eniStatus.NetworkInterfaceTrafficMode == trafficModeRDMA {
			devStatus = append(devStatus, networkv1.DeviceStatus{
				ID:      device.ID,
				Status:  networkv1.DeviceStatusReady,
				Message: "",
			})
		}
		if !device.IsPrimaryENI && *eniStatus.Status == types.ENIStatusAvailable {
			req := ecs.AttachNetworkInterfaceRequest{
				InstanceId:         ptr.To(device.InstanceID),
				NetworkInterfaceId: ptr.To(device.ID),
				RegionId:           ptr.To(e.regionID),
			}
			if device.NetworkCardIndex != 0 {
				req.NetworkCardIndex = ptr.To(int32(device.NetworkCardIndex))
			}
			_, err = e.client.AttachNetworkInterface(ctx, &req)
			if err != nil {
				if aliyunclient.IsThrottling(err) {
					return nil, err
				}
				devStatus = append(devStatus, networkv1.DeviceStatus{
					ID:      device.ID,
					Status:  networkv1.DeviceStatusFailed,
					Message: err.Error(),
				})
			} else {
				devStatus = append(devStatus, networkv1.DeviceStatus{
					ID:      device.ID,
					Status:  networkv1.DeviceStatusPending,
					Message: "",
				})
			}
		}
		if device.IsPrimaryENI && *eniStatus.Status == types.ENIStatusInUse && *eniStatus.NetworkInterfaceTrafficMode != trafficModeRDMA {
			err = e.ConvertPrimaryENI(ctx, device.ID, device.InstanceID, device.QueuePair)
			if err != nil {
				if aliyunclient.IsThrottling(err) {
					return nil, err
				}
				devStatus = append(devStatus, networkv1.DeviceStatus{
					ID:      device.ID,
					Status:  networkv1.DeviceStatusFailed,
					Message: err.Error(),
				})
			} else {
				devStatus = append(devStatus, networkv1.DeviceStatus{
					ID:     device.ID,
					Status: networkv1.DeviceStatusReady,
				})
			}
		}
	}
	return devStatus, nil
}

func (e *EriClient) IsJumboFrameEnabled(ctx context.Context, instanceID string) (bool, error) {
	resp, err := e.client.DescribeInstanceAttribute(ctx, &ecs.DescribeInstanceAttributeRequest{
		InstanceId: ptr.To(instanceID),
	})
	if err != nil {
		return false, fmt.Errorf("cannot describe instance attribute %s: %w", instanceID, err)
	}
	return jumboFrameFromAttr(resp.Body.EnableJumboFrame), nil
}

func jumboFrameFromAttr(enabled *bool) bool {
	return enabled != nil && *enabled
}

func (e *EriClient) OwnENI(eni *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet) bool {
	if e.ManagedNonOwned {
		return true
	}
	if eni.Tags == nil || eni.Tags.Tag == nil {
		return false
	}
	return lo.ContainsBy(eni.Tags.Tag, func(tag *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetTagsTag) bool {
		return tag.TagKey != nil && *tag.TagKey == eriTagCreatorKey &&
			tag.TagValue != nil && *tag.TagValue == eriTagCreatorValue
	})
}

func eniCardIndex(eni *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet) int {
	if eni.Attachment != nil && eni.Attachment.NetworkCardIndex != nil {
		return int(*eni.Attachment.NetworkCardIndex)
	}
	return 0
}

func toEri(eni *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, preferQueueCount int) *types.ERI {
	eri := &types.ERI{
		ID:           *eni.NetworkInterfaceId,
		IsPrimaryENI: *eni.Type == primaryENIType,
		MAC:          *eni.MacAddress,
		CardIndex:    eniCardIndex(eni),
		QueuePair:    preferQueueCount,
	}
	if eni.QueuePairNumber != nil && *eni.QueuePairNumber > 0 {
		eri.QueuePair = int(*eni.QueuePairNumber)
	}
	if eni.InstanceId != nil {
		eri.InstanceID = *eni.InstanceId
	}
	return eri
}
