package deviceplugin

import (
	"context"
	"fmt"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/samber/lo"
	k8sType "k8s.io/apimachinery/pkg/types"
	"k8s.io/kubelet/pkg/apis/podresources/v1"
	"time"
)

const (
	defaultPodResourcesPath    = "/var/lib/kubelet/pod-resources/kubelet.sock"
	defaultPodResourcesTimeout = 10 * time.Second
)

func getPodDevices() (map[k8sType.NamespacedName][]string, error) {
	grpcConn, closeFunc, err := dial(defaultPodResourcesPath, defaultPodResourcesTimeout)
	if err != nil {
		return nil, fmt.Errorf("error dialing resource socket: %v, %v", defaultPodResourcesPath, err)
	}
	defer closeFunc()
	client := v1.NewPodResourcesListerClient(grpcConn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	podDevices := map[k8sType.NamespacedName][]string{}

	resp, err := client.List(ctx, &v1.ListPodResourcesRequest{})
	if err != nil {
		return nil, fmt.Errorf("%v.Get(_) = _, %v", client, err)
	}
	for _, pr := range resp.PodResources {
		var res []string
		for _, c := range pr.Containers {
			lo.ForEach(c.Devices, func(item *v1.ContainerDevices, _ int) {
				if item.ResourceName == types.ResourceName {
					res = append(res, item.DeviceIds...)
				}
			})
		}
		podDevices[k8sType.NamespacedName{
			Namespace: pr.Namespace,
			Name:      pr.Name,
		}] = res
	}
	return podDevices, nil
}

func getDevPod(devId string) (k8sType.NamespacedName, bool, error) {
	podDevices, err := getPodDevices()
	if err != nil {
		return k8sType.NamespacedName{}, false, err
	}
	for pod, devices := range podDevices {
		for _, device := range devices {
			if device == devId {
				return pod, true, nil
			}
		}
	}
	return k8sType.NamespacedName{}, false, nil
}
