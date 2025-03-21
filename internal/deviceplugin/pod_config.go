package deviceplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/api/consts"
	"github.com/docker/docker/client"
	"github.com/samber/lo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	k8sType "k8s.io/apimachinery/pkg/types"

	"k8s.io/klog/v2"

	internalapi "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	runtimeapiV1alpha2 "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
)

type podConfig struct {
	Netns string
	SMCR  bool
}

type Namespace struct {
	Type string `json:"type"`
	Path string `json:"path"`
}
type Linux struct {
	Namespaces []Namespace `json:"namespaces"`
}

type Config struct {
	Annotations map[string]string `json:"annotations"`
}

type RuntimeSpec struct {
	Linux Linux `json:"linux"`
}

type sandboxInfoSpec struct {
	Config      Config      `json:"config"`
	RuntimeSpec RuntimeSpec `json:"runtimeSpec"`
}

func getPodConfig(pod k8sType.NamespacedName) (*podConfig, error) {
	if dockerClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		podSandbox, err := dockerClient.ContainerList(ctx, types.ContainerListOptions{
			Filters: filters.NewArgs(
				filters.Arg("label", "io.kubernetes.docker.type=podsandbox"),
				filters.Arg("label", "io.kubernetes.pod.name="+pod.Name),
				filters.Arg("label", "io.kubernetes.pod.namespace="+pod.Namespace),
			),
		})
		if err != nil {
			return nil, fmt.Errorf("get pod sandbox list: %w", err)
		}
		if len(podSandbox) == 0 {
			return nil, fmt.Errorf("pod sandbox not found")
		}
		inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer inspectCancel()
		sandboxInfo, err := dockerClient.ContainerInspect(inspectCtx, podSandbox[0].ID)
		if err != nil {
			return nil, fmt.Errorf("get pod sandbox inspect: %w", err)
		}
		if sandboxInfo.State == nil || sandboxInfo.State.Pid == 0 {
			return nil, fmt.Errorf("pod sandbox state not expect: %v", sandboxInfo.State)
		}
		config := &podConfig{
			Netns: fmt.Sprintf("/proc/%d/ns/net", sandboxInfo.State.Pid),
		}

		if sandboxInfo.Config == nil || sandboxInfo.Config.Labels == nil {
			return config, nil
		}
		smcConfig, ok := sandboxInfo.Config.Labels["annotation."+consts.PodAnnotationSMCR]
		if !ok {
			return config, nil
		}
		config.SMCR, err = strconv.ParseBool(smcConfig)
		if err != nil {
			return nil, fmt.Errorf("pod sandbox state not expect: %v", sandboxInfo.State)
		}
		return config, nil
	}

	sandboxs, err := criClient.ListPodSandbox(nil)
	if err != nil {
		return nil, err
	}
	sandbox, ok := lo.Find(sandboxs, func(item *runtimeapi.PodSandbox) bool {
		return item.Metadata.Namespace == pod.Namespace && item.Metadata.Name == pod.Name && item.State == runtimeapi.PodSandboxState_SANDBOX_READY
	})
	if !ok {
		return nil, fmt.Errorf("pod sandbox not found")
	}
	sbStatus, err := criClient.PodSandboxStatus(sandbox.Id, true)
	if err != nil {
		return nil, err
	}
	sbConfig, ok := sbStatus.Info["info"]
	if !ok {
		return nil, fmt.Errorf("pod sandbox info not found")
	}
	sbSpec := &sandboxInfoSpec{}
	err = json.Unmarshal([]byte(sbConfig), sbSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sandbox info: %v", err)
	}
	var podConfig podConfig
	if sbStatus.Status != nil && sbStatus.Status.Linux != nil && sbStatus.Status.Linux.Namespaces != nil &&
		sbStatus.Status.Linux.Namespaces.Options != nil && sbStatus.Status.Linux.Namespaces.Options.Network == runtimeapi.NamespaceMode_NODE {
		podConfig.Netns = "/proc/1/ns/net"
		return &podConfig, nil
	}
	netns, ok := lo.Find(sbSpec.RuntimeSpec.Linux.Namespaces, func(item Namespace) bool {
		return item.Type == "network"
	})
	if !ok || netns.Path == "" {
		return nil, fmt.Errorf("failed to find network namespace")
	}
	podConfig.Netns = netns.Path
	smcConfig, ok := sbSpec.Config.Annotations[consts.PodAnnotationSMCR]
	if !ok || smcConfig == "" {
		return &podConfig, nil
	}
	podConfig.SMCR, err = strconv.ParseBool(smcConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pod sandbox info: %s, %v", smcConfig, err)
	}
	return &podConfig, nil
}

