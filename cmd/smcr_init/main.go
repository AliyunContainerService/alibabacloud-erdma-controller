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
	// smc module is load
	_, err := os.Stat("/proc/sys/net/smc/tcp2smc")
	if err != nil {
		log.Fatal("error setting tcp2smcr", err)
	}

	err = os.WriteFile("/proc/sys/net/smc/tcp2smc", []byte("1"), 0644)
	if err != nil {
		log.Fatal("error setting tcp2smcr", err)
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
