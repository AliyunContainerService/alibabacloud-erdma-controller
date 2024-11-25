package controller

import (
	"context"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/samber/lo"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"time"

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

// NodeReconciler reconciles a ERdmaDevice object
type NodeReconciler struct {
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
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	erdmaLogger := log.FromContext(ctx).WithName("node-controller")

	node := v1.Node{}
	err := r.Client.Get(ctx, req.NamespacedName, &node)
	if err != nil {
		if errors.IsNotFound(err) {
			return RemoveERdmaDevices(r.Client, ctx, req.Name)
		}
		erdmaLogger.Error(err, "Failed to get node")
		return ctrl.Result{}, err
	}
	if !node.GetDeletionTimestamp().IsZero() {
		return RemoveERdmaDevices(r.Client, ctx, req.Name)
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
		eri, err := r.EriClient.SelectERIs(instanceID)
		if err != nil {
			return ctrl.Result{}, err
		}
		if eri == nil {
			erdmaLogger.Info("node not support erdma", "name", node.Name, "instance-id", instanceID)
			return ctrl.Result{}, nil
		}
		erdmaDevice := networkv1.ERdmaDevice{
			ObjectMeta: metav1.ObjectMeta{
				Name:       node.Name,
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
				Devices: lo.Map(eri, func(item *types.ERI, index int) networkv1.DeviceInfo {
					return networkv1.DeviceInfo{
						InstanceID:       item.InstanceID,
						MAC:              item.MAC,
						IsPrimaryENI:     item.IsPrimaryENI,
						ID:               item.ID,
						NetworkCardIndex: item.CardIndex,
						QueuePair:        item.QueuePair,
					}
				}),
			},
		}
		err = r.Client.Create(ctx, &erdmaDevice)
		if err != nil {
			return ctrl.Result{}, err
		}
		_ = wait.PollWithContext(ctx, 500*time.Millisecond, 2*time.Second, func(ctx context.Context) (bool, error) {
			dev := &networkv1.ERdmaDevice{}
			err := r.Client.Get(ctx, k8stypes.NamespacedName{
				Name: erdmaDevice.Name,
			}, dev)
			if err != nil {
				return false, nil
			}
			return true, nil
		})

		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func RemoveERdmaDevices(erdmaClient client.Client, ctx context.Context, nodeName string) (ctrl.Result, error) {
	erdmaDevices := networkv1.ERdmaDeviceList{}
	err := erdmaClient.List(ctx, &erdmaDevices, client.MatchingLabels{
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
		_, err := controllerutil.CreateOrPatch(ctx, erdmaClient, update, func() error {
			update.ObjectMeta = device.ObjectMeta
			return nil
		})
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	for _, erdmaDevice := range erdmaDevices.Items {
		err := erdmaClient.Delete(ctx, &erdmaDevice)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	pred := predicate.TypedFuncs[*v1.Node]{
		CreateFunc: func(e event.TypedCreateEvent[*v1.Node]) bool {
			return true
		},
		DeleteFunc: func(e event.TypedDeleteEvent[*v1.Node]) bool {
			return true
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*v1.Node]) bool {
			if e.ObjectNew.DeletionTimestamp != nil {
				return true
			}
			return e.ObjectOld.Spec.ProviderID != e.ObjectNew.Spec.ProviderID
		},
		GenericFunc: func(e event.TypedGenericEvent[*v1.Node]) bool {
			return true
		},
	}
	c, err := controller.New("node-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	return c.Watch(source.Kind(mgr.GetCache(), &v1.Node{}, &handler.TypedEnqueueRequestForObject[*v1.Node]{}, pred))
}