var (
	criClient    internalapi.RuntimeService
	dockerClient *client.Client
)

const (
	unixProtocol   = "unix"
	maxMsgSize     = 1024 * 1024 * 16
	kubeAPIVersion = "0.1.0"

	dockerShimSocket = "/var/run/dockershim.sock"
)

var runtimeEndpoints = []string{dockerShimSocket, "/run/containerd/containerd.sock", "/run/k3s/containerd/containerd.sock", "/var/run/cri-dockerd.sock"}

func initDockerClient() (err error) {
	dockerClient, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	return nil
}

func initCriClient(eps []string) (err error) {
	if criClient != nil {
		return nil
	}

	if sock, ok := os.LookupEnv("RUNTIME_SOCK"); ok {
		if _, err = os.Stat(sock); os.IsNotExist(err) {
			return fmt.Errorf("cannot find cri sock %s", sock)
		}
		criClient, err = NewRemoteRuntimeService(sock, 10*time.Second)
		if err != nil {
			return fmt.Errorf("connect cri sock %s error: %w", sock, err)
		}
		if sock == dockerShimSocket {
			return initDockerClient()
		}
		return err
	}

	for _, candidate := range eps {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			continue
		}
		criClient, err = NewRemoteRuntimeService(candidate, 10*time.Second)
		if err != nil {
			continue
		}
		if candidate == dockerShimSocket {
			err = initDockerClient()
			if err != nil {
				continue
			}
		}
		return err
	}

	return fmt.Errorf("cannot find valid cri sock in %s", strings.Join(eps, ","))
}

// remoteRuntimeService is a gRPC implementation of internalapi.RuntimeService.
type remoteRuntimeService struct {
	timeout               time.Duration
	runtimeClient         runtimeapi.RuntimeServiceClient
	runtimeClientV1alpha2 runtimeapiV1alpha2.RuntimeServiceClient
}

// useV1API returns true if the v1 CRI API should be used instead of runtimeapiV1alpha2.
func (r *remoteRuntimeService) useV1API() bool {
	return r.runtimeClientV1alpha2 == nil
}

func (r *remoteRuntimeService) versionV1alpha2(ctx context.Context, apiVersion string) (*runtimeapi.VersionResponse, error) {
	typedVersion, err := r.runtimeClientV1alpha2.Version(ctx, &runtimeapiV1alpha2.VersionRequest{
		Version: apiVersion,
	})
	if err != nil {
		return nil, err
	}

	if typedVersion.Version == "" || typedVersion.RuntimeName == "" || typedVersion.RuntimeApiVersion == "" || typedVersion.RuntimeVersion == "" {
		return nil, fmt.Errorf("not all fields are set in VersionResponse (%q)", *typedVersion)
	}

	return fromV1alpha2VersionResponse(typedVersion), err
}

// Version returns the runtime name, runtime version and runtime API version.
func (r *remoteRuntimeService) Version(apiVersion string) (*runtimeapi.VersionResponse, error) {

	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.versionV1(ctx, apiVersion)
	}

	return r.versionV1alpha2(ctx, apiVersion)
}

