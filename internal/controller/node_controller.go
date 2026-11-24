package controller

import (
	"context"
	stderrors "errors"
	"sync"
	"time"

	aliyunclient "github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/aliyun/client"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	coordinationv1 "k8s.io/api/coordination/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

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
	erdmaFinalizer           = "network.alibabacloud.com/erdma-controller"
	nodeNotReadyRequeueAfter = 30 * time.Second
)

// NodeReconciler reconciles a ERdmaDevice object
type NodeReconciler struct {
	client.Client
	APIReader               client.Reader
	Scheme                  *runtime.Scheme
	EriClient               *EriClient
	CtrlConfig              *types.Config
	MaxConcurrentReconciles int

	// taggedENIs tracks which ENIs have already been backfilled with the
	// terway-compat tags during this controller process lifetime, so that
	// existing ERdmaDevice CRs only trigger one TagResources call per ENI
	// across all reconcile passes.
	taggedENIs sync.Map
}

// +kubebuilder:rbac:groups=network.alibabacloud.com,resources=erdmadevices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.alibabacloud.com,resources=erdmadevices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.alibabacloud.com,resources=erdmadevices/finalizers,verbs=update
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch

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
	if !r.OwnNode(&node) {
		return ctrl.Result{}, nil
	}
	if !node.GetDeletionTimestamp().IsZero() {
		return RemoveERdmaDevices(r.Client, ctx, req.Name)
	}
	existingDevice := &networkv1.ERdmaDevice{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: node.Name}, existingDevice)
	deviceExists := err == nil
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if !isNodeReady(&node) {
		leaseReady, err := r.hasFreshNodeLease(ctx, &node, time.Now())
		if err != nil {
			return ctrl.Result{}, err
		}
		if leaseReady {
			erdmaLogger.Info("Node lease is active, proceeding before node is ready", "node", req.Name)
		} else {
			timeout := time.Duration(r.CtrlConfig.WaitNodeReadyTimeoutSeconds) * time.Second
			elapsed := time.Since(node.CreationTimestamp.Time)
			if elapsed < timeout {
				erdmaLogger.Info("Node is not ready, waiting", "node", req.Name, "elapsed", elapsed)
				return ctrl.Result{RequeueAfter: nodeNotReadyRequeueAfter}, nil
			}
			erdmaLogger.Info("Node is not ready but timeout exceeded, proceeding", "node", req.Name, "elapsed", elapsed)
		}
	}

	erdmaLogger.WithValues("node", req).Info("Node Added")
	if deviceExists {
		// Existing ERdmaDevice CR path: backfill terway-compat tags once per
		// controller lifetime so old nodes provisioned before this feature also
		// stop conflicting with terway.
		instanceID := existingDevice.Labels["alibabacloud.com/instance-id"]
		r.backfillEriTags(ctx, []networkv1.ERdmaDevice{*existingDevice}, instanceID, erdmaLogger)
		return ctrl.Result{}, nil
	}

	var (
		instanceID string
		eri        []*types.ERI
		jumboFrame bool
	)
	if isLingjunNode(&node) {
		instanceID = node.Spec.ProviderID
		eri, err = r.EriClient.SelectLingjunERIs(ctx, instanceID)
		if err != nil {
			return requeueOnECSThrottling(err, erdmaLogger)
		}
	} else {
		instanceInfo, err := r.EriClient.InstanceFromNode(ctx, &node)
		if err != nil {
			return requeueOnECSThrottling(err, erdmaLogger)
		}
		instanceID = *instanceInfo.InstanceId
		eri, err = r.EriClient.SelectERIs(ctx, instanceInfo)
		if err != nil {
			return requeueOnECSThrottling(err, erdmaLogger)
		}
		if len(eri) > 0 {
			jumboFrame, err = r.EriClient.IsJumboFrameEnabled(ctx, instanceID)
			if err != nil {
				var throttlingErr *aliyunclient.ThrottlingError
				if stderrors.As(err, &throttlingErr) {
					return requeueOnECSThrottling(err, erdmaLogger)
				}
				erdmaLogger.Error(err, "failed to check jumbo frame status, defaulting to false")
				jumboFrame = false
			}
		}
	}
	if len(eri) == 0 {
		erdmaLogger.Info("node not support erdma", "name", node.Name, "instance-id", instanceID)
		return ctrl.Result{}, nil
	}
	erdmaDevice := networkv1.ERdmaDevice{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
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
			JumboFrame: jumboFrame,
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

	return ctrl.Result{}, nil
}

