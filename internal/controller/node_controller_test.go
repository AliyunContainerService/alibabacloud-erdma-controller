package controller

import (
	"context"
	"testing"
	"time"

	networkv1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNodeReconciler_OwnNode(t *testing.T) {
	testcases := []struct {
		name     string
		selector map[string]string
		node     *v1.Node
		expected bool
	}{
		{
			name:     "node is nil",
			selector: nil,
			node:     nil,
			expected: false,
		},
		{
			name:     "selector all nodes",
			selector: nil,
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kubernetes.io/nodename": "cn-hangzhou.1.1.1.1",
					},
				},
			},
			expected: true,
		},
		{
			name: "selector match",
			selector: map[string]string{
				"selector1": "value1",
			},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kubernetes.io/nodename": "cn-hangzhou.1.1.1.1",
						"selector1":              "value1",
					},
				},
			},
			expected: true,
		},
		{
			name: "selector partial match",
			selector: map[string]string{
				"selector1": "value1",
				"selector2": "value2",
			},
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kubernetes.io/nodename": "cn-hangzhou.1.1.1.1",
						"selector1":              "value1",
					},
				},
			},
			expected: false,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			r := &NodeReconciler{
				CtrlConfig: &types.Config{
					NodeSelector: tc.selector,
				},
			}
			result := r.OwnNode(tc.node)
			if result != tc.expected {
				t.Errorf("expected %v, but got %v", tc.expected, result)
			}
		})
	}
}

func TestIsNodeReady(t *testing.T) {
	tests := []struct {
		name     string
		node     *v1.Node
		expected bool
	}{
		{
			name: "Node is Ready",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "Node is NotReady",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionFalse},
					},
				},
			},
			expected: false,
		},
		{
			name:     "No conditions",
			node:     &v1.Node{},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNodeReady(tt.node); got != tt.expected {
				t.Errorf("isNodeReady() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestPredictNodeUpdate(t *testing.T) {
	tests := []struct {
		name     string
		oldNode  *v1.Node
		newNode  *v1.Node
		expected bool
	}{
		{
			name: "New node not owned",
			oldNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
			},
			newNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "different-value",
					},
				},
			},
			expected: false,
		},
		{
			name: "Old node not owned, new node owned",
			oldNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "not-owned",
					},
				},
			},
			newNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
			},
			expected: true,
		},
		{
			name: "Node with deletion timestamp",
			oldNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
			},
			newNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "node1",
					DeletionTimestamp: &metav1.Time{},
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
			},
			expected: true,
		},
		{
			name: "Node becomes Ready",
			oldNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionFalse},
					},
				},
			},
			newNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "Node stays Ready",
			oldNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionTrue},
					},
				},
			},
			newNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionTrue},
					},
				},
			},
			expected: false,
		},
		{
			name: "ProviderID changed",
			oldNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
				Spec: v1.NodeSpec{
					ProviderID: "old-provider-id",
				},
			},
			newNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test-key": "test-value",
					},
				},
				Spec: v1.NodeSpec{
					ProviderID: "new-provider-id",
				},
			},
			expected: true,
		},
	}

	reconciler := &NodeReconciler{
		CtrlConfig: &types.Config{
			NodeSelector: map[string]string{
				"test-key": "test-value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.PredictNodeUpdate(tt.oldNode, tt.newNode)
			if result != tt.expected {
				t.Errorf("PredictNodeUpdate() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestReconcileNodeReadyGate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = networkv1.AddToScheme(scheme)

	tests := []struct {
		name            string
		node            *v1.Node
		expectRequeue   bool
		expectRequeueAt time.Duration
		expectErr       bool
	}{
		{
			name: "NotReady within timeout requeues",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "node1",
					CreationTimestamp: metav1.Now(),
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionFalse},
					},
				},
			},
			expectRequeue:   true,
			expectRequeueAt: 30 * time.Second,
		},
		{
			name: "NotReady timeout exceeded proceeds",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "node2",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionFalse},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "Ready node proceeds",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "node3",
					CreationTimestamp: metav1.Now(),
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionTrue},
					},
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.node).
				Build()

			r := &NodeReconciler{
				Client:    fakeClient,
				Scheme:    scheme,
				EriClient: &EriClient{},
				CtrlConfig: &types.Config{
					WaitNodeReadyTimeoutSeconds: 300,
				},
			}

			result, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(tt.node),
			})

			if tt.expectRequeue {
				if result.RequeueAfter != tt.expectRequeueAt {
					t.Errorf("expected RequeueAfter %v, got %v", tt.expectRequeueAt, result.RequeueAfter)
				}
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if tt.expectErr && err == nil {
				t.Errorf("expected error from EriClient, got nil")
			}
			if result.RequeueAfter == 30*time.Second {
				t.Errorf("should not have requeued with 30s delay")
			}
		})
	}
}