func (r *remoteRuntimeService) versionV1(ctx context.Context, apiVersion string) (*runtimeapi.VersionResponse, error) {
	typedVersion, err := r.runtimeClient.Version(ctx, &runtimeapi.VersionRequest{
		Version: apiVersion,
	})
	if err != nil {
		return nil, err
	}

	if typedVersion.Version == "" || typedVersion.RuntimeName == "" || typedVersion.RuntimeApiVersion == "" || typedVersion.RuntimeVersion == "" {
		return nil, fmt.Errorf("not all fields are set in VersionResponse (%q)", *typedVersion)
	}

	return typedVersion, err
}

func getConnection(ctx context.Context, endPoint string) (*grpc.ClientConn, error) {
	var conn *grpc.ClientConn
	addr, dialer, err := GetAddressAndDialer(endPoint)
	if err != nil {
		return nil, err
	}
	conn, err = grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(), grpc.WithContextDialer(dialer), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSize))) //nolint:staticcheck
	if err != nil {
		return nil, fmt.Errorf("connect endpoint '%s', make sure you are running as root and the endpoint has been started", endPoint)

	}
	return conn, nil
}

func NewRemoteRuntimeService(endpoint string, connectionTimeout time.Duration) (internalapi.RuntimeService, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	conn, err := getConnection(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	service := &remoteRuntimeService{
		timeout: connectionTimeout,
	}

	if err := service.determineAPIVersion(conn); err != nil {
		return nil, err
	}

	return service, nil
}

// Attach prepares a streaming endpoint to attach to a running container, and returns the address.
func (r *remoteRuntimeService) Attach(_ *runtimeapi.AttachRequest) (*runtimeapi.AttachResponse, error) {
	return nil, nil
}

// CheckpointContainer triggers a checkpoint of the given CheckpointContainerRequest
func (r *remoteRuntimeService) CheckpointContainer(_ *runtimeapi.CheckpointContainerRequest) error {
	return nil
}

// ContainerStats returns the stats of the container.
func (r *remoteRuntimeService) ContainerStats(_ string) (*runtimeapi.ContainerStats, error) {
	return nil, nil
}

// CreateContainer creates a new container in the specified PodSandbox.
func (r *remoteRuntimeService) CreateContainer(_ string, _ *runtimeapi.ContainerConfig, _ *runtimeapi.PodSandboxConfig) (string, error) {
	return "", nil
}

// Exec prepares a streaming endpoint to execute a command in the container, and returns the address.
func (r *remoteRuntimeService) Exec(_ *runtimeapi.ExecRequest) (*runtimeapi.ExecResponse, error) {
	return nil, nil
}

// ExecSync executes a command in the container, and returns the stdout output.
// If command exits with a non-zero exit code, an error is returned.
func (r *remoteRuntimeService) ExecSync(_ string, _ []string, _ time.Duration) (stdout []byte, stderr []byte, err error) {
	return nil, nil, nil
}

func (r *remoteRuntimeService) GetContainerEvents(_ chan *runtimeapi.ContainerEventResponse) error {
	return nil
}

// PortForward prepares a streaming endpoint to forward ports from a PodSandbox, and returns the address.
func (r *remoteRuntimeService) PortForward(_ *runtimeapi.PortForwardRequest) (*runtimeapi.PortForwardResponse, error) {
	return nil, nil
}

// RemoveContainer removes the container. If the container is running, the container
// should be forced to removal.
func (r *remoteRuntimeService) RemoveContainer(_ string) (err error) {
	return nil
}

// RemovePodSandbox removes the sandbox. If there are any containers in the
// sandbox, they should be forcibly removed.
func (r *remoteRuntimeService) RemovePodSandbox(_ string) (err error) {
	return nil
}

// ReopenContainerLog reopens the container log file.
func (r *remoteRuntimeService) ReopenContainerLog(_ string) (err error) {
	return nil
}

// RunPodSandbox creates and starts a pod-level sandbox. Runtimes should ensure
// the sandbox is in ready state.
func (r *remoteRuntimeService) RunPodSandbox(_ *runtimeapi.PodSandboxConfig, _ string) (string, error) {
	return "", nil
}

// StartContainer starts the container.
func (r *remoteRuntimeService) StartContainer(_ string) (err error) {
	return nil
}

// StopContainer stops a running container with a grace period (i.e., timeout).
func (r *remoteRuntimeService) StopContainer(_ string, _ int64) (err error) {
	return nil
}

// StopPodSandbox stops the sandbox. If there are any running containers in the
// sandbox, they should be forced to termination.
func (r *remoteRuntimeService) StopPodSandbox(_ string) (err error) {
	return nil
}

// UpdateContainerResources updates a containers resource config
func (r *remoteRuntimeService) UpdateContainerResources(_ string, _ *runtimeapi.ContainerResources) (err error) {
	return nil
}

// UpdateRuntimeConfig updates the config of a runtime service. The only
// update payload currently supported is the pod CIDR assigned to a node,
// and the runtime service just proxies it down to the network plugin.
func (r *remoteRuntimeService) UpdateRuntimeConfig(_ *runtimeapi.RuntimeConfig) (err error) {
	return nil
}

// Status returns the status of the runtime.
func (r *remoteRuntimeService) Status(verbose bool) (*runtimeapi.StatusResponse, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.statusV1(ctx, verbose)
	}

	return r.statusV1alpha2(ctx, verbose)
}

