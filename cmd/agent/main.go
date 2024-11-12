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
		preferDriver    string
		allocAllDevices bool
	)
	flag.StringVar(&preferDriver, "prefer-driver", "", "prefer driver")
	flag.BoolVar(&allocAllDevices, "allocate-all-devices", false,
		"allocate all erdma devices for resource request, true => alloc all, false => alloc devices based on numa")
	flag.Parse()

	eriAgent, err := agent.NewAgent(preferDriver, allocAllDevices)
	if err != nil {
		panic(err)
	}
	err = eriAgent.Run()
	if err != nil {
		panic(err)
	}
}
