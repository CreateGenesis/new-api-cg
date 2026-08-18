package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failAfterWriteResponseWriter struct {
	gin.ResponseWriter
	successfulWrites int
	writes           int
	err              error
}

type closeAwareStreamBody struct {
	first  []byte
	served bool
	closed chan struct{}
	once   sync.Once
}

func (b *closeAwareStreamBody) Read(data []byte) (int, error) {
	if !b.served {
		b.served = true
		return copy(data, b.first), nil
	}
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *closeAwareStreamBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func (w *failAfterWriteResponseWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes > w.successfulWrites {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func TestChannelOutputRecorderDoesNotRetryEmptyStreamWithoutInputUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "gpt-test",
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(": ping\n\ndata: {\"choices\":[],\"usage\":null}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	assert.Empty(t, response.Body.String())

	require.Nil(t, recorder.finish(ctx, info, &dto.Usage{}))
	assert.Contains(t, response.Body.String(), "[DONE]")
}

func TestUsageEstimationRepairsG16AnthropicUsageWithoutChangingCacheTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		Request:     &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hello"}}},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
			AnthropicInputIncludesCache: true,
			UsageEstimation: &dto.UsageEstimationSettings{
				Enabled: true, ModelFamily: dto.UsageEstimationModelFamilyGLM, OutputMultiplier: 2,
			},
		}},
	}
	recorder := newChannelOutputRecorder(c.Writer, info, false, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder
	body := `{"type":"message","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":84,"cache_read_input_tokens":17664,"cache_creation_input_tokens":0,"output_tokens":0}}`
	_, err := recorder.WriteString(body)
	require.NoError(t, err)
	usage := &dto.Usage{
		PromptTokens: 84, InputTokens: 84, UsageSemantic: service.UsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 17664},
		BillingUsage:        dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{InputTokens: 84, CacheReadInputTokens: 17664}),
	}

	require.Nil(t, recorder.finish(c, info, usage))
	normalized := service.NormalizeUsageForBilling(usage)
	assert.Equal(t, 17748, normalized.InputTokens.TotalInputTokens)
	assert.Greater(t, normalized.OutputTokens, 0)
	assert.Contains(t, response.Body.String(), `"input_tokens":84`)
	assert.Contains(t, response.Body.String(), `"cache_read_input_tokens":17664`)
	assert.NotContains(t, response.Body.String(), `"output_tokens":0`)
	assert.NotNil(t, info.UsageEstimationAudit)
}

func TestUsageEstimationRepairsTNTUsageWithG16AndTNTEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		Request:                 &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hello"}}},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
			AnthropicInputIncludesCache: true,
			TNTTencentOpenAIConversion:  true,
			UsageEstimation: &dto.UsageEstimationSettings{
				Enabled: true, ModelFamily: dto.UsageEstimationModelFamilyKimi, OutputMultiplier: 1.5,
			},
		}},
	}
	recorder := newChannelOutputRecorder(c.Writer, info, false, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder
	_, err := recorder.WriteString(`{"type":"message","content":[{"type":"text","text":"TNT ok"}],"usage":{"input_tokens":84,"cache_read_input_tokens":17664,"output_tokens":0}}`)
	require.NoError(t, err)
	openAIBilling := dto.NewOpenAIChatBillingUsage(&dto.Usage{PromptTokens: 17748, InputTokens: 17748, TotalTokens: 17748})
	usage := &dto.Usage{
		PromptTokens: 84, InputTokens: 84, UsageSemantic: service.UsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 17664},
		BillingUsage:        openAIBilling,
	}

	require.Nil(t, recorder.finish(c, info, usage))
	normalized := service.NormalizeUsageForBilling(usage)
	assert.Equal(t, 17748, normalized.InputTokens.TotalInputTokens)
	assert.Greater(t, normalized.OutputTokens, 0)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Greater(t, usage.BillingUsage.OpenAIUsage.CompletionTokens, 0)
	assert.Contains(t, response.Body.String(), `"input_tokens":84`)
	assert.Contains(t, response.Body.String(), `"cache_read_input_tokens":17664`)
	assert.NotNil(t, info.UsageEstimationAudit)
}