func (r *remoteRuntimeService) statusV1alpha2(ctx context.Context, verbose bool) (*runtimeapi.StatusResponse, error) {
	resp, err := r.runtimeClientV1alpha2.Status(ctx, &runtimeapiV1alpha2.StatusRequest{
		Verbose: verbose,
	})
	if err != nil {
		return nil, err
	}

	if resp.Status == nil || len(resp.Status.Conditions) < 2 {
		errorMessage := "RuntimeReady or NetworkReady condition are not set"
		err := errors.New(errorMessage)
		return nil, err
	}

	return fromV1alpha2StatusResponse(resp), nil
}

func (r *remoteRuntimeService) statusV1(ctx context.Context, verbose bool) (*runtimeapi.StatusResponse, error) {
	resp, err := r.runtimeClient.Status(ctx, &runtimeapi.StatusRequest{
		Verbose: verbose,
	})
	if err != nil {
		return nil, err
	}

	if resp.Status == nil || len(resp.Status.Conditions) < 2 {
		errorMessage := "RuntimeReady or NetworkReady condition are not set"
		err := errors.New(errorMessage)
		return nil, err
	}

	return resp, nil
}

// PodSandboxStatus returns the status of the PodSandbox.
func (r *remoteRuntimeService) PodSandboxStatus(podSandBoxID string, verbose bool) (*runtimeapi.PodSandboxStatusResponse, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.podSandboxStatusV1(ctx, podSandBoxID, verbose)
	}

	return r.podSandboxStatusV1alpha2(ctx, podSandBoxID, verbose)
}

func (r *remoteRuntimeService) podSandboxStatusV1alpha2(ctx context.Context, podSandBoxID string, verbose bool) (*runtimeapi.PodSandboxStatusResponse, error) {
	resp, err := r.runtimeClientV1alpha2.PodSandboxStatus(ctx, &runtimeapiV1alpha2.PodSandboxStatusRequest{
		PodSandboxId: podSandBoxID,
		Verbose:      verbose,
	})
	if err != nil {
		return nil, err
	}

	res := fromV1alpha2PodSandboxStatusResponse(resp)
	if res.Status != nil {
		if err := verifySandboxStatus(res.Status); err != nil {
			return nil, err
		}
	}

	return res, nil
}

