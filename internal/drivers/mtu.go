package drivers

import "github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"

const defaultJumboFrameMTU = 8500

// jumboMTU returns the MTU to use when jumbo frame is enabled.
// It falls back to defaultJumboFrameMTU when eri.JumboFrameMTU is not set.
func jumboMTU(eri *types.ERI) int {
	if eri.JumboFrameMTU > 0 {
		return eri.JumboFrameMTU
	}
	return defaultJumboFrameMTU
}