func (r *NodeReconciler) backfillEriTags(ctx context.Context, devices []networkv1.ERdmaDevice, instanceID string, logger logr.Logger) {
	var pending []string
	for _, dev := range devices {
		for _, device := range dev.Spec.Devices {
			if device.ID == "" {
				continue
			}
			if _, loaded := r.taggedENIs.LoadOrStore(device.ID, struct{}{}); loaded {
				continue
			}
			pending = append(pending, device.ID)
		}
	}
	if len(pending) == 0 {
		return
	}
	if err := r.EriClient.EnsureEriTags(ctx, pending, instanceID); err != nil {
		// Roll back the in-memory marker so the next reconcile retries.
		for _, id := range pending {
			r.taggedENIs.Delete(id)
		}
		// Best-effort: terway does not strictly depend on these tags, and the
		// managed RAM role may lack ecs:TagResources. Log a warning instead of
		// an error (which would emit a noisy stack trace) and retry next reconcile.
		logger.Info("WARNING: skipped terway-compat tag backfill on existing ERIs (best-effort, will retry)", "enis", pending, "instanceID", instanceID, "error", err.Error())
		return
	}
	logger.Info("backfilled terway-compat tags on existing ERIs", "enis", pending, "instanceID", instanceID)
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
	// Object deletion after the List is already the desired result.
	for i := range erdmaDevices.Items {
		device := &erdmaDevices.Items[i]
		if !controllerutil.ContainsFinalizer(device, erdmaFinalizer) {
			continue
		}
		base := device.DeepCopy()
		controllerutil.RemoveFinalizer(device, erdmaFinalizer)
		if err := erdmaClient.Patch(ctx, device, client.MergeFrom(base)); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	for i := range erdmaDevices.Items {
		err := erdmaClient.Delete(ctx, &erdmaDevices.Items[i])
		if err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func isNodeReady(node *v1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == v1.NodeReady {
			return cond.Status == v1.ConditionTrue
		}
	}
	return false
}

func (r *NodeReconciler) hasFreshNodeLease(ctx context.Context, node *v1.Node, now time.Time) (bool, error) {
	lease := &coordinationv1.Lease{}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	err := reader.Get(ctx, client.ObjectKey{Namespace: v1.NamespaceNodeLease, Name: node.Name}, lease)
	if errors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !isFreshNodeLease(lease, now) {
		return false, nil
	}
	return lo.SomeBy(lease.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.Kind == "Node" && ref.UID == node.UID
	}), nil
}

func isFreshNodeLease(lease *coordinationv1.Lease, now time.Time) bool {
	if lease == nil || lease.Namespace != v1.NamespaceNodeLease ||
		lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != lease.Name ||
		lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil ||
		*lease.Spec.LeaseDurationSeconds <= 0 {
		return false
	}

	expiresAt := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
	return now.Before(expiresAt)
}

func (r *NodeReconciler) OwnNode(node *v1.Node) bool {
	if node == nil {
		return false
	}
	for k, v := range r.CtrlConfig.NodeSelector {
		if node.Labels[k] != v {
			return false
		}
	}
	return true
}

func (r *NodeReconciler) PredictNodeUpdate(oldNode, newNode *v1.Node) bool {
	if !r.OwnNode(newNode) {
		return false
	}
	if !r.OwnNode(oldNode) && r.OwnNode(newNode) {
		return true
	}

	if newNode.DeletionTimestamp != nil {
		return true
	}

	if !isNodeReady(oldNode) && isNodeReady(newNode) {
		return true
	}

	return oldNode.Spec.ProviderID != newNode.Spec.ProviderID || isLingjunNode(oldNode) != isLingjunNode(newNode)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.APIReader = mgr.GetAPIReader()
	nodePred := predicate.TypedFuncs[*v1.Node]{
		CreateFunc: func(e event.TypedCreateEvent[*v1.Node]) bool {
			return r.OwnNode(e.Object)
		},
		DeleteFunc: func(e event.TypedDeleteEvent[*v1.Node]) bool {
			return r.OwnNode(e.Object)
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*v1.Node]) bool {
			return r.PredictNodeUpdate(e.ObjectOld, e.ObjectNew)
		},
		GenericFunc: func(e event.TypedGenericEvent[*v1.Node]) bool {
			return r.OwnNode(e.Object)
		},
	}
	c, err := controller.New("node-controller", mgr, controller.Options{
		Reconciler:              r,
		MaxConcurrentReconciles: r.MaxConcurrentReconciles,
	})
	if err != nil {
		return err
	}
	if err := c.Watch(source.Kind(mgr.GetCache(), &v1.Node{}, &handler.TypedEnqueueRequestForObject[*v1.Node]{}, nodePred)); err != nil {
		return err
	}

	leasePred := predicate.TypedFuncs[*coordinationv1.Lease]{
		CreateFunc: func(e event.TypedCreateEvent[*coordinationv1.Lease]) bool {
			return isFreshNodeLease(e.Object, time.Now())
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*coordinationv1.Lease]) bool {
			now := time.Now()
			return !isFreshNodeLease(e.ObjectOld, now) && isFreshNodeLease(e.ObjectNew, now)
		},
		DeleteFunc: func(event.TypedDeleteEvent[*coordinationv1.Lease]) bool {
			return false
		},
		GenericFunc: func(event.TypedGenericEvent[*coordinationv1.Lease]) bool {
			return false
		},
	}
	leaseToNode := handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, lease *coordinationv1.Lease) []ctrl.Request {
		if lease.Namespace != v1.NamespaceNodeLease {
			return nil
		}
		return []ctrl.Request{{NamespacedName: k8stypes.NamespacedName{Name: lease.Name}}}
	})
	return c.Watch(source.Kind(mgr.GetCache(), &coordinationv1.Lease{}, leaseToNode, leasePred))
}
