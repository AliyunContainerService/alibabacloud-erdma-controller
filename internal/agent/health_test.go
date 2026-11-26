package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/AliyunContainerService/alibabacloud-erdma-controller/api/v1"
)

type waitingKubernetes struct {
	started chan struct{}
}

func (k *waitingKubernetes) WaitEriInfo(ctx context.Context) (*v1.ERdmaDevice, error) {
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
	go func() { done <- a.Run(ctx) }()
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
	response = httptest.NewRecorder()
	a.HealthHandler(ctx).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("terminating agent returned readiness %d", response.Code)
	}
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not exit while waiting for devices")
	}
}
