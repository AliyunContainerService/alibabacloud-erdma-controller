package controller

import (
	"fmt"
	"os"
	"strings"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
	"github.com/alibabacloud-go/endpoint-util/service"
	"github.com/alibabacloud-go/tea/tea"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/config"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

var eriLog = ctrl.Log.WithName("ERI")

const (
	eriTagCreatorKey    = "creator"
	eriTagCreatorValue  = "alibabacloud-erdma-controller"
	eriTagInstanceIdKey = "instance-id"

	trafficModeRDMA = "HighPerformance"
)

type EriClient struct {
	client          *ecs.Client
	regionID        string
	ManagedNonOwned bool
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
	return &EriClient{
		regionID:        config.GetConfig().Region,
		ManagedNonOwned: config.GetConfig().ManageNonOwnedERIs,
		client:          client,
	}, nil
}

func (e *EriClient) InstanceIDFromNode(node *corev1.Node) (string, error) {
	var instanceID string
	if node.Spec.ProviderID != "" {
		providerIDs := strings.Split(node.Spec.ProviderID, ".")
		if len(providerIDs) == 2 {
			instanceID = providerIDs[1]
		}
	}
	if instanceID != "" {
		resp, err := e.client.DescribeInstances(&ecs.DescribeInstancesRequest{
			RegionId:    ptr.To(e.regionID),
			InstanceIds: ptr.To(fmt.Sprintf("[\"%s\"]", instanceID)),
		})
		if err != nil {
			return "", fmt.Errorf("cannot found instance %s, %s", instanceID, err)
		}
		if *resp.Body.TotalCount == 0 {
			eriLog.Info("cannot found instance from providerID", "provider-id", node.Spec.ProviderID)
		} else {
			return *resp.Body.Instances.Instance[0].InstanceId, nil
		}
	}
	internalIP, ok := lo.Find(node.Status.Addresses, func(address corev1.NodeAddress) bool {
		return address.Type == corev1.NodeInternalIP
	})
	if !ok {
		return "", fmt.Errorf("cannot found instance from node internal ip")
	}
	resp, err := e.client.DescribeInstances(&ecs.DescribeInstancesRequest{
		RegionId:           ptr.To(e.regionID),
		PrivateIpAddresses: ptr.To(fmt.Sprintf("[\"%s\"]", internalIP.Address)),
	})
	if err != nil {
		return "", fmt.Errorf("cannot found instance %s, %s", internalIP.Address, err)
	}
	if *resp.Body.TotalCount == 0 {
		return "", fmt.Errorf("cannot found instance from node internal ip %s", internalIP.Address)
	}
	if *resp.Body.TotalCount > 1 {
		return "", fmt.Errorf("found multiple instance from node internal ip %s", internalIP.Address)
	}
	return *resp.Body.Instances.Instance[0].InstanceId, nil
}

func (e *EriClient) CreateEriForInstance(instanceInfo *ecs.DescribeInstancesResponseBodyInstancesInstance, cardIndex []int, queuePair int) ([]*types.ERI, error) {
	resp, err := e.client.DescribeNetworkInterfaces(&ecs.DescribeNetworkInterfacesRequest{
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
	if *resp.StatusCode != 200 {
		return nil, fmt.Errorf("describe network interface failed, status code: %d", resp.StatusCode)
	}
	var eris []*types.ERI
	for _, eni := range resp.Body.NetworkInterfaceSets.NetworkInterfaceSet {
		if len(cardIndex) > 0 {
			eri := toEri(eni, queuePair)
			eri.InstanceID = *instanceInfo.InstanceId
			eri.CardIndex = cardIndex[0]
			cardIndex = cardIndex[1:]
			eris = append(eris, eri)
		}
	}
	for len(cardIndex) > 0 {
		eriResp, err := e.client.CreateNetworkInterface(&ecs.CreateNetworkInterfaceRequest{
			NetworkInterfaceName:        ptr.To(fmt.Sprintf("eri-%s-%d", *instanceInfo.InstanceId, cardIndex[0])),
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
			CardIndex:    cardIndex[0],
		})
		cardIndex = cardIndex[1:]
	}
	return eris, nil
}

func (e *EriClient) ConvertPrimaryENI(primaryENI string, queuePair int) error {
	if _, err := e.client.ModifyNetworkInterfaceAttribute(&ecs.ModifyNetworkInterfaceAttributeRequest{
		RegionId:           ptr.To(e.regionID),
		NetworkInterfaceId: ptr.To(primaryENI),
		NetworkInterfaceTrafficConfig: &ecs.ModifyNetworkInterfaceAttributeRequestNetworkInterfaceTrafficConfig{
			NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA),
			// todo: not support dynamic set queue pair number
			QueuePairNumber: ptr.To(int32(queuePair)),
		},
	}); err != nil {
		return err
	}
	return nil
}

func (e *EriClient) SelectERIs(instanceID string) ([]*types.ERI, error) {
	instanceResp, err := e.client.DescribeInstances(&ecs.DescribeInstancesRequest{
		RegionId:    ptr.To(e.regionID),
		InstanceIds: ptr.To(fmt.Sprintf("[\"%s\"]", instanceID)),
	})
	if err != nil {
		return nil, fmt.Errorf("cannot found instance %s, %s", instanceID, err)
	}
	if *instanceResp.Body.TotalCount == 0 {
		return nil, fmt.Errorf("cannot found instance %s", instanceID)
	}
	instanceTypeResp, err := e.client.DescribeInstanceTypes(&ecs.DescribeInstanceTypesRequest{
		InstanceTypes: []*string{
			instanceResp.Body.Instances.Instance[0].InstanceType,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("cannot found instance type %s, %s", *instanceResp.Body.Instances.Instance[0].InstanceType, err)
	}
	var (
		cardCount      int
		queuePairCount int
	)
	for _, instanceType := range instanceTypeResp.Body.InstanceTypes.InstanceType {
		if instanceType.EriQuantity == nil {
			return nil, nil
		}
		eriQuantity := *instanceType.EriQuantity
		if *instanceType.InstanceTypeId == *instanceResp.Body.Instances.Instance[0].InstanceType && eriQuantity == 0 {
			return nil, nil
		}
		if instanceType.NetworkCardQuantity == nil || *instanceType.NetworkCardQuantity < 2 {
			cardCount = 1
		} else {
			cardCount = int(min(*instanceType.NetworkCardQuantity, eriQuantity))
		}
		queuePairCount = int(*instanceType.QueuePairNumber)
		// GPU instance max queue pair number is card count * queue pair number
		if instanceType.GPUAmount != nil && *instanceType.GPUAmount > 0 {
			queuePairCount = int(*instanceType.QueuePairNumber * int32(cardCount))
		}
	}

	describeENIResponse, err := e.client.DescribeNetworkInterfaces(&ecs.DescribeNetworkInterfacesRequest{
		RegionId:   ptr.To(e.regionID),
		InstanceId: ptr.To(instanceID),
		PageSize:   ptr.To(int32(100)),
	})
	if err != nil {
		return nil, fmt.Errorf("cannot found node eni: %v", err)
	}
	existENIs := describeENIResponse.Body.NetworkInterfaceSets.NetworkInterfaceSet
	selectEriList, needCreate, queuePairNumberConfig, err := e.SelectEriFromExist(existENIs, queuePairCount, cardCount)
	if err != nil {
		return nil, fmt.Errorf("cannot generate eri config list from exist enis: %v", err)
	}
	eris, err := e.CreateEriForInstance(instanceResp.Body.Instances.Instance[0], needCreate, queuePairNumberConfig)
	if err != nil {
		return nil, err
	}
	selectEriList = append(selectEriList, eris...)

	return selectEriList, nil
}

func (e *EriClient) SelectEriFromExist(existENIs []*ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, queuePairCount, cardCount int) ([]*types.ERI, []int, int, error) {
	var existQueuePairCount int
	existERIs := lo.Filter(existENIs, func(item *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, _ int) bool {
		eri := item.NetworkInterfaceTrafficMode != nil && *item.NetworkInterfaceTrafficMode == trafficModeRDMA
		if eri {
			existQueuePairCount += int(*item.QueuePairNumber)
		}
		return eri
	})
	eriLog.Info("exist eri", "existERIs", lo.Map(existERIs, func(item *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, _ int) *types.ERI {
		return toEri(item, 0)
	}), "existQueuePairCount", existQueuePairCount, "osMaxQueuePairCount", queuePairCount, "cardCount", cardCount)

	var (
		selectedENIs []*ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
		cardIndexENI = map[int]*types.ERI{}
	)

	for _, eri := range existERIs {
		if !e.OwnENI(eri) {
			continue
		}
		eniIndex := eniCardIndex(eri)
		if _, ok := cardIndexENI[eniIndex]; !ok {
			cardIndexENI[eniIndex] = toEri(eri, 0)
			selectedENIs = append(selectedENIs, eri)
		}
	}
	var needCreateOrConvert []int
	if existQueuePairCount <= queuePairCount {
		for i := 0; i < cardCount; i++ {
			if _, ok := cardIndexENI[i]; !ok {
				needCreateOrConvert = append(needCreateOrConvert, i)
			}
		}
	}

	var remainQueuePairCountPerCardIndex int
	if len(needCreateOrConvert) > 0 {
		remainQueuePairCountPerCardIndex = (queuePairCount - existQueuePairCount) / len(needCreateOrConvert)
		if remainQueuePairCountPerCardIndex > 0 {
			if _, ok := cardIndexENI[0]; !ok {
				// if cardIndex 0 not bind ENI, using primary ENI as cardIndex 0 ENI
				for _, eni := range existENIs {
					if eni.Type != nil && *eni.Type == "Primary" {
						selectedENIs = append(selectedENIs, eni)
						cardIndexENI[0] = toEri(eni, remainQueuePairCountPerCardIndex)
						cardIndex0Idx := lo.IndexOf(needCreateOrConvert, 0)
						// remove from create list
						needCreateOrConvert = append(needCreateOrConvert[:cardIndex0Idx], needCreateOrConvert[cardIndex0Idx+1:]...)
					}
				}
			}
			if len(cardIndexENI) == 0 {
				return nil, nil, 0, fmt.Errorf("cannot find node primary ENI or existing ENI")
			}
		} else {
			needCreateOrConvert = nil
		}
	}

	eriList := lo.Map(selectedENIs, func(item *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet, _ int) *types.ERI {
		return toEri(item, remainQueuePairCountPerCardIndex)
	})
	if len(eriList) == 0 && len(needCreateOrConvert) == 0 {
		return nil, nil, 0, fmt.Errorf("cannot create ERI for instance due to no available slot")
	}
	return eriList, needCreateOrConvert, remainQueuePairCountPerCardIndex, nil
}

func (e *EriClient) EnsureEriForInstance(devices []networkv1.DeviceInfo) ([]networkv1.DeviceStatus, error) {
	eniIds := lo.Map(devices, func(item networkv1.DeviceInfo, _ int) *string {
		return ptr.To(item.ID)
	})
	enis, err := e.client.DescribeNetworkInterfaces(&ecs.DescribeNetworkInterfacesRequest{
		NetworkInterfaceId: eniIds,
		PageSize:           ptr.To(int32(100)),
		RegionId:           ptr.To(e.regionID),
	})
	if err != nil {
		return nil, err
	}
	eniMap := lo.SliceToMap(enis.Body.NetworkInterfaceSets.NetworkInterfaceSet,
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
			_, err = e.client.AttachNetworkInterface(&req)
			if err != nil {
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
			err = e.ConvertPrimaryENI(device.ID, device.QueuePair)
			if err != nil {
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
		IsPrimaryENI: *eni.Type == "Primary",
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
