package k8s

import (
	"context"
	"fmt"
	"os"
	"time"

	v1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/consts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
	ctrl "sigs.k8s.io/controller-runtime"
)

var scheme = runtime.NewScheme()

const (
	eriNodeNameLabel        = "alibabacloud.com/nodename"
	eriInfoListTimeout      = 10 * time.Second
	eriDeviceResourcePlural = "erdmadevices"
)

var eriDeviceGVR = schema.GroupVersionResource{
	Group:    v1.GroupVersion.Group,
	Version:  v1.GroupVersion.Version,
	Resource: eriDeviceResourcePlural,
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))
}

type Kubernetes interface {
	WaitEriInfo() (*v1.ERdmaDevice, error)
	WaitEriInfoContext(context.Context) (*v1.ERdmaDevice, error)
}

func NewKubernetes() (Kubernetes, error) {
	restConfig := ctrl.GetConfigOrDie()
	restConfig.UserAgent = consts.UA
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes dynamic client: %w", err)
	}

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("failed to get NODE_NAME")
	}
	return &k8s{
		nodeName: nodeName,
		resource: dynamicClient.Resource(eriDeviceGVR),
	}, nil
}

type k8s struct {
	nodeName string
	resource dynamic.ResourceInterface
}

func (k *k8s) WaitEriInfo() (*v1.ERdmaDevice, error) {
	return k.waitEriInfo(context.Background())
}

func (k *k8s) WaitEriInfoContext(ctx context.Context) (*v1.ERdmaDevice, error) {
	return k.waitEriInfo(ctx)
}

func hasERdmaDeviceSpec(device *v1.ERdmaDevice) bool {
	return device != nil && len(device.Spec.Devices) > 0
}

func (k *k8s) waitEriInfo(ctx context.Context) (*v1.ERdmaDevice, error) {
	selector := labels.Set{eriNodeNameLabel: k.nodeName}.AsSelector().String()
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = selector
			listCtx, cancel := context.WithTimeout(ctx, eriInfoListTimeout)
			defer cancel()
			return k.resource.List(listCtx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = selector
			return k.resource.Watch(ctx, options)
		},
	}
	objectType := &unstructured.Unstructured{}
	objectType.SetGroupVersionKind(v1.GroupVersion.WithKind("ERdmaDevice"))

	var device *v1.ERdmaDevice
	_, err := watchtools.UntilWithSync(ctx, lw, objectType, nil, func(event watch.Event) (bool, error) {
		if event.Type != watch.Added && event.Type != watch.Modified {
			return false, nil
		}
		object, ok := event.Object.(*unstructured.Unstructured)
		if !ok || object.GetName() != k.nodeName {
			return false, nil
		}
		device = &v1.ERdmaDevice{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, device); err != nil {
			return false, fmt.Errorf("decode erdma device %s: %w", k.nodeName, err)
		}
		if !hasERdmaDeviceSpec(device) {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("wait for erdma device %s: %w", k.nodeName, err)
	}
	return device, nil
}
