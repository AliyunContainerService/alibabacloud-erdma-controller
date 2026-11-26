package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "agent", "health.sock")
	listener, err := listenHealthSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(socket): 0700, socket: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("unexpected permissions for %s: %v, %v", path, info, err)
		}
	}
	if other, err := listenHealthSocket(socket); err == nil {
		_ = other.Close()
		t.Fatal("occupied socket was replaced")
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
	if err := checkHealth(filepath.Join(t.TempDir(), "absent.sock"), "healthz"); err == nil {
		t.Fatal("missing socket passed the health check")
	}
}
