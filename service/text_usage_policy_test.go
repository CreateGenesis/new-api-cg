package service

import (
	"errors"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usagePolicyTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func usagePolicyRelayInfo(inputLimit, outputLimit int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 17,
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{
					InputTokens:  inputLimit,
					OutputTokens: outputLimit,
				},
			},
		},
	}
}

func TestApplyTextUsagePolicyDoesNotValidateWithoutUsageTokenLimit(t *testing.T) {
	for _, info := range []*relaycommon.RelayInfo{
		nil,
		{ChannelMeta: &relaycommon.ChannelMeta{}},
		usagePolicyRelayInfo(0, 0),
	} {
		modified, err := applyTextUsagePolicy(nil, info, nil, func() (int, error) {
			t.Fatal("random source must not be called")
			return 0, nil
		})

		require.NoError(t, err)
		assert.False(t, modified)
	}
}

func TestApplyTextUsagePolicyValidatesOnlyConfiguredDirections(t *testing.T) {
	tests := []struct {
		name  string
		info  *relaycommon.RelayInfo
		usage *dto.Usage
	}{
		{
			name:  "input limit does not require output usage",
			info:  usagePolicyRelayInfo(100, 0),
			usage: &dto.Usage{PromptTokens: 10, TotalTokens: 10},
		},
		{
			name:  "output limit does not require input usage",
			info:  usagePolicyRelayInfo(0, 100),
			usage: &dto.Usage{CompletionTokens: 10, TotalTokens: 10},
		},
		{
			name: "missing usage is not treated as explicit zero usage",
			info: usagePolicyRelayInfo(100, 100),
		},
		{
			name:  "estimated fallback is not treated as explicit zero usage",
			info:  usagePolicyRelayInfo(100, 100),
			usage: &dto.Usage{Estimated: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modified, err := applyTextUsagePolicy(nil, test.info, test.usage, func() (int, error) {
				t.Fatal("random source must not be called")
				return 0, nil
			})

			require.NoError(t, err)
			assert.False(t, modified)
		})
	}
}

func TestApplyTextUsagePolicyLeavesDisabledAndEqualLimitsUntouched(t *testing.T) {
	for _, info := range []*relaycommon.RelayInfo{
		nil,
		{ChannelMeta: &relaycommon.ChannelMeta{}},
		usagePolicyRelayInfo(100, 20),
	} {
		usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}
		modified, err := applyTextUsagePolicy(nil, info, usage, func() (int, error) {
			t.Fatal("random source must not be called")
			return 0, nil
		})

		require.NoError(t, err)
		assert.False(t, modified)
		assert.Equal(t, 100, usage.PromptTokens)
		assert.Equal(t, 20, usage.CompletionTokens)
	}
}

func TestApplyTextUsagePolicyUsesIndependentInclusiveRandomFactors(t *testing.T) {
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	factors := []int{usageTokenLimitMinBasisPoints, usageTokenLimitMaxBasisPoints}
	index := 0

	modified, err := applyTextUsagePolicy(usagePolicyTestContext(), usagePolicyRelayInfo(100, 40), usage, func() (int, error) {
		factor := factors[index]
		index++
		return factor, nil
	})

	require.NoError(t, err)
	assert.True(t, modified)
	assert.Equal(t, 2, index)
	assert.Equal(t, 30, usage.PromptTokens)
	assert.Equal(t, 38, usage.CompletionTokens)
	assert.Equal(t, 68, usage.TotalTokens)
}

func TestApplyTextUsagePolicyPropagatesRandomFailureWithoutMutation(t *testing.T) {
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 20, TotalTokens: 1020}
	wantErr := errors.New("entropy unavailable")

	modified, err := applyTextUsagePolicy(nil, usagePolicyRelayInfo(100, 0), usage, func() (int, error) {
		return 0, wantErr
	})

	assert.False(t, modified)
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1000, usage.PromptTokens)
	assert.Equal(t, 1020, usage.TotalTokens)
}

