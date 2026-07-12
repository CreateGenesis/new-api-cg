package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeInputTokens(t *testing.T) {
	tests := []struct {
		name  string
		usage dto.Usage
		want  NormalizedInputTokens
	}{
		{
			name:  "OpenAI no cache",
			usage: dto.Usage{PromptTokens: 1026},
			want:  NormalizedInputTokens{TotalInputTokens: 1026, UncachedInputTokens: 1026},
		},
		{
			name:  "OpenAI partial cache",
			usage: dto.Usage{PromptTokens: 1026, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 500}},
			want:  NormalizedInputTokens{TotalInputTokens: 1026, UncachedInputTokens: 526, CacheReadInputTokens: 500},
		},
		{
			name:  "OpenAI full cache",
			usage: dto.Usage{PromptTokens: 1026, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 1026}},
			want:  NormalizedInputTokens{TotalInputTokens: 1026, CacheReadInputTokens: 1026},
		},
		{
			name: "Anthropic cache creation aggregate wins",
			usage: dto.Usage{
				PromptTokens: 100, UsageSemantic: UsageSemanticAnthropic,
				PromptTokensDetails:         dto.InputTokenDetails{CachedTokens: 30, CachedCreationTokens: 50},
				ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 20,
			},
			want: NormalizedInputTokens{
				TotalInputTokens: 180, UncachedInputTokens: 100, CacheReadInputTokens: 30,
				CacheCreationInputTokens: 50, CacheCreation5mInputTokens: 10, CacheCreation1hInputTokens: 20,
			},
		},
		{
			name: "Anthropic cache creation split wins",
			usage: dto.Usage{
				PromptTokens: 100, UsageSemantic: UsageSemanticAnthropic,
				PromptTokensDetails:         dto.InputTokenDetails{CachedCreationTokens: 20},
				ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 20,
			},
			want: NormalizedInputTokens{
				TotalInputTokens: 130, UncachedInputTokens: 100, CacheCreationInputTokens: 30,
				CacheCreation5mInputTokens: 10, CacheCreation1hInputTokens: 20,
			},
		},
		{
			name:  "cache larger than OpenAI total clamps uncached",
			usage: dto.Usage{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 150, CachedCreationTokens: 25}},
			want:  NormalizedInputTokens{TotalInputTokens: 100, CacheReadInputTokens: 150, CacheCreationInputTokens: 25},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, NormalizeInputTokens(&test.usage))
		})
	}
}

func TestNormalizeUsageForSemanticProductionRoundTrip(t *testing.T) {
	anthropic := dto.Usage{
		PromptTokens: 0, CompletionTokens: 220, UsageSemantic: UsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 1026},
	}

	openAI := NormalizeUsageForSemantic(&anthropic, UsageSemanticOpenAI)
	require.Equal(t, 1026, openAI.PromptTokens)
	assert.Equal(t, 1026, openAI.InputTokens)
	assert.Equal(t, 220, openAI.CompletionTokens)
	assert.Equal(t, 220, openAI.OutputTokens)
	assert.Equal(t, 1246, openAI.TotalTokens)
	assert.Equal(t, 1026, openAI.PromptTokensDetails.CachedTokens)

	roundTrip := NormalizeUsageForSemantic(&openAI, UsageSemanticAnthropic)
	assert.Equal(t, 0, roundTrip.PromptTokens)
	assert.Equal(t, 0, roundTrip.InputTokens)
	assert.Equal(t, 220, roundTrip.CompletionTokens)
	assert.Equal(t, 220, roundTrip.OutputTokens)
	assert.Equal(t, 1246, roundTrip.TotalTokens)
	assert.Equal(t, 1026, roundTrip.PromptTokensDetails.CachedTokens)
}

func TestBuildClaudeUsageFromOpenAIUsageSubtractsCachedInput(t *testing.T) {
	openAI := dto.Usage{
		PromptTokens: 1026, CompletionTokens: 220, UsageSemantic: UsageSemanticOpenAI,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 1026},
	}

	claude := buildClaudeUsageFromOpenAIUsage(&openAI)
	require.NotNil(t, claude)
	assert.Equal(t, 0, claude.InputTokens)
	assert.Equal(t, 1026, claude.CacheReadInputTokens)
	assert.Equal(t, 0, claude.CacheCreationInputTokens)
	assert.Equal(t, 220, claude.OutputTokens)

	roundTrip := dto.Usage{
		PromptTokens: claude.InputTokens, CompletionTokens: claude.OutputTokens,
		UsageSemantic: UsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         claude.CacheReadInputTokens,
			CachedCreationTokens: claude.CacheCreationInputTokens,
		},
	}
	normalized := NormalizeUsageForSemantic(&roundTrip, UsageSemanticOpenAI)
	assert.Equal(t, 1026, normalized.PromptTokens)
	assert.Equal(t, 1246, normalized.TotalTokens)
}
