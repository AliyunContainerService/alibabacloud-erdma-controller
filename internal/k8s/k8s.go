package k8s

import (
	"context"
	"fmt"
	"os"
	"time"

	v1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/consts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	scheme = runtime.NewScheme()
	k8sLog = ctrl.Log.WithName("k8s")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))
}

type Kubernetes interface {
	WaitEriInfo() (*v1.ERdmaDevice, error)
}

func NewKubernetes() (Kubernetes, error) {
	restConfig := ctrl.GetConfigOrDie()
	restConfig.UserAgent = consts.UA
	c, err := client.New(restConfig, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, err
	}

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("failed to get NODE_NAME")
	}
	return &k8s{
		nodeName: nodeName,
		client:   c,
	}, nil
}

type k8s struct {
	nodeName string
	client   client.Client
}

func (k *k8s) WaitEriInfo() (*v1.ERdmaDevice, error) {
	device := &v1.ERdmaDevice{}
	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, 1*time.Minute, true, func(context.Context) (bool, error) {
		erdmaDeviceList := &v1.ERdmaDeviceList{}
		err := k.client.List(context.TODO(), erdmaDeviceList, client.MatchingLabels{
			"alibabacloud.com/nodename": k.nodeName,
		}, &client.ListOptions{Raw: &metav1.ListOptions{
			ResourceVersion: "0",
		}})
		if err != nil {
			k8sLog.Error(err, "failed to list erdma devices")
			return false, fmt.Errorf("failed to list erdma devices, %v", err)
		}
		if len(erdmaDeviceList.Items) == 0 {
			k8sLog.Info("waiting for erdma devices")
			return false, nil
		}
		device = &erdmaDeviceList.Items[0]
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to wait erdma devices, %v", err)
	}
	return device, nil
}
