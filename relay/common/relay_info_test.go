package common

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoCacheUsageValidationSplitEnabledIsChannelScoped(t *testing.T) {
	var nilInfo *RelayInfo
	require.False(t, nilInfo.CacheUsageValidationSplitEnabled())
	require.False(t, (&RelayInfo{}).CacheUsageValidationSplitEnabled())
	require.True(t, (&RelayInfo{
		ChannelMeta: &ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{CacheUsageValidationSplit: true},
		},
	}).CacheUsageValidationSplitEnabled())
}

func TestRelayInfoConvOptionsIncludesAnthropicUsageCompatibility(t *testing.T) {
	info := &RelayInfo{ChannelMeta: &ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
		AnthropicInputIncludesCache: true,
	}}}

	assert.True(t, info.ConvOptions().Claude.AnthropicInputIncludesCache)
	assert.False(t, (&RelayInfo{}).ConvOptions().Claude.AnthropicInputIncludesCache)
}

func TestRelayInfoConvOptionsKeepsAnthropicUsageCompatibilityOnRetry(t *testing.T) {
	info := &RelayInfo{
		RetryIndex: 1,
		ChannelMeta: &ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
			AnthropicInputIncludesCache: true,
		}},
	}

	assert.True(t, info.ConvOptions().Claude.AnthropicInputIncludesCache)
}

func TestRelayInfoConsumesStreamProtocolEndRequirementPerHandler(t *testing.T) {
	info := &RelayInfo{IsStream: true}
	info.RequireStreamProtocolEnd()

	require.True(t, info.ConsumeStreamProtocolEndRequirement())
	require.False(t, info.ConsumeStreamProtocolEndRequirement())
}

func TestGenRelayInfoFreezesClientRequestMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stream := true
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{Stream: &stream}, nil)
	require.NoError(t, err)
	require.Equal(t, types.RequestModeStream, info.ClientRequestMode)

	info.IsStream = false
	require.Equal(t, types.RequestModeStream, info.ClientRequestMode)
}

func TestGenRelayInfoAssignsExplicitModesForRealtimeAndTasks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	realtimeContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	realtimeContext.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	realtime, err := GenRelayInfo(realtimeContext, types.RelayFormatOpenAIRealtime, nil, nil)
	require.NoError(t, err)
	require.Equal(t, types.RequestModeStream, realtime.ClientRequestMode)
	require.True(t, realtime.IsStream)

	taskContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	taskContext.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	task, err := GenRelayInfo(taskContext, types.RelayFormatTask, nil, nil)
	require.NoError(t, err)
	require.Equal(t, types.RequestModeNonStream, task.ClientRequestMode)
}