func (r *remoteRuntimeService) podSandboxStatusV1(ctx context.Context, podSandBoxID string, verbose bool) (*runtimeapi.PodSandboxStatusResponse, error) {
	resp, err := r.runtimeClient.PodSandboxStatus(ctx, &runtimeapi.PodSandboxStatusRequest{
		PodSandboxId: podSandBoxID,
		Verbose:      verbose,
	})
	if err != nil {
		return nil, err
	}

	status := resp.Status
	if resp.Status != nil {
		if err := verifySandboxStatus(status); err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// PodSandboxStats returns the stats of the pod.
func (r *remoteRuntimeService) PodSandboxStats(podSandboxID string) (*runtimeapi.PodSandboxStats, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.podSandboxStatsV1(ctx, podSandboxID)
	}

	return r.podSandboxStatsV1alpha2(ctx, podSandboxID)
}

func (r *remoteRuntimeService) podSandboxStatsV1alpha2(ctx context.Context, podSandboxID string) (*runtimeapi.PodSandboxStats, error) {
	resp, err := r.runtimeClientV1alpha2.PodSandboxStats(ctx, &runtimeapiV1alpha2.PodSandboxStatsRequest{
		PodSandboxId: podSandboxID,
	})
	if err != nil {
		return nil, err
	}

	return fromV1alpha2PodSandboxStats(resp.GetStats()), nil
}

func (r *remoteRuntimeService) podSandboxStatsV1(ctx context.Context, podSandboxID string) (*runtimeapi.PodSandboxStats, error) {
	resp, err := r.runtimeClient.PodSandboxStats(ctx, &runtimeapi.PodSandboxStatsRequest{
		PodSandboxId: podSandboxID,
	})
	if err != nil {
		return nil, err
	}

	return resp.GetStats(), nil
}

// ListPodSandboxStats returns the list of pod sandbox stats given the filter
func (r *remoteRuntimeService) ListPodSandboxStats(filter *runtimeapi.PodSandboxStatsFilter) ([]*runtimeapi.PodSandboxStats, error) {
	// Set timeout, because runtimes are able to cache disk stats results
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.listPodSandboxStatsV1(ctx, filter)
	}

	return r.listPodSandboxStatsV1alpha2(ctx, filter)
}

func (r *remoteRuntimeService) listPodSandboxStatsV1alpha2(ctx context.Context, filter *runtimeapi.PodSandboxStatsFilter) ([]*runtimeapi.PodSandboxStats, error) {
	resp, err := r.runtimeClientV1alpha2.ListPodSandboxStats(ctx, &runtimeapiV1alpha2.ListPodSandboxStatsRequest{
		Filter: v1alpha2PodSandboxStatsFilter(filter),
	})
	if err != nil {
		return nil, err
	}

	return fromV1alpha2ListPodSandboxStatsResponse(resp).GetStats(), nil
}

func (r *remoteRuntimeService) listPodSandboxStatsV1(ctx context.Context, filter *runtimeapi.PodSandboxStatsFilter) ([]*runtimeapi.PodSandboxStats, error) {
	resp, err := r.runtimeClient.ListPodSandboxStats(ctx, &runtimeapi.ListPodSandboxStatsRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, err
	}

	return resp.GetStats(), nil
}

// ListPodSandbox returns a list of PodSandboxes.
func (r *remoteRuntimeService) ListPodSandbox(filter *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.listPodSandboxV1(ctx, filter)
	}

	return r.listPodSandboxV1alpha2(ctx, filter)
}

// ListContainers lists containers by filters.
func (r *remoteRuntimeService) ListContainers(filter *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.listContainersV1(ctx, filter)
	}

	return r.listContainersV1alpha2(ctx, filter)
}

