package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/agent"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/drivers"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const defaultHealthSocket = "/var/lib/erdma-agent/health.sock"

func main() {
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	var (
		preferDriver          string
		allocAllDevices       bool
		devicepluginPreStart  bool
		localERIDiscovery     bool
		exposedLocalERIs      string
		erdmaInstallerVersion string
		jumboFrameMTU         int
		probeSocket           string
		healthCheck           string
	)
	flag.StringVar(&preferDriver, "prefer-driver", "", "prefer driver")
	flag.BoolVar(&allocAllDevices, "allocate-all-devices", false,
		"allocate all erdma devices for resource request, true => alloc all, false => alloc devices based on numa")
	flag.BoolVar(&devicepluginPreStart, "deviceplugin-prestart-container", false,
		"use device plugin prestart container to config smc-r, enable it if not use webhook to inject initContainers")
	flag.BoolVar(&localERIDiscovery, "local-eri-discovery", false,
		"Only manager on-node eri resources without using OpenAPI and access key")
	flag.StringVar(&exposedLocalERIs, "exposed-local-eris", "",
		"allocate specific ERI from existing ERI to pods for each instance")
	flag.StringVar(&erdmaInstallerVersion, "erdma-installer-version", drivers.DefaultErdmaInstallerVersion,
		"erdma installer package version for missing drivers (does not upgrade installed drivers)")
	flag.IntVar(&jumboFrameMTU, "jumbo-frame-mtu", 8500,
		"MTU value to set on ERDMA network interfaces when jumbo frame is enabled")
	flag.StringVar(&probeSocket, "health-probe-socket", defaultHealthSocket, "The container-local health probe socket.")
	flag.StringVar(&healthCheck, "health-check", "", "Check healthz or readyz through the health probe socket and exit.")
	flag.Parse()
	if healthCheck != "" {
		if err := checkHealth(probeSocket, healthCheck); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	eriAgent, err := agent.NewAgent(
		preferDriver,
		allocAllDevices,
		devicepluginPreStart,
		localERIDiscovery,
		exposedLocalERIs,
		erdmaInstallerVersion,
		jumboFrameMTU,
	)
	if err != nil {
		panic(err)
	}
	err = runAgent(ctrl.SetupSignalHandler(), eriAgent, probeSocket)
	if err != nil {
		panic(err)
	}
}

func runAgent(parent context.Context, eriAgent *agent.Agent, probeSocket string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	listener, err := listenHealthSocket(probeSocket)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           eriAgent.HealthHandler(ctx),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
		cancel()
	}()
	err = eriAgent.Run(ctx)
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		err = errors.Join(err, shutdownErr)
	}
	return errors.Join(err, <-serverErr)
}

func listenHealthSocket(socket string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func checkHealth(socket, check string) error {
	if check != "healthz" && check != "readyz" {
		return fmt.Errorf("unknown health check %q", check)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get("http://localhost/" + check)
	if err != nil {
		return err
	}
	if err := response.Body.Close(); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", check, response.StatusCode)
	}
	return nil
}