func TestBeginUpstreamAttemptRestoresAttemptScopedState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	rootcommon.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	rootcommon.SetContextKey(ctx, constant.ContextKeyChannelId, 2)
	rootcommon.SetContextKey(ctx, constant.ContextKeyChannelKey, "second-key")
	rootcommon.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, "https://second.example")
	rootcommon.SetContextKey(ctx, constant.ContextKeyOriginalModel, "client-model")

	start := time.Now().Add(-time.Minute)
	request := &dto.GeneralOpenAIRequest{Model: "client-model"}
	info := &RelayInfo{
		StartTime:         start,
		FirstResponseTime: start.Add(time.Second),
		isFirstResponse:   false,
		RelayFormat:       types.RelayFormatOpenAIResponses,
		ClientRequestMode: types.RequestModeNonStream,
		OriginModelName:   "client-model",
		RequestURLPath:    "/v1/responses?client=1",
		Request:           request,
		RetryIndex:        3,
		ResponsesUsageInfo: &ResponsesUsageInfo{BuiltInTools: map[string]*BuildInToolInfo{
			dto.BuildInToolWebSearchPreview: {
				ToolName:          dto.BuildInToolWebSearchPreview,
				SearchContextSize: "large",
			},
		}},
	}
	info.PriceData.AddOtherRatio("n", 3)
	info.CaptureUpstreamAttemptBaseline()

	info.OriginModelName = "gemini-model-nothinking"
	info.RequestURLPath = "/v1/models/replicate/predictions"
	info.IsStream = true
	info.IsGeminiBatchEmbedding = true
	info.DisablePing = true
	info.ReasoningEffort = "high"
	info.SendResponseCount = 8
	info.ReceivedResponseCount = 9
	info.RuntimeHeadersOverride = map[string]interface{}{"authorization": "Bearer leaked"}
	info.UseRuntimeHeadersOverride = true
	info.ParamOverrideAudit = []string{"set model"}
	info.KimiK3OfficialCompatibilityActive = true
	info.KimiK3HideThinking = true
	info.KimiK3BillingAudit = &dto.KimiK3BillingAudit{Equation: "leaked"}
	info.KimiK3MatchedStopSequence = "<stop>"
	info.UpstreamRequestBodySize = 1234
	info.SimulatedModelCacheInfo = &SimulatedModelCacheInfo{Mode: "leaked"}
	info.UsageTokenLimitAudit = &UsageTokenLimitAudit{Input: &UsageTokenLimitDirectionAudit{Original: 999}}
	info.RequestConversionChain = []types.RelayFormat{types.RelayFormatOpenAIResponses, types.RelayFormatClaude}
	info.FinalRequestRelayFormat = types.RelayFormatClaude
	info.StreamStatus = &StreamStatus{}
	info.StreamProtocolEndRequired = true
	info.ThinkingContentInfo = ThinkingContentInfo{HasSentThinkingContent: true}
	info.ClaudeConvertInfo = &ClaudeConvertInfo{Done: true}
	info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount = 4
	info.PriceData.AddOtherRatio("n", 7)
	info.PriceData.AddOtherRatio("prompt_extend", 2)
	request.Model = "mutated-model"
	ctx.Set("claude_web_search_requests", 2)
	ctx.Set("image_generation_call", true)
	ctx.Set("image_generation_call_quality", "high")
	ctx.Set("image_generation_call_size", "1536x1024")

	info.BeginUpstreamAttempt(ctx)

	require.Equal(t, "client-model", info.OriginModelName)
	require.Equal(t, "/v1/responses?client=1", info.RequestURLPath)
	require.False(t, info.IsStream)
	require.False(t, info.IsGeminiBatchEmbedding)
	require.False(t, info.HasSendResponse())
	require.False(t, info.DisablePing)
	require.Empty(t, info.ReasoningEffort)
	require.Zero(t, info.SendResponseCount)
	require.Zero(t, info.ReceivedResponseCount)
	require.Nil(t, info.RuntimeHeadersOverride)
	require.False(t, info.UseRuntimeHeadersOverride)
	require.Nil(t, info.ParamOverrideAudit)
	require.False(t, info.IsKimiK3OfficialCompatibility())
	require.False(t, info.KimiK3HideThinking)
	require.Nil(t, info.KimiK3BillingAudit)
	require.Empty(t, info.KimiK3MatchedStopSequence)
	require.Zero(t, info.UpstreamRequestBodySize)
	require.Nil(t, info.SimulatedModelCacheInfo)
	require.Nil(t, info.UsageTokenLimitAudit)
	require.Equal(t, []types.RelayFormat{types.RelayFormatOpenAIResponses}, info.RequestConversionChain)
	require.Empty(t, info.FinalRequestRelayFormat)
	require.Nil(t, info.StreamStatus)
	require.False(t, info.StreamProtocolEndRequired)
	require.True(t, info.ThinkingContentInfo.IsFirstThinkingContent)
	require.False(t, info.ThinkingContentInfo.HasSentThinkingContent)
	require.Nil(t, info.ClaudeConvertInfo)
	require.Equal(t, map[string]float64{"n": 3}, info.PriceData.OtherRatios())
	require.Equal(t, "client-model", request.Model)
	require.Equal(t, 3, info.RetryIndex)
	require.NotNil(t, info.ResponsesUsageInfo)
	tool := info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview]
	require.NotNil(t, tool)
	require.Zero(t, tool.CallCount)
	require.Equal(t, "large", tool.SearchContextSize)
	require.Zero(t, ctx.GetInt("claude_web_search_requests"))
	require.False(t, ctx.GetBool("image_generation_call"))
	require.Empty(t, ctx.GetString("image_generation_call_quality"))
	require.Empty(t, ctx.GetString("image_generation_call_size"))
	require.Equal(t, 2, info.ChannelId)
	require.Equal(t, "second-key", info.ApiKey)
}

func TestBeginUpstreamAttemptRebuildsClaudeConversionState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rootcommon.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	rootcommon.SetContextKey(ctx, constant.ContextKeyOriginalModel, "claude-test")

	info := &RelayInfo{
		StartTime:       time.Now(),
		RelayFormat:     types.RelayFormatClaude,
		OriginModelName: "claude-test",
		RequestURLPath:  "/v1/messages",
		ClaudeConvertInfo: &ClaudeConvertInfo{
			Done: true,
		},
	}
	info.CaptureUpstreamAttemptBaseline()
	info.ClaudeConvertInfo = &ClaudeConvertInfo{Done: true, Index: 9}

	info.BeginUpstreamAttempt(ctx)

	require.NotNil(t, info.ClaudeConvertInfo)
	require.Equal(t, LastMessageTypeNone, info.ClaudeConvertInfo.LastMessagesType)
	require.False(t, info.ClaudeConvertInfo.Done)
}

