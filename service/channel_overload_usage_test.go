package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
)

func TestReliableUsageTotalUsesActualFieldsInPriorityOrder(t *testing.T) {
	tests := []struct {
		name  string
		usage *dto.Usage
		want  int64
	}{
		{name: "nil", usage: nil, want: 0},
		{name: "estimated", usage: &dto.Usage{TotalTokens: 50, Estimated: true}, want: 0},
		{name: "total tokens", usage: &dto.Usage{TotalTokens: 10, InputTokens: 50, OutputTokens: 60}, want: 10},
		{name: "input output", usage: &dto.Usage{InputTokens: 4, OutputTokens: 6, PromptTokens: 20, CompletionTokens: 30}, want: 10},
		{name: "prompt completion", usage: &dto.Usage{PromptTokens: 3, CompletionTokens: 2}, want: 5},
		{name: "negative fields cannot reduce total", usage: &dto.Usage{InputTokens: -5, OutputTokens: 6}, want: 6},
		{name: "missing usage", usage: &dto.Usage{}, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, reliableUsageTotal(test.usage))
		})
	}
}

func TestRealtimeUsageTotalUsesReportedTotalThenComponents(t *testing.T) {
	assert.Equal(t, int64(12), normalizedRealtimeUsageTotal(&dto.RealtimeUsage{TotalTokens: 12, InputTokens: 20, OutputTokens: 30}))
	assert.Equal(t, int64(9), normalizedRealtimeUsageTotal(&dto.RealtimeUsage{InputTokens: 4, OutputTokens: 5}))
	assert.Zero(t, normalizedRealtimeUsageTotal(&dto.RealtimeUsage{InputTokens: -1, OutputTokens: -2}))
}
