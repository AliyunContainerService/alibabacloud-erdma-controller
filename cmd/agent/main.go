package main

import (
	"flag"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/agent"
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
	flag.StringVar(&erdmaInstallerVersion, "erdma-installer-version", "1.5.4",
		"erdma installer version")
	flag.IntVar(&jumboFrameMTU, "jumbo-frame-mtu", 8500,
		"MTU value to set on ERDMA network interfaces when jumbo frame is enabled")
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
	err = eriAgent.Run()
	if err != nil {
		panic(err)
	}
}