func TestBeginUpstreamAttemptRestoresTaskBaselines(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	rootcommon.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	rootcommon.SetContextKey(ctx, constant.ContextKeyOriginalModel, "sora")

	info := &RelayInfo{
		StartTime:       time.Now(),
		RelayFormat:     types.RelayFormatTask,
		OriginModelName: "sora",
		RequestURLPath:  "/v1/videos",
		TaskRelayInfo: &TaskRelayInfo{
			Action:       constant.TaskActionRemix,
			OriginTaskID: "origin-task",
			PublicTaskID: "public-task",
		},
	}
	info.CaptureUpstreamAttemptBaseline()
	info.TaskRelayInfo.Action = "generate"
	ctx.Set("task_request", "leaked")
	ctx.Set("action", "leaked")

	info.BeginUpstreamAttempt(ctx)

	require.Equal(t, constant.TaskActionRemix, info.TaskRelayInfo.Action)
	require.Equal(t, "origin-task", info.TaskRelayInfo.OriginTaskID)
	require.Equal(t, "public-task", info.TaskRelayInfo.PublicTaskID)
	require.Nil(t, info.ClaudeConvertInfo)
	value, exists := ctx.Get("task_request")
	require.True(t, exists)
	require.Nil(t, value)
	require.Empty(t, ctx.GetString("action"))
}

func TestRelayInfoMetaTypedNilReceiver(t *testing.T) {
	var info *RelayInfo
	var meta convmeta.Meta = info

	assert.Empty(t, meta.GetOriginModelName())
	assert.Empty(t, meta.GetUpstreamModelName())
	assert.False(t, meta.HasChannelMeta())
	assert.Zero(t, meta.GetChannelID())
	assert.Zero(t, meta.GetChannelType())
	assert.False(t, meta.GetIsStream())
	assert.Empty(t, meta.GetReasoningEffort())
	assert.Nil(t, meta.ReasoningState())
	assert.Zero(t, meta.GetEstimatePromptTokens())
	assert.Zero(t, meta.GetSendResponseCount())

	assert.NotPanics(t, func() {
		meta.SetReasoningEffort("high")
		meta.IncrSendResponseCount()
		meta.AppendRequestConversion(types.RelayFormatClaude)
	})

	firstState := meta.EnsureClaudeConvertInfo()
	secondState := meta.EnsureClaudeConvertInfo()
	require.NotNil(t, firstState)
	require.NotNil(t, secondState)
	assert.Equal(t, convmeta.LastMessageTypeNone, firstState.LastMessagesType)
	assert.NotSame(t, firstState, secondState)

	firstOptions := meta.ConvOptions()
	secondOptions := meta.ConvOptions()
	require.NotNil(t, firstOptions)
	require.NotNil(t, secondOptions)
	assert.NotSame(t, firstOptions, secondOptions)
	assert.NotNil(t, firstOptions.Claude.DefaultMaxTokens)
	assert.NotNil(t, firstOptions.Gemini.SupportsImagine)
	assert.NotNil(t, firstOptions.Gemini.SafetySetting)
	assert.NotNil(t, firstOptions.PreserveThinkingSuffix)
	assert.NotNil(t, firstOptions.PreserveEffortTail)
}

func TestGenRelayInfoCapturesRequestReasoningEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		path        string
		relayFormat types.RelayFormat
		request     dto.Request
		expected    string
	}{
		{
			name:        "OpenAI chat top-level effort",
			path:        "/v1/chat/completions",
			relayFormat: types.RelayFormatOpenAI,
			request:     &dto.GeneralOpenAIRequest{Model: "gpt-5.6-sol", ReasoningEffort: " high "},
			expected:    "high",
		},
		{
			name:        "OpenRouter nested chat effort",
			path:        "/v1/chat/completions",
			relayFormat: types.RelayFormatOpenAI,
			request:     &dto.GeneralOpenAIRequest{Model: "anthropic/claude", Reasoning: json.RawMessage(`{"effort":"xhigh"}`)},
			expected:    "xhigh",
		},
		{
			name:        "OpenAI Responses effort",
			path:        "/v1/responses",
			relayFormat: types.RelayFormatOpenAIResponses,
			request:     &dto.OpenAIResponsesRequest{Model: "gpt-5.6-sol", Reasoning: &dto.Reasoning{Effort: "max"}},
			expected:    "max",
		},
		{
			name:        "explicit none is preserved",
			path:        "/v1/responses",
			relayFormat: types.RelayFormatOpenAIResponses,
			request:     &dto.OpenAIResponsesRequest{Model: "gpt-5.6-sol", Reasoning: &dto.Reasoning{Effort: "none"}},
			expected:    "none",
		},
		{
			name:        "non-string nested effort is ignored",
			path:        "/v1/chat/completions",
			relayFormat: types.RelayFormatOpenAI,
			request:     &dto.GeneralOpenAIRequest{Model: "anthropic/claude", Reasoning: json.RawMessage(`{"effort":42}`)},
			expected:    "",
		},
		{
			name:        "Claude output config effort",
			path:        "/v1/messages",
			relayFormat: types.RelayFormatClaude,
			request:     &dto.ClaudeRequest{Model: "claude-opus-4-7", OutputConfig: json.RawMessage(`{"effort":"medium"}`)},
			expected:    "medium",
		},
		{
			name:        "Gemini thinking level",
			path:        "/v1beta/models/gemini-3-pro:generateContent",
			relayFormat: types.RelayFormatGemini,
			request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "low"},
			}},
			expected: "low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", tt.path, nil)

			info, err := GenRelayInfo(ctx, tt.relayFormat, tt.request, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, info.ReasoningEffort)
		})
	}
}

