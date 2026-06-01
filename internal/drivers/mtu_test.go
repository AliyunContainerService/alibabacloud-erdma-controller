package drivers

import (
	"testing"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/stretchr/testify/assert"
)

func TestJumboMTU(t *testing.T) {
	tests := []struct {
		name     string
		eri      *types.ERI
		expected int
	}{
		{
			name:     "zero JumboFrameMTU falls back to default 8500",
			eri:      &types.ERI{JumboFrame: true, JumboFrameMTU: 0},
			expected: 8500,
		},
		{
			name:     "custom JumboFrameMTU is used as-is",
			eri:      &types.ERI{JumboFrame: true, JumboFrameMTU: 9000},
			expected: 9000,
		},
		{
			name:     "JumboFrameMTU equal to default is used as-is",
			eri:      &types.ERI{JumboFrame: true, JumboFrameMTU: 8500},
			expected: 8500,
		},
		{
			name:     "negative JumboFrameMTU falls back to default",
			eri:      &types.ERI{JumboFrame: true, JumboFrameMTU: -1},
			expected: 8500,
		},
		{
			name:     "JumboFrame disabled still returns correct MTU value",
			eri:      &types.ERI{JumboFrame: false, JumboFrameMTU: 9000},
			expected: 9000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, jumboMTU(tt.eri))
		})
	}
}

func TestDefaultJumboFrameMTU(t *testing.T) {
	assert.Equal(t, 8500, defaultJumboFrameMTU)
}
