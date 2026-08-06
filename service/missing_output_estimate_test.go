package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyMissingOutputEstimateUpdatesProtocolBillingUsage(t *testing.T) {
	tests := []struct {
		name        string
		billing     *dto.BillingUsage
		assertUsage func(*testing.T, *dto.BillingUsage)
	}{
		{
			name:    "OpenAI",
			billing: dto.NewOpenAIChatBillingUsage(&dto.Usage{PromptTokens: 10, TotalTokens: 10}),
			assertUsage: func(t *testing.T, billing *dto.BillingUsage) {
				require.NotNil(t, billing.OpenAIUsage)
				assert.Equal(t, 7, billing.OpenAIUsage.CompletionTokens)
				assert.Equal(t, 7, billing.OpenAIUsage.OutputTokens)
				assert.Equal(t, 17, billing.OpenAIUsage.TotalTokens)
				assert.True(t, billing.OpenAIUsage.Estimated)
			},
		},
		{
			name:    "Claude",
			billing: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{InputTokens: 10}),
			assertUsage: func(t *testing.T, billing *dto.BillingUsage) {
				require.NotNil(t, billing.ClaudeUsage)
				assert.Equal(t, 7, billing.ClaudeUsage.OutputTokens)
			},
		},
		{
			name:    "Gemini",
			billing: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{PromptTokenCount: 10, TotalTokenCount: 10}),
			assertUsage: func(t *testing.T, billing *dto.BillingUsage) {
				require.NotNil(t, billing.GeminiUsageMetadata)
				assert.Equal(t, 7, billing.GeminiUsageMetadata.CandidatesTokenCount)
				assert.Equal(t, 17, billing.GeminiUsageMetadata.TotalTokenCount)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage := &dto.Usage{PromptTokens: 10, TotalTokens: 10, BillingUsage: test.billing}

			assert.True(t, ApplyMissingOutputEstimate(usage, 7))
			assert.Equal(t, 7, usage.CompletionTokens)
			assert.Equal(t, 7, usage.OutputTokens)
			assert.Equal(t, 17, usage.TotalTokens)
			assert.True(t, usage.Estimated)
			require.NotNil(t, usage.BillingUsage)
			assert.True(t, usage.BillingUsage.Estimated)
			test.assertUsage(t, usage.BillingUsage)
		})
	}
}

func TestApplyMissingOutputEstimatePreservesReportedOutput(t *testing.T) {
	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}

	assert.False(t, ApplyMissingOutputEstimate(usage, 7))
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 13, usage.TotalTokens)
}
