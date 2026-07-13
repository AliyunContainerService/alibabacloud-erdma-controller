package main

import (
	"log"
	"os"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/consts"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/drivers"
)

func main() {
	pnetid := os.Getenv(consts.SMCRPNETEnv)
	if pnetid == "" {
		log.Fatal("smcr pnetid is empty")
	}
	// 1. config sysctls
	// tcp2smc transparently redirects eligible TCP traffic to SMC-R. It is an
	// Alibaba Cloud Linux kernel knob that has been removed on newer kernels
	// (e.g. Alibaba Cloud Linux 4). Its absence means SMC-R is not supported on
	// this kernel, so fail with a clear message rather than a cryptic error.
	if _, err := os.Stat("/proc/sys/net/smc/tcp2smc"); err != nil {
		log.Fatalf("SMC-R is not supported on this kernel: /proc/sys/net/smc/tcp2smc is unavailable "+
			"(e.g. removed on Alibaba Cloud Linux 4): %v", err)
	}
	err := os.WriteFile("/proc/sys/net/smc/tcp2smc", []byte("1"), 0644)
	if err != nil {
		log.Fatal("error setting tcp2smc", err)
	}
	err = os.WriteFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", []byte("1"), 0644)
	if err != nil {
		log.Fatal("error setting disable_ipv6", err)
	}
	// 2. config smcr pnet
	err = drivers.ConfigForNetDevice(pnetid, "eth0")
	if err != nil {
		log.Fatal("error config smcr pnet", err)
	}
}
