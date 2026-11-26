package main

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "health.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	if err := checkHealth(socket, "healthz"); err != nil {
		t.Fatalf("live agent: %v", err)
	}
	if err := checkHealth(socket, "readyz"); err == nil {
		t.Fatal("unready agent passed the readiness check")
	}
	if err := checkHealth(socket, "unknown"); err == nil {
		t.Fatal("unknown check succeeded")
	}
	if err := checkHealth(filepath.Join(t.TempDir(), "absent.sock"), "healthz"); err == nil {
		t.Fatal("missing socket passed the health check")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkHealth(socket, "healthz"); err == nil {
		t.Fatal("stopped server passed the health check")
	}
}
