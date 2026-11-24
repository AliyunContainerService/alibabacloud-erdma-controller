package controller

import (
	"context"
	"fmt"
	"net"
	"sort"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/consts"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func isLingjunNode(node *corev1.Node) bool {
	return node.Labels[consts.LingjunWorkerLabel] == consts.LingjunWorkerLabelValue
}

// SelectLingjunERIs observes already provisioned eRDMA ENIs. Lingjun nodes are
// not ECS instances: do not query instance capacity or create/convert/attach ENIs.
// Only cloud-marked HighPerformance ENIs are eligible; the node's tail RDMA
// devices must never be discovered by scanning all local RDMA devices.
func (e *EriClient) SelectLingjunERIs(ctx context.Context, instanceID string) ([]*types.ERI, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("Lingjun node providerID is empty")
	}
	var result []*types.ERI
	for page := int32(1); ; page++ {
		resp, err := e.client.DescribeNetworkInterfaces(ctx, &ecs.DescribeNetworkInterfacesRequest{
			RegionId: ptr.To(e.regionID), InstanceId: ptr.To(instanceID),
			PageSize: ptr.To(int32(100)), PageNumber: ptr.To(page),
		})
		if err != nil {
			return nil, fmt.Errorf("describe Lingjun eRDMA ENIs for %s: %w", instanceID, err)
		}
		if resp == nil || resp.Body == nil {
			return nil, fmt.Errorf("describe Lingjun eRDMA ENIs for %s: missing response body", instanceID)
		}
		if resp.Body.NetworkInterfaceSets == nil {
			if ptr.Deref(resp.Body.TotalCount, int32(0)) == 0 {
				break
			}
			return nil, fmt.Errorf("describe Lingjun eRDMA ENIs for %s: missing ENI list", instanceID)
		}
		enis := resp.Body.NetworkInterfaceSets.NetworkInterfaceSet
		for _, eni := range enis {
			if eni == nil || ptr.Deref(eni.NetworkInterfaceTrafficMode, "") != trafficModeRDMA {
				continue
			}
			if ptr.Deref(eni.Status, "") != types.ENIStatusInUse {
				return nil, fmt.Errorf("Lingjun eRDMA ENI %s is not in use on instance %s", ptr.Deref(eni.NetworkInterfaceId, ""), instanceID)
			}
			if ptr.Deref(eni.NetworkInterfaceId, "") == "" || ptr.Deref(eni.Type, "") == "" {
				return nil, fmt.Errorf("Lingjun eRDMA ENI on %s has incomplete identity", instanceID)
			}
			if _, err := net.ParseMAC(ptr.Deref(eni.MacAddress, "")); err != nil {
				return nil, fmt.Errorf("Lingjun eRDMA ENI %s has invalid MAC: %w", *eni.NetworkInterfaceId, err)
			}
			eri := toEri(eni, 0)
			eri.InstanceID = instanceID
			result = append(result, eri)
		}
		if len(enis) < 100 || (resp.Body.TotalCount != nil && int64(page)*100 >= int64(*resp.Body.TotalCount)) {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if err := e.EnsureEriTags(ctx, lo.Map(result, func(item *types.ERI, _ int) string { return item.ID }), instanceID); err != nil {
		eriLog.Info("WARNING: skipped terway-compat tags on selected ERIs (best-effort)", "instanceID", instanceID, "error", err.Error())
	}
	return result, nil
}
