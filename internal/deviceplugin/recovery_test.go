package deviceplugin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type recoveryStream struct {
	grpc.ServerStream
	ctx  context.Context
	send func(*pluginapi.ListAndWatchResponse) error
}

func (s *recoveryStream) Context() context.Context                     { return s.ctx }
func (s *recoveryStream) Send(r *pluginapi.ListAndWatchResponse) error { return s.send(r) }

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for stream signal")
	}
}

func TestListAndWatchRecovery(t *testing.T) {
	for _, mode := range []string{"initial send failure", "periodic send failure", "disconnect", "local stop", "old generation"} {
		t.Run(mode, func(t *testing.T) {
			p := &ERDMADevicePlugin{Locker: &sync.Mutex{}, stop: make(chan struct{}), restartCh: make(chan struct{}, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sent := make(chan struct{}, 1)
			calls := 0
			stream := &recoveryStream{ctx: ctx, send: func(*pluginapi.ListAndWatchResponse) error {
				calls++
				if mode == "initial send failure" || (mode == "periodic send failure" && calls > 1) {
					return errors.New("disconnected")
				}
				sent <- struct{}{}
				return nil
			}}
			done := make(chan struct{})
			go func() { defer close(done); p.ListAndWatch(&pluginapi.Empty{}, stream) }()
			if mode != "initial send failure" {
				waitSignal(t, sent)
				switch mode {
				case "disconnect":
					cancel()
				case "local stop", "old generation":
					close(p.stop)
					if mode == "old generation" {
						p.Lock()
						p.stop = make(chan struct{})
						p.restartCh = make(chan struct{}, 1)
						p.Unlock()
					}
					cancel()
				}
			}
			waitSignal(t, done)
			if mode == "local stop" || mode == "old generation" {
				select {
				case <-p.restartCh:
					t.Fatal("intentional shutdown requested recovery")
				default:
				}
			} else {
				waitSignal(t, p.restartCh)
			}
		})
	}
}
