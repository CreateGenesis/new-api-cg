package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
)

func TestApplyUsagePostProcessingNormalizesGenericOpenAIUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens: 1026, CompletionTokens: 220,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 1026},
	}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI}}

	applyUsagePostProcessing(info, usage, nil)

	assert.Equal(t, service.UsageSemanticOpenAI, usage.UsageSemantic)
	assert.Equal(t, 1026, usage.PromptTokens)
	assert.Equal(t, 1026, usage.InputTokens)
	assert.Equal(t, 220, usage.CompletionTokens)
	assert.Equal(t, 220, usage.OutputTokens)
	assert.Equal(t, 1246, usage.TotalTokens)
	assert.Equal(t, 1026, usage.PromptTokensDetails.CachedTokens)
}