func TestApplyTextUsagePolicySynchronizesOpenAIUsageAndAudit(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		TotalTokens:      1200,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         200,
			CachedCreationTokens: 100,
			TextTokens:           800,
			AudioTokens:          100,
			ImageTokens:          100,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 50, TextTokens: 150},
	}
	usage.BillingUsage = dto.NewOpenAIChatBillingUsage(usage)
	info := usagePolicyRelayInfo(100, 50)

	modified, err := applyTextUsagePolicy(usagePolicyTestContext(), info, usage, func() (int, error) { return 5000, nil })

	require.NoError(t, err)
	assert.True(t, modified)
	assert.Equal(t, 50, usage.PromptTokens)
	assert.Equal(t, 25, usage.CompletionTokens)
	assert.Equal(t, 75, usage.TotalTokens)
	assert.Equal(t, 10, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 5, usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 40, usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 18, usage.CompletionTokenDetails.TextTokens)
	assert.Equal(t, 6, usage.CompletionTokenDetails.ReasoningTokens)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, usage.PromptTokens, usage.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, usage.CompletionTokens, usage.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Equal(t, usage.TotalTokens, usage.BillingUsage.OpenAIUsage.TotalTokens)
	require.NotNil(t, info.UsageTokenLimitAudit)
	assert.Equal(t, 5000, info.UsageTokenLimitAudit.Input.RandomBasis)
	assert.Equal(t, 5000, info.UsageTokenLimitAudit.Output.RandomBasis)
}

func TestApplyTextUsagePolicySynchronizesClaudeAndGeminiBillingUsage(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		usage := &dto.Usage{
			PromptTokens:     700,
			CompletionTokens: 200,
			UsageSemantic:    UsageSemanticAnthropic,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         200,
				CachedCreationTokens: 100,
			},
			ClaudeCacheCreation5mTokens: 40,
			ClaudeCacheCreation1hTokens: 60,
			BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
				InputTokens:                 700,
				CacheReadInputTokens:        200,
				CacheCreationInputTokens:    100,
				OutputTokens:                200,
				ClaudeCacheCreation5mTokens: 40,
				ClaudeCacheCreation1hTokens: 60,
			}),
		}

		modified, err := applyTextUsagePolicy(usagePolicyTestContext(), usagePolicyRelayInfo(100, 50), usage, func() (int, error) { return 5000, nil })

		require.NoError(t, err)
		assert.True(t, modified)
		assert.Equal(t, 35, usage.PromptTokens)
		assert.Equal(t, 10, usage.PromptTokensDetails.CachedTokens)
		assert.Equal(t, 5, usage.PromptTokensDetails.CachedCreationTokens)
		assert.Equal(t, 25, usage.CompletionTokens)
		require.NotNil(t, usage.BillingUsage.ClaudeUsage)
		assert.Equal(t, 35, usage.BillingUsage.ClaudeUsage.InputTokens)
		assert.Equal(t, 10, usage.BillingUsage.ClaudeUsage.CacheReadInputTokens)
		assert.Equal(t, 5, usage.BillingUsage.ClaudeUsage.CacheCreationInputTokens)
		assert.Equal(t, 25, usage.BillingUsage.ClaudeUsage.OutputTokens)
	})

	t.Run("gemini", func(t *testing.T) {
		metadata := &dto.GeminiUsageMetadata{
			PromptTokenCount:        800,
			ToolUsePromptTokenCount: 200,
			CandidatesTokenCount:    150,
			ThoughtsTokenCount:      50,
			TotalTokenCount:         1200,
			CachedContentTokenCount: 100,
			PromptTokensDetails:     []dto.GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 800}},
			CandidatesTokensDetails: []dto.GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 150}},
		}
		usage := &dto.Usage{
			PromptTokens:     1000,
			CompletionTokens: 200,
			TotalTokens:      1200,
			UsageSemantic:    UsageSemanticGemini,
			BillingUsage:     dto.NewGeminiChatBillingUsage(metadata),
		}

		modified, err := applyTextUsagePolicy(usagePolicyTestContext(), usagePolicyRelayInfo(100, 50), usage, func() (int, error) { return 5000, nil })

		require.NoError(t, err)
		assert.True(t, modified)
		require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
		final := usage.BillingUsage.GeminiUsageMetadata
		assert.Equal(t, 40, final.PromptTokenCount)
		assert.Equal(t, 10, final.ToolUsePromptTokenCount)
		assert.Equal(t, 18, final.CandidatesTokenCount)
		assert.Equal(t, 7, final.ThoughtsTokenCount)
		assert.Equal(t, 75, final.TotalTokenCount)
		assert.Equal(t, 5, final.CachedContentTokenCount)
		assert.Equal(t, 40, final.PromptTokensDetails[0].TokenCount)
		assert.Equal(t, 18, final.CandidatesTokensDetails[0].TokenCount)
	})
}

func TestLimitedUsageTokensHasMinimumOne(t *testing.T) {
	assert.Equal(t, 1, limitedUsageTokens(1, usageTokenLimitMinBasisPoints))
}
