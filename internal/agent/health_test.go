package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
)

func TestHealthWhileWaitingForDevices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Agent{}
	handler := a.HealthHandler(ctx)
	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		want := http.StatusOK
		if path == "/readyz" {
			want = http.StatusServiceUnavailable
		}
		if response.Code != want {
			t.Fatalf("%s returned %d, want %d", path, response.Code, want)
		}
	}
	cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("terminating agent returned readiness %d", response.Code)
	}
}

type waitingKubernetes struct {
	started chan struct{}
}

func (k *waitingKubernetes) WaitEriInfo() (*v1.ERdmaDevice, error) {
	return k.WaitEriInfoContext(context.Background())
}

func (k *waitingKubernetes) WaitEriInfoContext(ctx context.Context) (*v1.ERdmaDevice, error) {
	close(k.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestShutdownWhileWaitingForDevices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := &waitingKubernetes{started: make(chan struct{})}
	a := &Agent{kubernetes: k}
	done := make(chan error, 1)
	go func() { done <- a.RunContext(ctx) }()
	select {
	case <-k.started:
	case <-time.After(time.Second):
		t.Fatal("agent did not start device discovery")
	}
	response := httptest.NewRecorder()
	a.HealthHandler(ctx).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("idle agent is not ready to accept configuration: %d", response.Code)
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not exit while waiting for devices")
	}
}
