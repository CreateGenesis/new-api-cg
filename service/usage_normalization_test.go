package service

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
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
		{
			name:  "cache token sum cannot overflow into uncached input",
			usage: dto.Usage{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: math.MaxInt, CachedCreationTokens: math.MaxInt}},
			want:  NormalizedInputTokens{TotalInputTokens: 100, CacheReadInputTokens: math.MaxInt, CacheCreationInputTokens: math.MaxInt},
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

func TestNormalizeUsageForBillingProducesProtocolParity(t *testing.T) {
	tests := []struct {
		name       string
		usage      *dto.Usage
		wantMode   string
		wantSource string
		wantStatus string
	}{
		{
			name: "OpenAI input includes cache read",
			usage: &dto.Usage{BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
				PromptTokens:     2604,
				CompletionTokens: 383,
				TotalTokens:      2987,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 2432,
				},
			})},
			wantMode:   UsageAccountingModeIncluded,
			wantSource: UsageNormalizationSourceTotalTokens,
			wantStatus: UsageNormalizationStatusMatched,
		},
		{
			name: "Claude input excludes cache read",
			usage: &dto.Usage{BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
				InputTokens:          172,
				CacheReadInputTokens: 2432,
				OutputTokens:         383,
			})},
			wantMode:   UsageAccountingModeSeparate,
			wantSource: UsageNormalizationSourceBillingUsage,
			wantStatus: UsageNormalizationStatusNotChecked,
		},
		{
			name: "Gemini prompt includes cached content",
			usage: &dto.Usage{BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
				PromptTokenCount:        2604,
				CandidatesTokenCount:    383,
				TotalTokenCount:         2987,
				CachedContentTokenCount: 2432,
			})},
			wantMode:   UsageAccountingModeIncluded,
			wantSource: UsageNormalizationSourceTotalTokens,
			wantStatus: UsageNormalizationStatusMatched,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := NormalizeUsageForBilling(test.usage)

			assert.Equal(t, 172, normalized.InputTokens.UncachedInputTokens)
			assert.Equal(t, 2432, normalized.InputTokens.CacheReadInputTokens)
			assert.Equal(t, 2604, normalized.InputTokens.TotalInputTokens)
			assert.Equal(t, 383, normalized.OutputTokens)
			assert.Equal(t, 2987, normalized.TotalTokens)
			assert.Equal(t, test.wantMode, normalized.Audit.Mode)
			assert.Equal(t, test.wantSource, normalized.Audit.Source)
			assert.Equal(t, test.wantStatus, normalized.Audit.Status)
		})
	}
}

func TestNormalizeUsageForBillingUsesReconciliationBeforeFallback(t *testing.T) {
	tests := []struct {
		name        string
		usage       dto.Usage
		wantMode    string
		wantSource  string
		wantStatus  string
		wantInput   int
		wantTotalIn int
	}{
		{
			name: "total equals included input plus output",
			usage: dto.Usage{
				PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 30},
			},
			wantMode: UsageAccountingModeIncluded, wantSource: UsageNormalizationSourceTotalTokens,
			wantStatus: UsageNormalizationStatusMatched, wantInput: 70, wantTotalIn: 100,
		},
		{
			name: "total equals separate input cache and output",
			usage: dto.Usage{
				PromptTokens: 70, CompletionTokens: 20, TotalTokens: 120,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 30},
			},
			wantMode: UsageAccountingModeSeparate, wantSource: UsageNormalizationSourceTotalTokens,
			wantStatus: UsageNormalizationStatusMatched, wantInput: 70, wantTotalIn: 100,
		},
		{
			name: "inconsistent total falls back to included input",
			usage: dto.Usage{
				PromptTokens: 100, CompletionTokens: 20, TotalTokens: 999,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 30},
			},
			wantMode: UsageAccountingModeIncluded, wantSource: UsageNormalizationSourceFallback,
			wantStatus: UsageNormalizationStatusMismatch, wantInput: 70, wantTotalIn: 100,
		},
		{
			name: "missing total falls back to included input",
			usage: dto.Usage{
				PromptTokens: 100, CompletionTokens: 20,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 30},
			},
			wantMode: UsageAccountingModeIncluded, wantSource: UsageNormalizationSourceFallback,
			wantStatus: UsageNormalizationStatusNotChecked, wantInput: 70, wantTotalIn: 100,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := NormalizeUsageForBilling(&test.usage)

			assert.Equal(t, test.wantMode, normalized.Audit.Mode)
			assert.Equal(t, test.wantSource, normalized.Audit.Source)
			assert.Equal(t, test.wantStatus, normalized.Audit.Status)
			assert.Equal(t, test.wantInput, normalized.InputTokens.UncachedInputTokens)
			assert.Equal(t, test.wantTotalIn, normalized.InputTokens.TotalInputTokens)
		})
	}
}

