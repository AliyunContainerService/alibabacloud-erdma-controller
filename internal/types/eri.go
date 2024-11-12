package types

import "strings"

type ERI struct {
	ID           string
	IsPrimaryENI bool
	MAC          string
	InstanceID   string
	CardIndex    int
	QueuePair    int
}

type ERdmaCAP uint32

const (
	// nolint
	ERDMA_CAP_RDMA_CM ERdmaCAP = 1 << iota
	ERDMA_CAP_SMC_R
	ERDMA_CAP_VERBS
	ERDMA_CAP_GDR
	ERDMA_CAP_OOB
)

func (cap *ERdmaCAP) String() string {
	var capSlice []string
	if *cap&ERDMA_CAP_RDMA_CM != 0 {
		capSlice = append(capSlice, "RDMA_CM")
	}
	if *cap&ERDMA_CAP_SMC_R != 0 {
		capSlice = append(capSlice, "SMC_R")
	}
	if *cap&ERDMA_CAP_VERBS != 0 {
		capSlice = append(capSlice, "VERBS")
	}
	if *cap&ERDMA_CAP_GDR != 0 {
		capSlice = append(capSlice, "GDR")
	}
	if *cap&ERDMA_CAP_OOB != 0 {
		capSlice = append(capSlice, "OOB")
	}
	return strings.Join(capSlice, ",")
}

type ERdmaDeviceInfo struct {
	Name         string
	MAC          string
	DevPaths     []string
	NUMA         int64
	Capabilities ERdmaCAP
}

const ResourceName = "aliyun/erdma"

const (
	ENIStatusInUse     string = "InUse"
	ENIStatusAvailable string = "Available"
)