func (r *remoteRuntimeService) listPodSandboxV1alpha2(ctx context.Context, filter *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error) {
	resp, err := r.runtimeClientV1alpha2.ListPodSandbox(ctx, &runtimeapiV1alpha2.ListPodSandboxRequest{
		Filter: v1alpha2PodSandboxFilter(filter),
	})
	if err != nil {
		return nil, err
	}

	return fromV1alpha2ListPodSandboxResponse(resp).Items, nil
}

func (r *remoteRuntimeService) listPodSandboxV1(ctx context.Context, filter *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error) {
	resp, err := r.runtimeClient.ListPodSandbox(ctx, &runtimeapi.ListPodSandboxRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, err
	}

	return resp.Items, nil
}

func (r *remoteRuntimeService) listContainersV1alpha2(ctx context.Context, filter *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	resp, err := r.runtimeClientV1alpha2.ListContainers(ctx, &runtimeapiV1alpha2.ListContainersRequest{
		Filter: v1alpha2ContainerFilter(filter),
	})
	if err != nil {
		return nil, err
	}

	return fromV1alpha2ListContainersResponse(resp).Containers, nil
}

func (r *remoteRuntimeService) listContainersV1(ctx context.Context, filter *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	resp, err := r.runtimeClient.ListContainers(ctx, &runtimeapi.ListContainersRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, err
	}

	return resp.Containers, nil
}

// ListContainerStats returns the list of ContainerStats given the filter.
func (r *remoteRuntimeService) ListContainerStats(filter *runtimeapi.ContainerStatsFilter) ([]*runtimeapi.ContainerStats, error) {
	// Do not set timeout, because writable layer stats collection takes time.
	// TODO(random-liu): Should we assume runtime should cache the result, and set timeout here?
	ctx, cancel := getContextWithCancel()
	defer cancel()

	if r.useV1API() {
		return r.listContainerStatsV1(ctx, filter)
	}

	return r.listContainerStatsV1alpha2(ctx, filter)
}

func (r *remoteRuntimeService) listContainerStatsV1(ctx context.Context, filter *runtimeapi.ContainerStatsFilter) ([]*runtimeapi.ContainerStats, error) {
	resp, err := r.runtimeClient.ListContainerStats(ctx, &runtimeapi.ListContainerStatsRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, err
	}

	return resp.GetStats(), nil
}

func (r *remoteRuntimeService) listContainerStatsV1alpha2(ctx context.Context, filter *runtimeapi.ContainerStatsFilter) ([]*runtimeapi.ContainerStats, error) {
	resp, err := r.runtimeClientV1alpha2.ListContainerStats(ctx, &runtimeapiV1alpha2.ListContainerStatsRequest{
		Filter: v1alpha2ContainerStatsFilter(filter),
	})
	if err != nil {
		return nil, err
	}

	return fromV1alpha2ListContainerStatsResponse(resp).GetStats(), nil
}

// ContainerStatus returns the container status.
func (r *remoteRuntimeService) ContainerStatus(containerID string, verbose bool) (*runtimeapi.ContainerStatusResponse, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	if r.useV1API() {
		return r.containerStatusV1(ctx, containerID, verbose)
	}

	return r.containerStatusV1alpha2(ctx, containerID, verbose)
}

func (r *remoteRuntimeService) containerStatusV1(ctx context.Context, containerID string, verbose bool) (*runtimeapi.ContainerStatusResponse, error) {
	resp, err := r.runtimeClient.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: containerID,
		Verbose:     verbose,
	})
	if err != nil {
		return nil, err
	}

	status := resp.Status
	if resp.Status != nil {
		if err := verifyContainerStatus(status); err != nil {
			return nil, err
		}
	}

	return resp, nil
}

func (r *remoteRuntimeService) containerStatusV1alpha2(ctx context.Context, containerID string, verbose bool) (*runtimeapi.ContainerStatusResponse, error) {
	resp, err := r.runtimeClientV1alpha2.ContainerStatus(ctx, &runtimeapiV1alpha2.ContainerStatusRequest{
		ContainerId: containerID,
		Verbose:     verbose,
	})
	if err != nil {
		return nil, err
	}

	res := fromV1alpha2ContainerStatusResponse(resp)
	if resp.Status != nil {
		if err := verifyContainerStatus(res.Status); err != nil {
			return nil, err
		}
	}

	return res, nil
}

