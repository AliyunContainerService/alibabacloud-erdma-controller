package controller

import (
	"fmt"
	"strings"

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
	client   *ecs.Client
	regionID string
}

func NewEriClient() (*EriClient, error) {
	cred, err := getCredential()
	if err != nil {
		return nil, err
	}

	client, err := ecs.NewClient(&openapi.Config{
		RegionId:   &config.GetConfig().Region,
		UserAgent:  ptr.To("AlibabaCloud/ERdma-Controller/0.1"),
		Credential: cred,
	})
	if err != nil {
		return nil, err
	}
	return &EriClient{
		regionID: config.GetConfig().Region,
		client:   client,
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

func (e *EriClient) EnsureEriForInstance(instanceID string) (*types.ERI, error) {
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
	for _, instanceType := range instanceTypeResp.Body.InstanceTypes.InstanceType {
		if *instanceType.InstanceTypeId == *instanceResp.Body.Instances.Instance[0].InstanceType && *instanceType.EriQuantity == 0 {
			return nil, nil
		}
	}

	existENIs, err := e.client.DescribeNetworkInterfaces(&ecs.DescribeNetworkInterfacesRequest{
		RegionId:   ptr.To(e.regionID),
		InstanceId: ptr.To(instanceID),
		PageSize:   ptr.To(int32(100)),
	})
	if err != nil {
		return nil, fmt.Errorf("cannot found node eni: %v", err)
	}
	var selectedENI *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
	for _, eni := range existENIs.Body.NetworkInterfaceSets.NetworkInterfaceSet {
		if eni.Type != nil && *eni.Type == "Primary" {
			selectedENI = eni
			break
		}
	}
	if selectedENI == nil {
		return nil, fmt.Errorf("cannot found node primary eni")
	}
	if selectedENI.NetworkInterfaceTrafficMode != nil && *selectedENI.NetworkInterfaceTrafficMode == trafficModeRDMA {
		return toEri(selectedENI), nil
	}
	if _, err := e.client.ModifyNetworkInterfaceAttribute(&ecs.ModifyNetworkInterfaceAttributeRequest{
		RegionId:           ptr.To(e.regionID),
		NetworkInterfaceId: selectedENI.NetworkInterfaceId,
		NetworkInterfaceTrafficConfig: &ecs.ModifyNetworkInterfaceAttributeRequestNetworkInterfaceTrafficConfig{
			NetworkInterfaceTrafficMode: ptr.To(trafficModeRDMA),
		},
	}); err != nil {
		return nil, err
	}

	return toEri(selectedENI), nil
}

func toEri(eni *ecs.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet) *types.ERI {
	return &types.ERI{
		ID:           *eni.NetworkInterfaceId,
		IsPrimaryENI: *eni.Type == "Primary",
		MAC:          *eni.MacAddress,
		InstanceID:   *eni.InstanceId,
	}
}