func TestGenRelayInfoKeepsOriginAndLeavesBillingUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	const model = "qwen3.8-max@thinking:on@temperature:0.2"
	ctx.Set("original_model", model)

	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{Model: model}, nil)
	require.NoError(t, err)
	assert.Equal(t, model, info.OriginModelName)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, model, info.GetBillingModelName())
}

func TestInitChannelMetaRestoresRequestReasoningEffortForRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	request := &dto.OpenAIResponsesRequest{
		Model:     "gpt-5.6-sol",
		Reasoning: &dto.Reasoning{Effort: "max"},
	}
	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAIResponses, request, nil)
	require.NoError(t, err)

	info.SetReasoningEffort("high")
	info.InitChannelMeta(ctx)
	assert.Equal(t, "max", info.ReasoningEffort)

	info.SetReasoningEffort("low")
	info.InitChannelMeta(ctx)
	assert.Equal(t, "max", info.ReasoningEffort)
}

func TestInitChannelMetaResetsPerAttemptStreamStateAndPreservesRequestState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{Model: "gpt-test"}, nil)
	require.NoError(t, err)

	claudeState := relayconvert.NewClaudeToChatStreamState()
	_, err = claudeState.ConvertChunk(&dto.ClaudeResponse{
		Type:  "content_block_start",
		Index: ptr(7),
		ContentBlock: &dto.ClaudeMediaMessage{
			Type: "tool_use",
			Id:   "toolu_1",
			Name: "lookup",
		},
	})
	require.NoError(t, err)
	_, err = claudeState.ConvertChunk(&dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: ptr(7),
		Delta: &dto.ClaudeMediaMessage{
			Type:        "input_json_delta",
			PartialJson: ptr(`{"q":"x"}`),
		},
	})
	require.NoError(t, err)

	geminiState, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatGemini, relayconvert.ResponseStreamOptions{
		ID:    "chatcmpl_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)

	info.SendResponseCount = 3
	info.ClaudeToChatStreamState = claudeState
	info.ChatToGeminiStreamState = geminiState
	info.LastError = types.NewError(assert.AnError, types.ErrorCodeBadResponseBody)
	info.StreamStatus = NewStreamStatus()
	info.StreamStatus.RecordError("attempt 1 soft error")
	info.RecordConversionDiagnostics(context.Background(), []types.ConversionDiagnostic{{
		Code:     "test.loss",
		Message:  "attempt 1 conversion loss",
		Severity: types.ConversionDiagnosticWarning,
		From:     types.RelayFormatClaude,
		To:       types.RelayFormatOpenAI,
	}})

	info.InitChannelMeta(ctx)

	assert.Zero(t, info.SendResponseCount)
	assert.Nil(t, info.ClaudeToChatStreamState)
	assert.Nil(t, info.ChatToGeminiStreamState)

	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
	assert.Equal(t, 1, info.StreamStatus.TotalErrorCount())
	diagnostics := info.ConversionDiagnostics()
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "test.loss", diagnostics[0].Code)
	require.NotNil(t, info.LastError)

	freshClaude := relayconvert.NewClaudeToChatStreamState()
	_, err = freshClaude.ConvertChunk(&dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: ptr(7),
		Delta: &dto.ClaudeMediaMessage{
			Type:        "input_json_delta",
			PartialJson: ptr(`{"q":"x"}`),
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown content block index")

	info.IncrSendResponseCount()
	responses := relayconvert.StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_retry",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: ptr("hello")},
		}},
	}, info)
	require.NotEmpty(t, responses)
	assert.Equal(t, "message_start", responses[0].Type)
}

func ptr[T any](value T) *T {
	return &value
}
