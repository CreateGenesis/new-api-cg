package claude

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeInputIncludesCache(t *testing.T) {
	tests := []struct {
		name          string
		usage         dto.ClaudeUsage
		wantInput     int
		wantCacheRead int
		wantCacheNew  int
	}{
		{name: "cache read only", usage: dto.ClaudeUsage{InputTokens: 17748, CacheReadInputTokens: 17664}, wantInput: 84, wantCacheRead: 17664},
		{name: "cache creation only", usage: dto.ClaudeUsage{InputTokens: 500, CacheCreationInputTokens: 120}, wantInput: 380, wantCacheNew: 120},
		{name: "cache read and creation", usage: dto.ClaudeUsage{InputTokens: 1000, CacheReadInputTokens: 600, CacheCreationInputTokens: 250}, wantInput: 150, wantCacheRead: 600, wantCacheNew: 250},
		{name: "cache larger than input clamps", usage: dto.ClaudeUsage{InputTokens: 100, CacheReadInputTokens: 150, CacheCreationInputTokens: 25}, wantCacheRead: 150, wantCacheNew: 25},
		{name: "negative counters clamp", usage: dto.ClaudeUsage{InputTokens: -100, CacheReadInputTokens: -20, CacheCreationInputTokens: -30}},
		{
			name: "cache creation split used once",
			usage: dto.ClaudeUsage{InputTokens: 500, CacheCreation: &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 80, Ephemeral1hInputTokens: 20,
			}},
			wantInput: 400, wantCacheNew: 100,
		},
		{
			name: "explicit creation total wins over split",
			usage: dto.ClaudeUsage{InputTokens: 500, CacheCreationInputTokens: 90, CacheCreation: &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 80, Ephemeral1hInputTokens: 20,
			}},
			wantInput: 410, wantCacheNew: 90,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := NormalizeInputIncludesCache(&tt.usage)
			assert.True(t, changed)
			assert.Equal(t, tt.wantInput, tt.usage.InputTokens)
			assert.Equal(t, tt.wantCacheRead, tt.usage.CacheReadInputTokens)
			assert.Equal(t, tt.wantCacheNew, tt.usage.CacheCreationInputTokens)
			if dto.HasClaudeUsageTokens(&tt.usage) {
				require.NotNil(t, tt.usage.BillingUsage)
				assert.True(t, tt.usage.BillingUsage.HasNormalizedAnthropicInputCache())
			}
		})
	}
}

func TestNormalizeInputIncludesCacheSaturatesCacheCreationSplit(t *testing.T) {
	usage := &dto.ClaudeUsage{
		InputTokens: math.MaxInt,
		CacheCreation: &dto.ClaudeCacheCreationUsage{
			Ephemeral5mInputTokens: math.MaxInt, Ephemeral1hInputTokens: math.MaxInt,
		},
	}

	require.True(t, NormalizeInputIncludesCache(usage))
	assert.Zero(t, usage.InputTokens)
	assert.Equal(t, math.MaxInt, usage.CacheCreationInputTokens)
}

func TestNormalizeInputIncludesCacheIsIdempotent(t *testing.T) {
	usage := &dto.ClaudeUsage{InputTokens: 17748, CacheReadInputTokens: 17664, OutputTokens: 12}
	require.True(t, NormalizeInputIncludesCache(usage))
	first := *usage
	firstBilling := dto.CloneBillingUsage(usage.BillingUsage)

	assert.False(t, NormalizeInputIncludesCache(usage))
	assert.Equal(t, first.InputTokens, usage.InputTokens)
	assert.Equal(t, first.CacheReadInputTokens, usage.CacheReadInputTokens)
	assert.Equal(t, firstBilling, usage.BillingUsage)
}

func TestNormalizeInputIncludesCacheRequiresRecognizedMarker(t *testing.T) {
	usage := &dto.ClaudeUsage{
		InputTokens: 200, CacheReadInputTokens: 50,
		BillingUsage: &dto.BillingUsage{Semantic: dto.BillingUsageSemanticOpenAI, AnthropicInputCacheNormalized: true},
	}

	require.True(t, NormalizeInputIncludesCache(usage))
	assert.Equal(t, 150, usage.InputTokens)
	assert.True(t, usage.BillingUsage.HasNormalizedAnthropicInputCache())
}

func TestNormalizeInputIncludesCachePreservesOriginalOpenAIBillingUsage(t *testing.T) {
	openAIUsage := &dto.Usage{PromptTokens: 17748, CompletionTokens: 12, TotalTokens: 17760}
	usage := &dto.ClaudeUsage{
		InputTokens: 17748, CacheReadInputTokens: 17664,
		BillingUsage: dto.NewOpenAIChatBillingUsage(openAIUsage),
	}

	require.True(t, NormalizeInputIncludesCache(usage))
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 17748, usage.BillingUsage.OpenAIUsage.PromptTokens)
	assert.True(t, usage.BillingUsage.AnthropicInputCacheNormalized)
}