func TestUsageEstimationRepairsStreamingTNTG16Usage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		IsStream:                true,
		ShouldIncludeUsage:      true,
		Request:                 &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hello"}}},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
			AnthropicInputIncludesCache: true,
			TNTTencentOpenAIConversion:  true,
			UsageEstimation: &dto.UsageEstimationSettings{
				Enabled: true, ModelFamily: dto.UsageEstimationModelFamilyKimi,
			},
		}},
	}
	recorder := newChannelOutputRecorder(c.Writer, info, false, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder

	_, err := recorder.WriteString("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":84,\"cache_read_input_tokens\":17664}}}\n\n")
	require.NoError(t, err)
	_, err = recorder.WriteString("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"TNT stream\"}}\n\n")
	require.NoError(t, err)
	_, err = recorder.WriteString("data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":84,\"cache_read_input_tokens\":17664,\"output_tokens\":0}}\n\n")
	require.NoError(t, err)
	_, err = recorder.WriteString("data: [DONE]\n\n")
	require.NoError(t, err)

	usage := &dto.Usage{
		PromptTokens: 84, InputTokens: 84, UsageSemantic: service.UsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 17664},
		BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
			InputTokens: 84, CacheReadInputTokens: 17664,
		}),
	}
	require.Nil(t, recorder.finish(c, info, usage))
	normalized := service.NormalizeUsageForBilling(usage)
	assert.Equal(t, 17748, normalized.InputTokens.TotalInputTokens)
	assert.Greater(t, normalized.OutputTokens, 0)
	assert.Contains(t, response.Body.String(), `"input_tokens":84`)
	assert.Contains(t, response.Body.String(), `"cache_read_input_tokens":17664`)
	assert.NotContains(t, response.Body.String(), `"output_tokens":0`)
	assert.NotNil(t, info.UsageEstimationAudit)
}

func TestUsageEstimationPreservesErrorFinishReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		Request:            &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hello"}}},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
			UsageEstimation: &dto.UsageEstimationSettings{Enabled: true, ModelFamily: dto.UsageEstimationModelFamilyGLM},
		}},
	}
	recorder := newChannelOutputRecorder(c.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder
	_, err := recorder.WriteString(`{"id":"x","choices":[{"message":{"reasoning_content":"head"},"finish_reason":"error"}],"usage":{"prompt_tokens":0,"completion_tokens":26973,"total_tokens":26973}}`)
	require.NoError(t, err)
	usage := &dto.Usage{CompletionTokens: 26973, OutputTokens: 26973, TotalTokens: 26973, UpstreamInputReported: true, UpstreamOutputReported: true}

	require.Nil(t, recorder.finish(c, info, usage))
	assert.Contains(t, response.Body.String(), `"finish_reason":"error"`)
	assert.Greater(t, usage.PromptTokens, 0)
	assert.Equal(t, 26973, usage.CompletionTokens)
}

func TestChannelOutputRecorderDoesNotRetryEmptyNonStreamWithInputUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`)
	require.NoError(t, err)
	assert.Empty(t, response.Body.String())

	require.Nil(t, recorder.finish(ctx, info, &dto.Usage{PromptTokens: 5, TotalTokens: 5}))
	assert.Contains(t, response.Body.String(), `"prompt_tokens":5`)
}

func TestChannelOutputRecorderDoesNotRetryEmptyContentWithNonzeroInputUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":5,"completion_tokens":12,"total_tokens":17}}`)
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 5, CompletionTokens: 12, TotalTokens: 17}

	require.Nil(t, recorder.finish(ctx, info, usage))
	assert.Equal(t, 12, usage.CompletionTokens)
	assert.Contains(t, response.Body.String(), `"prompt_tokens":5`)
}