// determineAPIVersion tries to connect to the remote runtime by using the
// highest available API version.
//
// A GRPC redial will always use the initially selected (or automatically
// determined) CRI API version. If the redial was due to the container runtime
// being upgraded, then the container runtime must also support the initially
// selected version or the redial is expected to fail, which requires a restart
// of kubelet.
func (r *remoteRuntimeService) determineAPIVersion(conn *grpc.ClientConn) error {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	r.runtimeClient = runtimeapi.NewRuntimeServiceClient(conn)

	if _, err := r.runtimeClient.Version(ctx, &runtimeapi.VersionRequest{}); err == nil {
		klog.Error("Using CRI v1 runtime API")
	} else if status.Code(err) == codes.Unimplemented {
		klog.Error("Using CRI v1alpha2 runtime API")
		r.runtimeClientV1alpha2 = runtimeapiV1alpha2.NewRuntimeServiceClient(conn)
	} else {
		return fmt.Errorf("unable to determine runtime API version: %w", err)
	}

	return nil
}

func parseEndpoint(endpoint string) (string, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", err
	}

	switch u.Scheme {
	case "tcp":
		return "tcp", u.Host, nil

	case "unix":
		return "unix", u.Path, nil

	case "":
		return "", "", fmt.Errorf("using %q as endpoint is deprecated, please consider using full url format", endpoint)

	default:
		return u.Scheme, "", fmt.Errorf("protocol %q not supported", u.Scheme)
	}
}

func parseEndpointWithFallbackProtocol(endpoint string, fallbackProtocol string) (protocol string, addr string, err error) {
	if protocol, addr, err = parseEndpoint(endpoint); err != nil && protocol == "" {
		fallbackEndpoint := fallbackProtocol + "://" + endpoint
		protocol, addr, err = parseEndpoint(fallbackEndpoint)
	}
	return
}

// GetAddressAndDialer returns the address parsed from the given endpoint and a context dialer.
func GetAddressAndDialer(endpoint string) (string, func(ctx context.Context, addr string) (net.Conn, error), error) {
	protocol, addr, err := parseEndpointWithFallbackProtocol(endpoint, unixProtocol)
	if err != nil {
		return "", nil, err
	}
	if protocol != unixProtocol {
		return "", nil, fmt.Errorf("only support unix socket endpoint")
	}

	return addr, dialUnix, nil
}

func dialUnix(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, unixProtocol, addr)
}

func fromV1alpha2VersionResponse(from *runtimeapiV1alpha2.VersionResponse) *runtimeapi.VersionResponse {
	return (*runtimeapi.VersionResponse)(unsafe.Pointer(from))
}

func fromV1alpha2PodSandboxStatusResponse(from *runtimeapiV1alpha2.PodSandboxStatusResponse) *runtimeapi.PodSandboxStatusResponse {
	return (*runtimeapi.PodSandboxStatusResponse)(unsafe.Pointer(from))
}

func fromV1alpha2ContainerStatusResponse(from *runtimeapiV1alpha2.ContainerStatusResponse) *runtimeapi.ContainerStatusResponse {
	return (*runtimeapi.ContainerStatusResponse)(unsafe.Pointer(from))
}

func v1alpha2ContainerStatsFilter(from *runtimeapi.ContainerStatsFilter) *runtimeapiV1alpha2.ContainerStatsFilter {
	return (*runtimeapiV1alpha2.ContainerStatsFilter)(unsafe.Pointer(from))
}

func fromV1alpha2ListContainerStatsResponse(from *runtimeapiV1alpha2.ListContainerStatsResponse) *runtimeapi.ListContainerStatsResponse {
	return (*runtimeapi.ListContainerStatsResponse)(unsafe.Pointer(from))
}

