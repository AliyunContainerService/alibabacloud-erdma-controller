package controller

import (
	"testing"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
