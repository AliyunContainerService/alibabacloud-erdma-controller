package k8s

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

type observedResource struct {
	dynamic.ResourceInterface
	watchStarted  chan struct{}
	watchOnce     sync.Once
	listSelector  string
	watchSelector string
}

func (r *observedResource) List(ctx context.Context, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	r.listSelector = options.LabelSelector
	return r.ResourceInterface.List(ctx, options)
}

func (r *observedResource) Watch(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	r.watchSelector = options.LabelSelector
	watcher, err := r.ResourceInterface.Watch(ctx, options)
	if err == nil {
		r.watchOnce.Do(func() { close(r.watchStarted) })
	}
	return watcher, err
}
func TestHasERdmaDeviceSpecRequiresAtLeastOneDevice(t *testing.T) {
	tests := []struct {
		name   string
		device *v1.ERdmaDevice
		want   bool
	}{
		{name: "nil"},
		{name: "no devices", device: &v1.ERdmaDevice{}},
		{name: "spec exists while status pending", device: &v1.ERdmaDevice{
			Spec:   v1.ERdmaDeviceSpec{Devices: []v1.DeviceInfo{{ID: "eni-1"}}},
			Status: v1.ERdmaDeviceStatus{Devices: []v1.DeviceStatus{{ID: "eni-1", Status: v1.DeviceStatusPending}}},
		}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasERdmaDeviceSpec(tt.device); got != tt.want {
				t.Fatalf("hasERdmaDeviceSpec() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestWaitEriInfoReturnsExistingSelectedObject(t *testing.T) {
	const nodeName = "node-target"
	resource := newObservedResource(
		erdmaDevice("node-other", v1.DeviceStatusReady),
		erdmaDevice(nodeName, v1.DeviceStatusReady),
	)
	k := &k8s{nodeName: nodeName, resource: resource}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := k.waitEriInfo(ctx)
	if err != nil {
		t.Fatalf("waitEriInfo() error = %v", err)
	}
	if got.Name != nodeName {
		t.Fatalf("waitEriInfo() name = %q, want %q", got.Name, nodeName)
	}
	wantSelector := eriNodeNameLabel + "=" + nodeName
	if resource.listSelector != wantSelector || resource.watchSelector != wantSelector {
		t.Fatalf("selectors = list %q watch %q, want %q", resource.listSelector, resource.watchSelector, wantSelector)
	}
}

func TestWaitEriInfoReturnsWhenWatchObservesCreation(t *testing.T) {
	const nodeName = "node-created-later"
	resource := newObservedResource()
	k := &k8s{nodeName: nodeName, resource: resource}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := make(chan *v1.ERdmaDevice, 1)
	errs := make(chan error, 1)
	go func() {
		device, err := k.waitEriInfo(ctx)
		if err != nil {
			errs <- err
			return
		}
		result <- device
	}()

	select {
	case <-resource.watchStarted:
	case err := <-errs:
		t.Fatalf("waitEriInfo() before watch error = %v", err)
	case <-ctx.Done():
		t.Fatal("watch did not start")
	}
	if _, err := resource.Create(ctx, toUnstructured(t, erdmaDevice(nodeName, v1.DeviceStatusPending)), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pending ERdmaDevice: %v", err)
	}

	select {
	case got := <-result:
		if got.Name != nodeName {
			t.Fatalf("waitEriInfo() name = %q, want %q", got.Name, nodeName)
		}
		if !hasERdmaDeviceSpec(got) || got.Status.Devices[0].Status != v1.DeviceStatusPending {
			t.Fatalf("waitEriInfo() did not return the first object with a populated spec: %#v", got)
		}
	case err := <-errs:
		t.Fatalf("waitEriInfo() error = %v", err)
	case <-ctx.Done():
		t.Fatal("waitEriInfo() did not return after watch creation event")
	}
}

func TestWaitEriInfoWaitsForExistingObjectSpecUpdate(t *testing.T) {
	const nodeName = "node-spec-populated-later"
	pending := erdmaDevice(nodeName, v1.DeviceStatusPending)
	pending.Spec.Devices = nil
	resource := newObservedResource(pending)
	k := &k8s{nodeName: nodeName, resource: resource}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := make(chan *v1.ERdmaDevice, 1)
	errs := make(chan error, 1)
	go func() {
		device, err := k.waitEriInfo(ctx)
		if err != nil {
			errs <- err
			return
		}
		result <- device
	}()

	select {
	case <-resource.watchStarted:
	case err := <-errs:
		t.Fatalf("waitEriInfo() before watch error = %v", err)
	case <-ctx.Done():
		t.Fatal("watch did not start")
	}

	current, err := resource.Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pending ERdmaDevice: %v", err)
	}
	populated := toUnstructured(t, erdmaDevice(nodeName, v1.DeviceStatusPending))
	current.Object["spec"] = populated.Object["spec"]
	if _, err := resource.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("populate ERdmaDevice spec: %v", err)
	}

	select {
	case got := <-result:
		if !hasERdmaDeviceSpec(got) || got.Spec.Devices[0].ID != "eni-"+nodeName {
			t.Fatalf("waitEriInfo() returned unexpected ERdmaDevice: %#v", got)
		}
	case err := <-errs:
		t.Fatalf("waitEriInfo() error = %v", err)
	case <-ctx.Done():
		t.Fatal("waitEriInfo() did not return after spec update")
	}
}

func newObservedResource(objects ...runtime.Object) *observedResource {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{eriDeviceGVR: "ERdmaDeviceList"},
		objects...,
	)
	return &observedResource{
		ResourceInterface: client.Resource(eriDeviceGVR),
		watchStarted:      make(chan struct{}),
	}
}

func erdmaDevice(nodeName, status string) *v1.ERdmaDevice {
	deviceID := "eni-" + nodeName
	return &v1.ERdmaDevice{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.GroupVersion.String(),
			Kind:       "ERdmaDevice",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeName,
			Labels: map[string]string{eriNodeNameLabel: nodeName},
		},
		Spec: v1.ERdmaDeviceSpec{Devices: []v1.DeviceInfo{{ID: deviceID}}},
		Status: v1.ERdmaDeviceStatus{Devices: []v1.DeviceStatus{{
			ID:     deviceID,
			Status: status,
		}}},
	}
}

func toUnstructured(t *testing.T, object runtime.Object) *unstructured.Unstructured {
	t.Helper()
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatalf("convert object to unstructured: %v", err)
	}
	return &unstructured.Unstructured{Object: content}
}

func TestWaitEriInfoPreservesCancellation(t *testing.T) {
	resource := newObservedResource()
	k := &k8s{nodeName: "node-no-devices", resource: resource}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := k.WaitEriInfoContext(ctx); done <- err }()
	select {
	case <-resource.watchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("device watch did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled watch returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled watch did not exit")
	}
}
