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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
)

const (
	erdmaFinalizer = "network.alibabacloud.com/erdma-controller"
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

	node := v1.Node{}
	err := r.Client.Get(ctx, req.NamespacedName, &node)
	if err != nil {
		if errors.IsNotFound(err) {
			return r.RemoveERdmaDevices(ctx, req.Name)
		}
		erdmaLogger.Error(err, "Failed to get node")
		return ctrl.Result{}, err
	}
	if !node.GetDeletionTimestamp().IsZero() {
		return r.RemoveERdmaDevices(ctx, req.Name)
	}
	erdmaLogger.WithValues("node", req).Info("Node Added")

	instanceID, err := r.EriClient.InstanceIDFromNode(&node)
	if err != nil {
		return ctrl.Result{}, err
	}
	erdmaDevices := networkv1.ERdmaDeviceList{}
	err = r.Client.List(ctx, &erdmaDevices, client.MatchingLabels{
		"alibabacloud.com/instance-id": instanceID,
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(erdmaDevices.Items) == 0 {
		eri, err := r.EriClient.EnsureEriForInstance(instanceID)
		if err != nil {
			return ctrl.Result{}, err
		}
		if eri == nil {
			erdmaLogger.Info("node not support erdma: %s(%s)", node.Name, instanceID)
			return ctrl.Result{}, nil
		}
		erdmaDevice := networkv1.ERdmaDevice{
			ObjectMeta: metav1.ObjectMeta{
				Name:       eri.ID,
				Finalizers: []string{erdmaFinalizer},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: node.APIVersion,
						Kind:       node.Kind,
						Name:       node.Name,
						UID:        node.UID,
					},
				},
				Labels: map[string]string{
					"alibabacloud.com/instance-id": instanceID,
					"alibabacloud.com/nodename":    node.Name,
				},
			},
			Spec: networkv1.ERdmaDeviceSpec{
				ID:           eri.ID,
				IsPrimaryENI: eri.IsPrimaryENI,
				MAC:          eri.MAC,
				InstanceID:   instanceID,
			},
		}
		return ctrl.Result{}, r.Client.Create(ctx, &erdmaDevice)
	}

	return ctrl.Result{}, nil
}

func (r *ERdmaDeviceReconciler) RemoveERdmaDevices(ctx context.Context, nodeName string) (ctrl.Result, error) {
	erdmaDevices := networkv1.ERdmaDeviceList{}
	err := r.Client.List(ctx, &erdmaDevices, client.MatchingLabels{
		"alibabacloud.com/nodename": nodeName,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if len(erdmaDevices.Items) == 0 {
		return ctrl.Result{}, nil
	}
	// todo remove erdma device
	for _, device := range erdmaDevices.Items {
		device.Finalizers = []string{}
		controllerutil.RemoveFinalizer(&device, erdmaFinalizer)

		update := device.DeepCopy()
		_, err := controllerutil.CreateOrPatch(ctx, r.Client, update, func() error {
			update.ObjectMeta = device.ObjectMeta
			return nil
		})
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	for _, erdmaDevice := range erdmaDevices.Items {
		err := r.Client.Delete(ctx, &erdmaDevice)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ERdmaDeviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Node{}).Owns(&networkv1.ERdmaDevice{}).
		Complete(r)
}