func TestNormalizeUsageForBillingTotalTokensOverrideContradictoryMetadata(t *testing.T) {
	tests := []struct {
		name      string
		usage     *dto.Usage
		wantMode  string
		wantInput int
	}{
		{
			name: "separate equation overrides OpenAI semantic",
			usage: &dto.Usage{
				PromptTokens:     70,
				CompletionTokens: 20,
				TotalTokens:      120,
				UsageSemantic:    UsageSemanticOpenAI,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 30,
				},
			},
			wantMode:  UsageAccountingModeSeparate,
			wantInput: 70,
		},
		{
			name: "separate equation overrides OpenAI billing usage",
			usage: &dto.Usage{BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
				PromptTokens:     70,
				CompletionTokens: 20,
				TotalTokens:      120,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 30,
				},
			})},
			wantMode:  UsageAccountingModeSeparate,
			wantInput: 70,
		},
		{
			name: "included equation overrides Anthropic source",
			usage: &dto.Usage{
				PromptTokens:     100,
				CompletionTokens: 20,
				TotalTokens:      120,
				UsageSource:      dto.BillingUsageSourceClaudeMessages,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens: 30,
				},
			},
			wantMode:  UsageAccountingModeIncluded,
			wantInput: 70,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := NormalizeUsageForBilling(test.usage)

			assert.Equal(t, test.wantMode, normalized.Audit.Mode)
			assert.Equal(t, UsageNormalizationSourceTotalTokens, normalized.Audit.Source)
			assert.Equal(t, UsageNormalizationStatusMatched, normalized.Audit.Status)
			assert.Equal(t, test.wantInput, normalized.InputTokens.UncachedInputTokens)
			assert.Equal(t, 100, normalized.InputTokens.TotalInputTokens)
		})
	}
}

func TestNormalizeUsageForBillingFallsBackToMetadataAndClampsInvalidCounts(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 20,
		TotalTokens:      999,
		UsageSource:      dto.BillingUsageSourceClaudeMessages,
		UsageSemantic:    UsageSemanticOpenAI,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         150,
			CachedCreationTokens: -10,
			CacheWriteTokens:     25,
		},
	}

	normalized := NormalizeUsageForBilling(usage)

	assert.Equal(t, UsageAccountingModeSeparate, normalized.Audit.Mode)
	assert.Equal(t, UsageNormalizationSourceUsageSource, normalized.Audit.Source)
	assert.Equal(t, UsageNormalizationStatusMismatch, normalized.Audit.Status)
	assert.Equal(t, 100, normalized.InputTokens.UncachedInputTokens)
	assert.Equal(t, 275, normalized.InputTokens.TotalInputTokens)
	assert.Equal(t, 150, normalized.InputTokens.CacheReadInputTokens)
	assert.Equal(t, 25, normalized.InputTokens.CacheCreationInputTokens)
}

func TestNormalizeUsageForBillingPrefersOriginalBillingUsageAcrossConversion(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     172,
		CompletionTokens: 383,
		TotalTokens:      2987,
		UsageSemantic:    UsageSemanticAnthropic,
		BillingUsage: dto.NewOpenAIResponsesBillingUsage(&dto.Usage{
			InputTokens:  2604,
			OutputTokens: 383,
			InputTokensDetails: &dto.InputTokenDetails{
				CachedTokens: 2432,
			},
		}),
	}

	normalized := NormalizeUsageForBilling(usage)

	assert.Equal(t, UsageAccountingModeIncluded, normalized.Audit.Mode)
	assert.Equal(t, UsageNormalizationSourceBillingUsage, normalized.Audit.Source)
	assert.Equal(t, 172, normalized.InputTokens.UncachedInputTokens)
	assert.Equal(t, 2604, normalized.InputTokens.TotalInputTokens)
}
