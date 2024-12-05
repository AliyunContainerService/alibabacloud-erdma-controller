/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"time"

	"github.com/samber/lo"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
)

// ERdmaDeviceReconciler reconciles a ERdmaDevice object
type ERdmaDeviceReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	EriClient *EriClient
}

// +kubebuilder:rbac:groups=network.alibabacloud.com,resources=erdmadevices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.alibabacloud.com,resources=erdmadevices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.alibabacloud.com,resources=erdmadevices/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ERdmaDevice object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *ERdmaDeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	erdmaLogger := log.FromContext(ctx).WithName("erdma-controller")

	device := networkv1.ERdmaDevice{}
	err := r.Client.Get(ctx, req.NamespacedName, &device)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		erdmaLogger.Error(err, "Failed to get erdma device")
		return ctrl.Result{}, err
	}
	if !device.GetDeletionTimestamp().IsZero() {
		return RemoveERdmaDevices(r.Client, ctx, req.Name)
	}
	erdmaLogger.WithValues("erdma device", req).Info("erdma device Added")

	if len(device.Spec.Devices) == len(device.Status.Devices) {
		eriNeedConfig := lo.ContainsBy(device.Status.Devices, func(item networkv1.DeviceStatus) bool {
			return item.Status != networkv1.DeviceStatusReady
		})
		if !eriNeedConfig {
			return ctrl.Result{}, nil
		}
	}

	eriStatus, err := r.EriClient.EnsureEriForInstance(device.Spec.Devices)
	if err != nil {
		return ctrl.Result{}, err
	}
	device.Status.Devices = eriStatus
	err = r.Client.Status().Update(ctx, &device)
	if err != nil {
		return ctrl.Result{}, err
	}
	if lo.ContainsBy(eriStatus, func(item networkv1.DeviceStatus) bool {
		return item.Status != networkv1.DeviceStatusReady
	}) {
		return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ERdmaDeviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1.ERdmaDevice{}).
		Complete(r)
}
