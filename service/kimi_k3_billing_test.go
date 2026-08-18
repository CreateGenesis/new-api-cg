package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kimiK3BillingRelayInfo(channelType int) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:       channelType,
		UpstreamModelName: "kimi-k3",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			KimiK3OfficialCompatibility: true,
		},
	}}
	info.ActivateKimiK3OfficialCompatibility()
	return info
}

func TestPrepareKimiK3OfficialBillingUsesOpenAIChatSemanticForAllChannels(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic, constant.ChannelTypeMoonshot} {
		usage := &dto.Usage{BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
			InputTokens: 70, CacheReadInputTokens: 20, CacheCreationInputTokens: 10, OutputTokens: 5,
		})}

		prepared := prepareKimiK3OfficialBilling(kimiK3BillingRelayInfo(channelType), usage)

		require.NotNil(t, prepared.BillingUsage)
		assert.Equal(t, dto.BillingUsageSourceOAIChat, prepared.BillingUsage.Source)
		assert.Equal(t, dto.BillingUsageSemanticOpenAI, prepared.BillingUsage.Semantic)
		require.NotNil(t, prepared.BillingUsage.OpenAIUsage)
		assert.Equal(t, 100, prepared.BillingUsage.OpenAIUsage.PromptTokens)
		assert.Equal(t, 5, prepared.BillingUsage.OpenAIUsage.CompletionTokens)
	}
}

func TestPrepareKimiK3OfficialBillingPreservesSignedAnthropicEquation(t *testing.T) {
	info := kimiK3BillingRelayInfo(constant.ChannelTypeAnthropic)
	usage := &dto.Usage{BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
		InputTokens: -2, CacheReadInputTokens: 93, OutputTokens: 7,
	})}

	prepared := prepareKimiK3OfficialBilling(info, usage)

	require.NotNil(t, prepared.BillingUsage.OpenAIUsage)
	assert.Equal(t, 91, prepared.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, 91, prepared.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, info.KimiK3BillingAudit)
	assert.Equal(t, -2, info.KimiK3BillingAudit.OriginalInputTokens)
	assert.Equal(t, 91, info.KimiK3BillingAudit.SignedTotalInput)
	assert.Contains(t, info.KimiK3BillingAudit.NegativeFields, "input_tokens")
}

func TestPrepareKimiK3OfficialBillingExcludesPseudoDisabledReasoningTokens(t *testing.T) {
	info := kimiK3BillingRelayInfo(constant.ChannelTypeOpenAI)
	info.KimiK3HideThinking = true
	usage := &dto.Usage{
		PromptTokens:     2,
		CompletionTokens: 63,
		TotalTokens:      65,
	}
	usage.CompletionTokenDetails.ReasoningTokens = 51
	usage.BillingUsage = dto.NewOpenAIChatBillingUsage(usage)

	prepared := prepareKimiK3OfficialBilling(info, usage)

	assert.Equal(t, 12, prepared.CompletionTokens)
	assert.Equal(t, 14, prepared.TotalTokens)
	assert.Zero(t, prepared.CompletionTokenDetails.ReasoningTokens)
	require.NotNil(t, prepared.BillingUsage)
	require.NotNil(t, prepared.BillingUsage.OpenAIUsage)
	assert.Equal(t, 12, prepared.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Equal(t, 14, prepared.BillingUsage.OpenAIUsage.TotalTokens)
	assert.Zero(t, prepared.BillingUsage.OpenAIUsage.CompletionTokenDetails.ReasoningTokens)
	assert.Equal(t, 12, NormalizeUsageForBilling(prepared).OutputTokens)

	assert.Equal(t, 63, usage.CompletionTokens)
	assert.Equal(t, 51, usage.CompletionTokenDetails.ReasoningTokens)
}
