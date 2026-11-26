package deviceplugin

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestReadinessFollowsKubeletStream(t *testing.T) {
	p := &ERDMADevicePlugin{Locker: &sync.Mutex{}, stop: make(chan struct{}), restartCh: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := make(chan struct{}, 1)
	stream := &recoveryStream{ctx: ctx, send: func(*pluginapi.ListAndWatchResponse) error {
		sent <- struct{}{}
		return nil
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.ListAndWatch(&pluginapi.Empty{}, stream)
	}()
	waitSignal(t, sent)
	deadline := time.After(time.Second)
	for !p.Ready() {
		select {
		case <-deadline:
			t.Fatal("plugin did not become ready after sending devices")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	waitSignal(t, done)
	if p.Ready() {
		t.Fatal("plugin remained ready after kubelet disconnected")
	}
}

func TestStopRemovesOnlyOwnSocket(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := grpc.NewServer(grpc.UnaryInterceptor(func(
		ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (interface{}, error) {
		close(entered)
		<-release
		return handler(ctx, req)
	}))
	p := &ERDMADevicePlugin{socket: socket, server: server, stop: make(chan struct{})}
	pluginapi.RegisterDevicePluginServer(server, p)
	go func() { _ = server.Serve(listener) }()
	connection, closeConnection, err := dial(socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConnection()
	go func() {
		_, _ = pluginapi.NewDevicePluginClient(connection).GetDevicePluginOptions(context.Background(), &pluginapi.Empty{})
	}()
	waitSignal(t, entered)
	other := filepath.Join(dir, "other.sock")
	if err := os.WriteFile(other, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p.ready.Store(true)
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("stop exceeded its grace period with an unresponsive RPC")
	}
	if p.Ready() {
		t.Fatal("stopped plugin remained ready")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("plugin socket still exists: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated socket removed: %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("repeated stop: %v", err)
	}
}