func TestKimiK3HiddenReasoningOnlyResponseDoesNotRetryWithNonzeroInputUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		OriginModelName:    "kimi-k3",
		KimiK3HideThinking: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				KimiK3OfficialCompatibility: true,
			},
		},
	}
	info.ActivateKimiK3OfficialCompatibility()
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":null}}],"usage":{"prompt_tokens":5,"completion_tokens":12,"total_tokens":17}}`)
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 5, CompletionTokens: 12, TotalTokens: 17}

	require.Nil(t, recorder.finish(ctx, info, usage))
	assert.Contains(t, response.Body.String(), `"prompt_tokens":5`)
}

func TestChannelOutputRecorderRetriesEmptyOutputWithExplicitZeroInputUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	require.NoError(t, err)
	policyErr := recorder.finish(ctx, info, &dto.Usage{})

	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelZeroOutput, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderDoesNotRejectMissingUsageWithTokenLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			},
		},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
	require.NoError(t, err)
	require.Nil(t, recorder.finish(ctx, info, &dto.Usage{}))
	assert.Contains(t, response.Body.String(), "hello")
}

func TestChannelOutputRecorderRejectsZeroOutputWithoutEstimation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{OutputTokens: 1_000_000},
			},
		},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder
	ctx.Writer.Header().Set("Content-Type", "application/json")

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":"hello world"}}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`)
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 5, TotalTokens: 5}

	policyErr := recorder.finish(ctx, info, usage)

	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelZeroOutput, policyErr.GetErrorCode())
	assert.False(t, usage.Estimated)
	assert.Zero(t, usage.CompletionTokens)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderUsesBillingUsageForStrictValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{OutputTokens: 1_000_000},
			},
		},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14}}`)
	require.NoError(t, err)
	usage := &dto.Usage{
		PromptTokens:     5,
		CompletionTokens: 9,
		TotalTokens:      14,
		BillingUsage: &dto.BillingUsage{
			Source:   dto.BillingUsageSourceOAIChat,
			Semantic: dto.BillingUsageSemanticOpenAI,
			OpenAIUsage: &dto.Usage{
				PromptTokens: 5,
				TotalTokens:  5,
			},
		},
	}

	policyErr := recorder.finish(ctx, info, usage)

	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelZeroOutput, policyErr.GetErrorCode())
	assert.False(t, usage.Estimated)
	assert.Equal(t, 9, usage.CompletionTokens)
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderAppliesInputLimitToBodyAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{UsageTokenLimit: &dto.UsageTokenLimitSettings{
				InputTokens: 100,
			}},
		},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder
	ctx.Writer.Header().Set("Content-Type", "application/json")

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010}}`)
	require.NoError(t, err)
	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 10,
		TotalTokens:      1010,
		BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
			PromptTokens:     1000,
			CompletionTokens: 10,
			TotalTokens:      1010,
		}),
	}

	require.Nil(t, recorder.finish(ctx, info, usage))
	assert.GreaterOrEqual(t, usage.PromptTokens, 30)
	assert.LessOrEqual(t, usage.PromptTokens, 95)
	assert.Equal(t, usage.PromptTokens+10, usage.TotalTokens)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, usage.PromptTokens, usage.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, usage.TotalTokens, usage.BillingUsage.OpenAIUsage.TotalTokens)
	require.NotNil(t, info.UsageTokenLimitAudit)
	require.NotNil(t, info.UsageTokenLimitAudit.Input)
	assert.Equal(t, 1000, info.UsageTokenLimitAudit.Input.Original)

	var payload struct {
		Usage dto.Usage `json:"usage"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.Equal(t, usage.PromptTokens, payload.Usage.PromptTokens)
	assert.Equal(t, usage.TotalTokens, payload.Usage.TotalTokens)
}

func TestChannelOutputRecorderPreservesFirstStatusCodeBeforeCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	recorder.WriteHeader(299)
	recorder.WriteHeader(201)
	_, err := recorder.WriteString(`{"choices":[{"message":{"content":"hello"}}]}`)
	require.NoError(t, err)
	recorder.WriteHeader(202)

	require.Nil(t, recorder.finish(ctx, info, &dto.Usage{CompletionTokens: 1, TotalTokens: 1}))
	assert.Equal(t, 299, response.Code)
}

func TestChannelOutputRecorderCommitsFirstEffectiveStreamOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		IsStream:           true,
		ShouldIncludeUsage: false,
		OriginModelName:    "gpt-test",
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "hello")

	_, err = recorder.WriteString("data: [DONE]\n\n")
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 10, TotalTokens: 10}
	require.Nil(t, recorder.finish(ctx, info, usage))

	assert.Zero(t, usage.CompletionTokens)
	assert.NotContains(t, response.Body.String(), "\"usage\"")
	assert.True(t, strings.HasSuffix(response.Body.String(), "data: [DONE]\n\n"))
}

func TestChannelOutputRecorderRejectsUncommittedStreamZeroUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		IsStream:           true,
		ShouldIncludeUsage: true,
		OriginModelName:    "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{OutputTokens: 1_000_000},
			},
		},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":0,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)

	policyErr := recorder.finish(ctx, info, &dto.Usage{PromptTokens: 10, TotalTokens: 10})

	require.NotNil(t, policyErr)
	assert.False(t, types.IsSkipRetryError(policyErr))
	assert.Equal(t, types.ErrorCodeChannelZeroOutput, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderReleasesHeldTailWithCompleteUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		IsStream:           true,
		ShouldIncludeUsage: true,
		OriginModelName:    "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{InputTokens: 1_000_000},
			},
		},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "hello")
	_, err = recorder.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)

	require.Nil(t, recorder.finish(ctx, info, &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}))
	assert.Contains(t, response.Body.String(), "hello")
	assert.True(t, strings.HasSuffix(response.Body.String(), "data: [DONE]\n\n"))
}

func TestChannelOutputRecorderCommitsKimiReasoningImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "kimi-k3",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				KimiK3OfficialCompatibility: true,
			},
		},
	}
	info.ActivateKimiK3OfficialCompatibility()
	recorder := newChannelOutputRecorder(
		ctx.Writer,
		info,
		true,
		false,
		responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "[内容已过滤]"),
		64*1024,
	)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n")
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "thinking")
	assert.True(t, recorder.committed)
}

func TestChannelOutputRecorderCommitsNonVisibleSemanticOutputImmediately(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		event     string
		want      string
	}{
		{
			name:      "OpenAI reasoning",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			event:     `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
			want:      "thinking",
		},
		{
			name:      "OpenAI tool call",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			event:     `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"lookup","arguments":"{}"}}]}}]}`,
			want:      "lookup",
		},
		{
			name:      "Responses reasoning",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			event:     `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
			want:      "thinking",
		},
		{
			name:      "Responses tool call",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			event:     `{"type":"response.function_call_arguments.delta","delta":"{}"}`,
			want:      "function_call",
		},
		{
			name:   "Claude thinking",
			format: types.RelayFormatClaude,
			event:  `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"thinking"}}`,
			want:   "thinking",
		},
		{
			name:   "Claude tool use",
			format: types.RelayFormatClaude,
			event:  `{"type":"content_block_start","content_block":{"type":"tool_use","name":"lookup","input":{}}}`,
			want:   "lookup",
		},
		{
			name:   "Gemini thought",
			format: types.RelayFormatGemini,
			event:  `{"candidates":[{"content":{"parts":[{"text":"thinking","thought":true}]}}]}`,
			want:   "thinking",
		},
		{
			name:   "Gemini function call",
			format: types.RelayFormatGemini,
			event:  `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`,
			want:   "lookup",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			info := &relaycommon.RelayInfo{
				RelayFormat:     test.format,
				RelayMode:       test.relayMode,
				IsStream:        true,
				OriginModelName: "test-model",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
			}
			recorder := newChannelOutputRecorder(
				ctx.Writer,
				info,
				true,
				false,
				responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "[内容已过滤]"),
				64*1024,
			)
			ctx.Writer = recorder

			_, err := recorder.WriteString("data: " + test.event + "\n\n")
			require.NoError(t, err)
			assert.True(t, recorder.committed)
			assert.Contains(t, response.Body.String(), test.want)
		})
	}
}

func TestChannelOutputRecorderKeepsKimiWhitespaceContentUnderPrefixInspection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "kimi-k3",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				KimiK3OfficialCompatibility: true,
			},
		},
	}
	info.ActivateKimiK3OfficialCompatibility()
	recorder := newChannelOutputRecorder(
		ctx.Writer,
		info,
		true,
		false,
		responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "[内容已过滤]"),
		64*1024,
	)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"  \"}}]}\n\n")
	require.NoError(t, err)
	assert.False(t, recorder.committed)

	_, err = recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"[内容已过滤]\"}}]}\n\n")
	require.ErrorIs(t, err, errResponseContentMatched)
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderReleasesContentAsSoonAsNoRuleCanMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "test-model",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
	}
	recorder := newChannelOutputRecorder(
		ctx.Writer,
		info,
		false,
		false,
		responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "内容已过滤"),
		64*1024,
	)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"内\"}}]}\n\n")
	require.NoError(t, err)
	assert.False(t, recorder.committed)
	assert.Empty(t, response.Body.String())

	_, err = recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"容正常\"}}]}\n\n")
	require.NoError(t, err)
	assert.True(t, recorder.committed)
	assert.Contains(t, response.Body.String(), `"content":"内"`)
	assert.Contains(t, response.Body.String(), `"content":"容正常"`)
}

func TestChannelOutputRecorderTreatsCommittedWriteFailureAsClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	connectionReset := errors.New("write: connection reset by peer")
	failingWriter := &failAfterWriteResponseWriter{
		ResponseWriter:   c.Writer,
		successfulWrites: 1,
		err:              connectionReset,
	}
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "kimi-k3",
		StreamStatus:    relaycommon.NewStreamStatus(),
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				KimiK3OfficialCompatibility: true,
			},
		},
	}
	recorder := newChannelOutputRecorder(failingWriter, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "first")

	_, err = recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n")
	require.ErrorIs(t, err, connectionReset)
	require.Nil(t, recorder.finish(c, info, &dto.Usage{CompletionTokens: 1, TotalTokens: 1}))
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.Snapshot().EndReason)
	assert.NotContains(t, response.Body.String(), "second")
}

func TestChannelOutputRecorderPreservesInterruptedStreamWithoutUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	status := relaycommon.NewStreamStatus()
	status.RequireProtocolEnd()
	status.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "gpt-test",
		StreamStatus:    status,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{InputTokens: 100},
			},
		},
	}
	recorder := newChannelOutputRecorder(c.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 10, TotalTokens: 1010, Estimated: true}
	require.Nil(t, recorder.finish(c, info, usage))

	assert.Contains(t, response.Body.String(), "partial")
	assert.Equal(t, relaycommon.StreamEndReasonEOF, status.Snapshot().EndReason)
	assert.True(t, status.IsInterrupted())
	assert.GreaterOrEqual(t, usage.PromptTokens, 30)
	assert.LessOrEqual(t, usage.PromptTokens, 95)
	assert.Equal(t, 10, usage.CompletionTokens)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.NotNil(t, info.UsageTokenLimitAudit)
	require.NotNil(t, info.UsageTokenLimitAudit.Input)
}

func TestChannelOutputRecorderMarksCanceledRequestAsClientGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	status := relaycommon.NewStreamStatus()
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "gpt-test",
		StreamStatus:    status,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{OutputTokens: 40},
			},
		},
	}
	recorder := newChannelOutputRecorder(c.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	require.NoError(t, err)
	cancel()
	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 500, TotalTokens: 510, Estimated: true}
	require.Nil(t, recorder.finish(c, info, usage))

	assert.Equal(t, relaycommon.StreamEndReasonClientGone, status.Snapshot().EndReason)
	assert.ErrorIs(t, status.Snapshot().EndError, context.Canceled)
	assert.Equal(t, 10, usage.PromptTokens)
	assert.GreaterOrEqual(t, usage.CompletionTokens, 12)
	assert.LessOrEqual(t, usage.CompletionTokens, 38)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.NotNil(t, info.UsageTokenLimitAudit)
	require.NotNil(t, info.UsageTokenLimitAudit.Output)
}

func TestChannelOutputRecorderDoesNotOverrideInterruptedEmptyStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	status := relaycommon.NewStreamStatus()
	status.RequireProtocolEnd()
	status.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "gpt-test",
		StreamStatus:    status,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{InputTokens: 1_000_000},
			},
		},
	}
	recorder := newChannelOutputRecorder(c.Writer, info, true, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	c.Writer = recorder

	require.Nil(t, recorder.finish(c, info, &dto.Usage{}))
	assert.Equal(t, relaycommon.StreamEndReasonEOF, status.Snapshot().EndReason)
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderHandlesTCPResetAfterVisibleStreamOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousStreamingTimeout })
	type handlerResult struct {
		policyErr  *types.NewAPIError
		endReason  relaycommon.StreamEndReason
		upstreamID int
	}
	resultChan := make(chan handlerResult, 1)
	upstreamClosed := make(chan struct{})

	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		info := &relaycommon.RelayInfo{
			RelayFormat:     types.RelayFormatOpenAI,
			RelayMode:       relayconstant.RelayModeChatCompletions,
			IsStream:        true,
			OriginModelName: "kimi-k3",
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: "kimi-k3",
				ChannelOtherSettings: dto.ChannelOtherSettings{
					KimiK3OfficialCompatibility: true,
				},
			},
		}
		recorder := newChannelOutputRecorder(c.Writer, info, true, false, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
		c.Writer = recorder
		upstreamBody := &closeAwareStreamBody{
			first:  []byte("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n"),
			closed: upstreamClosed,
		}
		resp := &http.Response{Body: upstreamBody}
		_ = helper.StreamScannerHandler(c, resp, info, func(data string, result *helper.StreamResult) {
			if err := helper.StringData(c, data); err != nil {
				result.Error(err)
			}
		})
		_, _ = recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"after-reset\"}}]}\n\n")
		policyErr := recorder.finish(c, info, &dto.Usage{CompletionTokens: 1, TotalTokens: 1})
		resultChan <- handlerResult{
			policyErr:  policyErr,
			endReason:  info.StreamStatus.Snapshot().EndReason,
			upstreamID: info.ReceivedResponseCount,
		}
	})

	server := httptest.NewUnstartedServer(router)
	server.EnableHTTP2 = false
	server.Start()
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	connection, err := net.DialTimeout("tcp", address, 2*time.Second)
	require.NoError(t, err)
	tcpConnection := connection.(*net.TCPConn)
	request := fmt.Sprintf("POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Length: 0\r\nConnection: keep-alive\r\n\r\n", address)
	_, err = io.WriteString(tcpConnection, request)
	require.NoError(t, err)

	reader := bufio.NewReader(tcpConnection)
	visibleOutput := false
	for !visibleOutput {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		visibleOutput = strings.Contains(line, "first")
	}
	require.NoError(t, tcpConnection.SetLinger(0))
	require.NoError(t, tcpConnection.Close())

	select {
	case <-upstreamClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream body was not closed after the client reset")
	}
	select {
	case result := <-resultChan:
		require.Nil(t, result.policyErr)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, result.endReason)
		assert.Equal(t, 1, result.upstreamID)
	case <-time.After(2 * time.Second):
		t.Fatal("relay handler did not return after the client reset")
	}
}

func TestChannelOutputRecorderDropsTerminalTailAfterCommittedZeroUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		IsStream:           true,
		ShouldIncludeUsage: true,
		OriginModelName:    "gpt-test",
		StreamStatus:       relaycommon.NewStreamStatus(),
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				UsageTokenLimit: &dto.UsageTokenLimitSettings{OutputTokens: 1_000_000},
			},
		},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, true, operation_setting.ResponseContentRetryPolicy{}, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "hello")
	_, err = recorder.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":0,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 10, TotalTokens: 10}
	policyErr := recorder.finish(ctx, info, usage)

	require.NotNil(t, policyErr)
	assert.True(t, types.IsSkipRetryError(policyErr))
	assert.Equal(t, types.ErrorCodeChannelZeroOutput, policyErr.GetErrorCode())
	assert.NotContains(t, response.Body.String(), `"usage"`)
	assert.NotContains(t, response.Body.String(), "[DONE]")
	snapshot := info.StreamStatus.Snapshot()
	require.Len(t, snapshot.Errors, 1)
	assert.Equal(t, service.ErrUpstreamUsageMissingOutput.Error(), snapshot.Errors[0].Message)
}

func TestObserveChannelOutputPayloadRecognizesSupportedProtocols(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		payload   string
	}{
		{
			name:      "OpenAI chat text",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			payload:   `{"choices":[{"message":{"content":"hello"}}]}`,
		},
		{
			name:      "OpenAI completion text",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeCompletions,
			payload:   `{"choices":[{"text":"hello"}]}`,
		},
		{
			name:      "Responses text delta",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			payload:   `{"type":"response.output_text.delta","delta":"hello"}`,
		},
		{
			name:    "Claude tool use",
			format:  types.RelayFormatClaude,
			payload: `{"content":[{"type":"tool_use","name":"search","input":{}}]}`,
		},
		{
			name:    "Gemini function call",
			format:  types.RelayFormatGemini,
			payload: `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search","args":{}}}]}}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, common.UnmarshalJsonStr(test.payload, &payload))
			var output strings.Builder

			assert.True(t, observeChannelOutputPayload(test.format, test.relayMode, payload, &output))
			assert.NotEmpty(t, output.String())
		})
	}

	var empty map[string]any
	require.NoError(t, common.UnmarshalJsonStr(`{"choices":[{"delta":{"role":"assistant","content":""}}]}`, &empty))
	var output strings.Builder
	assert.False(t, observeChannelOutputPayload(types.RelayFormatOpenAI, relayconstant.RelayModeChatCompletions, empty, &output))
}

