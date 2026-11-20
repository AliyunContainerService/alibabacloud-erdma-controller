package controller

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/consts"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func isLingjunNode(node *corev1.Node) bool {
	if value, ok := node.Labels[consts.LingjunWorkerLabel]; ok {
		return value == consts.LingjunWorkerLabelValue
	}
	// Lingjun nodes may only expose their e01-* node ID as providerID.
	// Do not treat arbitrary non-ECS provider IDs as Lingjun.
	return strings.HasPrefix(node.Spec.ProviderID, "e01-") && node.Spec.ProviderID == node.Name
}

// SelectLingjunERIs observes already provisioned eRDMA ENIs. Lingjun nodes are
// not ECS instances: do not query instance capacity or create/convert/attach ENIs.
// Only cloud-marked HighPerformance ENIs are eligible; the node's tail RDMA
// devices must never be discovered by scanning all local RDMA devices.
func (e *EriClient) SelectLingjunERIs(ctx context.Context, node *corev1.Node) ([]*types.ERI, error) {
	var result []*types.ERI
	for page := int32(1); ; page++ {
		resp, err := e.client.DescribeNetworkInterfaces(ctx, &ecs.DescribeNetworkInterfacesRequest{
			RegionId: ptr.To(e.regionID), InstanceId: ptr.To(node.Name),
			PageSize: ptr.To(int32(100)), PageNumber: ptr.To(page),
		})
		if err != nil {
			return nil, fmt.Errorf("describe Lingjun eRDMA ENIs for %s: %w", node.Name, err)
		}
		if resp == nil || resp.Body == nil {
			return nil, fmt.Errorf("describe Lingjun eRDMA ENIs for %s: missing response body", node.Name)
		}
		if resp.Body.NetworkInterfaceSets == nil {
			if ptr.Deref(resp.Body.TotalCount, int32(0)) == 0 {
				break
			}
			return nil, fmt.Errorf("describe Lingjun eRDMA ENIs for %s: missing ENI list", node.Name)
		}
		enis := resp.Body.NetworkInterfaceSets.NetworkInterfaceSet
		for _, eni := range enis {
			if eni == nil || ptr.Deref(eni.NetworkInterfaceTrafficMode, "") != trafficModeRDMA {
				continue
			}
			if ptr.Deref(eni.InstanceId, "") != node.Name || ptr.Deref(eni.Status, "") != types.ENIStatusInUse {
				return nil, fmt.Errorf("Lingjun eRDMA ENI %s is not attached to node %s", ptr.Deref(eni.NetworkInterfaceId, ""), node.Name)
			}
			if ptr.Deref(eni.NetworkInterfaceId, "") == "" || ptr.Deref(eni.Type, "") == "" {
				return nil, fmt.Errorf("Lingjun eRDMA ENI on %s has incomplete identity", node.Name)
			}
			if _, err := net.ParseMAC(ptr.Deref(eni.MacAddress, "")); err != nil {
				return nil, fmt.Errorf("Lingjun eRDMA ENI %s has invalid MAC: %w", *eni.NetworkInterfaceId, err)
			}
			result = append(result, toEri(eni, 0))
		}
		if len(enis) < 100 || (resp.Body.TotalCount != nil && int64(page)*100 >= int64(*resp.Body.TotalCount)) {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
