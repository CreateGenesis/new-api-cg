package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestApplyUsagePostProcessingKeepsOpenAICacheIncludedForBilling(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens: 87, CompletionTokens: 16, TotalTokens: 190,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 87},
	}
	usage.BillingUsage = dto.NewOpenAIChatBillingUsage(usage)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI}}

	rewritten := applyUsagePostProcessing(info, usage, nil)

	assert.True(t, rewritten)
	assert.Equal(t, service.UsageSemanticOpenAI, usage.UsageSemantic)
	assert.Equal(t, 87, usage.PromptTokens)
	assert.Equal(t, 103, usage.TotalTokens)
	require.NotNil(t, usage.BillingUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIChat, usage.BillingUsage.Source)
	assert.Equal(t, service.UsageSemanticOpenAI, usage.BillingUsage.Semantic)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 87, usage.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, 103, usage.BillingUsage.OpenAIUsage.TotalTokens)

	normalized := service.NormalizeUsageForBilling(usage)
	assert.Zero(t, normalized.InputTokens.UncachedInputTokens)
	assert.Equal(t, 87, normalized.InputTokens.CacheReadInputTokens)
	assert.Equal(t, 16, normalized.OutputTokens)
}

func TestSendStreamDataRewritesContradictoryOpenAIUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI}}
	data := `{"id":"chunk_1","choices":[],"usage":{"prompt_tokens":87,"completion_tokens":16,"total_tokens":190,"prompt_tokens_details":{"cached_tokens":87},"billing_usage":{"source":"oai_chat","semantic":"openai","openai_usage":{"prompt_tokens":87,"completion_tokens":16,"total_tokens":190,"prompt_tokens_details":{"cached_tokens":87}}}}}`

	err := sendStreamData(ctx, info, data, false, false)

	require.NoError(t, err)
	responseData := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(recorder.Body.String()), "data: "))
	var envelope struct {
		Usage *dto.Usage `json:"usage"`
	}
	require.NoError(t, common.UnmarshalJsonStr(responseData, &envelope))
	require.NotNil(t, envelope.Usage)
	assert.Equal(t, 87, envelope.Usage.PromptTokens)
	assert.Equal(t, 103, envelope.Usage.TotalTokens)
	require.NotNil(t, envelope.Usage.BillingUsage)
	require.NotNil(t, envelope.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 103, envelope.Usage.BillingUsage.OpenAIUsage.TotalTokens)
}

func TestOpenaiHandlerRewritesContradictoryOpenAIUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "openai-test",
		},
	}
	body := `{"id":"chat_1","object":"chat.completion","created":1,"model":"openai-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":87,"completion_tokens":16,"total_tokens":190,"prompt_tokens_details":{"cached_tokens":87},"billing_usage":{"source":"oai_chat","semantic":"openai","openai_usage":{"prompt_tokens":87,"completion_tokens":16,"total_tokens":190,"prompt_tokens_details":{"cached_tokens":87}}}}}`
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	usage, relayErr := OpenaiHandler(ctx, info, response)

	require.Nil(t, relayErr)
	require.NotNil(t, usage)
	assert.Equal(t, 103, usage.TotalTokens)
	var forwarded dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &forwarded))
	assert.Equal(t, 87, forwarded.Usage.PromptTokens)
	assert.Equal(t, 103, forwarded.Usage.TotalTokens)
	require.NotNil(t, forwarded.Usage.BillingUsage)
	require.NotNil(t, forwarded.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 103, forwarded.Usage.BillingUsage.OpenAIUsage.TotalTokens)
}