func TestKimiK3OfficialOutputPolicyAcceptsReasoningOnlyResponses(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		payload   string
	}{
		{
			name:      "OpenAI Chat reasoning only",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			payload:   `{"choices":[{"message":{"content":"","reasoning_content":"thinking"}}]}`,
		},
		{
			name:      "Responses reasoning only",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			payload:   `{"output":[{"type":"reasoning","content":[{"type":"summary_text","text":"thinking"}]}]}`,
		},
		{
			name:    "Anthropic thinking only",
			format:  types.RelayFormatClaude,
			payload: `{"content":[{"type":"thinking","thinking":"thinking"}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, common.UnmarshalJsonStr(test.payload, &payload))
			var output strings.Builder
			assert.True(t, observeChannelOutputPayload(test.format, test.relayMode, payload, &output))
			assert.NotEmpty(t, output.String())
		})
	}
}

func TestKimiK3OfficialOutputPolicyAcceptsVisibleTextAndTools(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		payload   string
	}{
		{
			name:      "OpenAI Chat text",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			payload:   `{"choices":[{"message":{"content":"answer"}}]}`,
		},
		{
			name:      "Responses tool",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			payload:   `{"output":[{"type":"function_call","name":"lookup","arguments":"{}"}]}`,
		},
		{
			name:    "Anthropic tool",
			format:  types.RelayFormatClaude,
			payload: `{"content":[{"type":"tool_use","name":"lookup","input":{}}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, common.UnmarshalJsonStr(test.payload, &payload))
			var output strings.Builder
			assert.True(t, observeChannelOutputPayload(test.format, test.relayMode, payload, &output))
			assert.NotEmpty(t, output.String())
		})
	}
}

func TestGeminiOutputEventWithUsageIsNotHeldAsTerminalTail(t *testing.T) {
	event := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n")

	assert.False(t, isChannelOutputStreamTailEvent(types.RelayFormatGemini, event))
}
