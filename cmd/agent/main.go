package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"time"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/agent"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/drivers"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

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
		probeAddr             string
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
	flag.StringVar(&probeAddr, "health-probe-bind-address", "127.0.0.1:18081",
		"The address the health probe endpoint binds to.")
	flag.Parse()

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
	err = runAgent(ctrl.SetupSignalHandler(), eriAgent, probeAddr)
	if err != nil {
		panic(err)
	}
}

func runAgent(parent context.Context, eriAgent *agent.Agent, probeAddr string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	listener, err := net.Listen("tcp", probeAddr)
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
	err = eriAgent.RunContext(ctx)
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
