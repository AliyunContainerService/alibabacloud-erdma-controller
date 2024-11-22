package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"k8s.io/utils/ptr"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/api/consts"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/config"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var log = ctrl.Log.WithName("mutating-webhook")

// MutatingHook MutatingHook
func MutatingHook(client client.Client) *webhook.Admission {
	return &webhook.Admission{
		Handler: admission.HandlerFunc(func(ctx context.Context, req webhook.AdmissionRequest) webhook.AdmissionResponse {
			if !*config.GetConfig().EnableWebhook {
				return webhook.Allowed("webhook not enabled")
			}
			switch req.Kind.Kind {
			case "Pod":
				return podWebhook(ctx, &req, client)
			}
			return webhook.Allowed("not care")
		}),
	}
}

func podWebhook(_ context.Context, req *webhook.AdmissionRequest, client client.Client) webhook.AdmissionResponse {
	original := &corev1.Pod{}
	err := json.Unmarshal(req.Object.Raw, original)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed decoding pod: %s, %w", string(req.Object.Raw), err))
	}
	pod := original.DeepCopy()
	l := log.WithName(k8stypes.NamespacedName{
		Namespace: req.Namespace,
		Name:      req.Name,
	}.String())
	l.V(5).Info("checking pod")
	podAnnotations := pod.GetAnnotations()
	if podAnnotations == nil {
		return admission.Allowed("not rdma")
	}

	_, rdmaRes := lo.Find(append(pod.Spec.Containers, pod.Spec.InitContainers...), func(container corev1.Container) bool {
		if container.Resources.Limits != nil {
			if _, ok := container.Resources.Limits[types.ResourceName]; ok {
				return true
			}
		}
		if container.Resources.Requests != nil {
			if _, ok := container.Resources.Requests[types.ResourceName]; ok {
				return true
			}
		}
		return false
	})

	if rdmaRes && *config.GetConfig().EnableDevicePlugin {
		if _, ok := podAnnotations[consts.PodAnnotationSMCR]; ok && *config.GetConfig().EnableInitContainerInject {
			smcInitImage := config.GetConfig().SMCInitImage
			if smcInitImage == "" {
				smcInitImage = "registry.cn-hangzhou.aliyuncs.com/erdma/smcr_init:latest"
			}
			pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
				Name:            "smcr-init",
				Image:           smcInitImage,
				ImagePullPolicy: "Always",
				Command:         []string{"/usr/local/bin/smcr_init"},
				Resources: corev1.ResourceRequirements{
					Requests: map[corev1.ResourceName]resource.Quantity{types.ResourceName: resource.MustParse(strconv.Itoa(1))},
					Limits:   map[corev1.ResourceName]resource.Quantity{types.ResourceName: resource.MustParse(strconv.Itoa(1))},
				},
				SecurityContext: &corev1.SecurityContext{
					Privileged: ptr.To(true),
				},
			})
		}
	} else {
		return admission.Allowed("not rdma")
	}

	originalPatched, err := json.Marshal(original)
	if err != nil {
		l.Error(err, "error marshal origin podNetworking")
		return webhook.Errored(1, err)
	}

	podPatched, err := json.Marshal(pod)
	if err != nil {
		l.Error(err, "error marshal origin podNetworking")
		return webhook.Errored(1, err)
	}

	l.Info("patch pod for erdma", "namespace", pod.Namespace, "name", pod.Name)
	return admission.PatchResponseFromRaw(originalPatched, podPatched)
}