func fromV1alpha2ListPodSandboxResponse(from *runtimeapiV1alpha2.ListPodSandboxResponse) *runtimeapi.ListPodSandboxResponse {
	return (*runtimeapi.ListPodSandboxResponse)(unsafe.Pointer(from))
}

func fromV1alpha2ListContainersResponse(from *runtimeapiV1alpha2.ListContainersResponse) *runtimeapi.ListContainersResponse {
	return (*runtimeapi.ListContainersResponse)(unsafe.Pointer(from))
}

func fromV1alpha2ListPodSandboxStatsResponse(from *runtimeapiV1alpha2.ListPodSandboxStatsResponse) *runtimeapi.ListPodSandboxStatsResponse {
	return (*runtimeapi.ListPodSandboxStatsResponse)(unsafe.Pointer(from))
}

func fromV1alpha2PodSandboxStats(from *runtimeapiV1alpha2.PodSandboxStats) *runtimeapi.PodSandboxStats {
	return (*runtimeapi.PodSandboxStats)(unsafe.Pointer(from))
}

func fromV1alpha2StatusResponse(from *runtimeapiV1alpha2.StatusResponse) *runtimeapi.StatusResponse {
	return (*runtimeapi.StatusResponse)(unsafe.Pointer(from))
}

func v1alpha2ContainerFilter(from *runtimeapi.ContainerFilter) *runtimeapiV1alpha2.ContainerFilter {
	return (*runtimeapiV1alpha2.ContainerFilter)(unsafe.Pointer(from))
}

func v1alpha2PodSandboxFilter(from *runtimeapi.PodSandboxFilter) *runtimeapiV1alpha2.PodSandboxFilter {
	return (*runtimeapiV1alpha2.PodSandboxFilter)(unsafe.Pointer(from))
}

func v1alpha2PodSandboxStatsFilter(from *runtimeapi.PodSandboxStatsFilter) *runtimeapiV1alpha2.PodSandboxStatsFilter {
	return (*runtimeapiV1alpha2.PodSandboxStatsFilter)(unsafe.Pointer(from))
}

// verifySandboxStatus verified whether all required fields are set in PodSandboxStatus.
func verifySandboxStatus(status *runtimeapi.PodSandboxStatus) error {
	if status.Id == "" {
		return fmt.Errorf("status.Id is not set")
	}

	if status.Metadata == nil {
		return fmt.Errorf("status.Metadata is not set")
	}

	metadata := status.Metadata
	if metadata.Name == "" || metadata.Namespace == "" || metadata.Uid == "" {
		return fmt.Errorf("metadata.Name, metadata.Namespace or metadata.Uid is not in metadata %q", metadata)
	}

	if status.CreatedAt == 0 {
		return fmt.Errorf("status.CreatedAt is not set")
	}

	return nil
}

// getContextWithTimeout returns a context with timeout.
func getContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// getContextWithCancel returns a context with cancel.
func getContextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// verifyContainerStatus verified whether all required fields are set in ContainerStatus.
func verifyContainerStatus(status *runtimeapi.ContainerStatus) error {
	if status.Id == "" {
		return fmt.Errorf("status.Id is not set")
	}

	if status.Metadata == nil {
		return fmt.Errorf("status.Metadata is not set")
	}

	metadata := status.Metadata
	if metadata.Name == "" {
		return fmt.Errorf("metadata.Name is not in metadata %q", metadata)
	}

	if status.CreatedAt == 0 {
		return fmt.Errorf("status.CreatedAt is not set")
	}

	if status.Image == nil || status.Image.Image == "" {
		return fmt.Errorf("status.Image is not set")
	}

	if status.ImageRef == "" {
		return fmt.Errorf("status.ImageRef is not set")
	}

	return nil
}
