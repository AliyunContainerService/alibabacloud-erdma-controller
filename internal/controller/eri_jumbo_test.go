// Copyright 2024 Alibaba Cloud. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJumboFrameFromAttr(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		input    *bool
		expected bool
	}{
		{
			name:     "nil pointer returns false",
			input:    nil,
			expected: false,
		},
		{
			name:     "true pointer returns true",
			input:    &trueVal,
			expected: true,
		},
		{
			name:     "false pointer returns false",
			input:    &falseVal,
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, jumboFrameFromAttr(tt.input))
		})
	}
}
