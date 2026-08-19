package relay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	openaichannel "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func responseRetryPolicy(mode operation_setting.ResponseContentMatchMode, content string) operation_setting.ResponseContentRetryPolicy {
	return operation_setting.ResponseContentRetryPolicy{
		Enabled: true,
		Rules:   []operation_setting.ResponseContentRetryRule{{Mode: mode, Content: content}},
	}
}

func TestResponseContentMatcherHandlesLeadingWhitespaceAndSplitPrefix(t *testing.T) {
	matcher := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "内容已过滤"))
	matcher.append(" \n\t内容")
	assert.False(t, matcher.matched)
	matcher.append("已过滤，稍后重试")
	assert.True(t, matcher.matched)
}

func TestResponseContentMatcherExactWaitsForCompletion(t *testing.T) {
	matcher := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchExact, "blocked"))
	matcher.append("blocked")
	assert.False(t, matcher.matched)
	assert.True(t, matcher.finish())

	nonMatch := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchExact, "blocked"))
	nonMatch.append("blocked response")
	assert.True(t, nonMatch.resolvedWithoutMatch())
	assert.False(t, nonMatch.finish())
}

func TestVisibleResponseExtractionSupportsDownstreamProtocols(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		payload   map[string]any
		stream    bool
	}{
		{
			name: "openai chat", format: types.RelayFormatOpenAI, relayMode: relayconstant.RelayModeChatCompletions,
			payload: map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "blocked"}}}}, stream: true,
		},
		{
			name: "responses", format: types.RelayFormatOpenAIResponses,
			payload: map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "refusal", "refusal": "blocked"}}}}},
		},
		{
			name: "claude", format: types.RelayFormatClaude,
			payload: map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "blocked"}}, stream: true,
		},
		{
			name: "gemini", format: types.RelayFormatGemini,
			payload: map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "blocked"}}}}}}, stream: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"))
			observeVisibleResponsePayload(tt.format, tt.relayMode, tt.payload, matcher, tt.stream)
			assert.True(t, matcher.matched)
		})
	}
}

func TestVisibleResponseExtractionSupportsErrorEnvelopes(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{name: "openai error object", payload: map[string]any{"error": map[string]any{"message": "blocked by upstream"}}},
		{name: "string error", payload: map[string]any{"error": "blocked by upstream"}},
		{name: "responses nested error", payload: map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"message": "blocked by upstream"}}}},
		{name: "typed top-level error", payload: map[string]any{"type": "server_error", "message": "blocked by upstream"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"))
			observeVisibleResponsePayload(types.RelayFormatOpenAI, relayconstant.RelayModeChatCompletions, tt.payload, matcher, true)
			assert.True(t, matcher.matched)
		})
	}
}

func TestVisibleResponseExtractionDoesNotTreatOrdinaryMessageMetadataAsError(t *testing.T) {
	matcher := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"))
	observeVisibleResponsePayload(types.RelayFormatOpenAI, relayconstant.RelayModeChatCompletions, map[string]any{
		"type":    "message_start",
		"message": "blocked metadata",
	}, matcher, true)
	assert.False(t, matcher.matched)
	assert.False(t, matcher.resolvedWithoutMatch())
}

func TestVisibleResponseExtractionIgnoresReasoningToolsAndGeminiThoughts(t *testing.T) {
	matcher := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"))
	observeVisibleResponsePayload(types.RelayFormatOpenAI, relayconstant.RelayModeChatCompletions, map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"content":           "safe",
			"reasoning_content": "blocked",
			"tool_calls":        []any{map[string]any{"function": map[string]any{"arguments": "blocked"}}},
		}}},
	}, matcher, false)
	assert.True(t, matcher.resolvedWithoutMatch())
	assert.False(t, matcher.matched)

	thoughtMatcher := newResponseContentMatcher(responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"))
	observeVisibleResponsePayload(types.RelayFormatGemini, 0, map[string]any{
		"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"text": "blocked", "thought": true},
			map[string]any{"text": "safe"},
		}}}},
	}, thoughtMatcher, false)
	assert.True(t, thoughtMatcher.resolvedWithoutMatch())
	assert.False(t, thoughtMatcher.matched)
}

func TestResponsesReasoningCommitsBeforeLaterVisibleContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIResponses, RelayMode: relayconstant.RelayModeResponses, IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"), 64*1024)
	ctx.Writer = recorder

	_, err := recorder.Write([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n"))
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "thinking")
	_, err = recorder.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"blocked\"}]}]}}\n\n"))
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "blocked")
}

func TestChannelOutputRecorderRejectsMatchedNonStreamWithoutWritingBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"), 64*1024)
	ctx.Writer = recorder
	_, err := recorder.Write([]byte(`{"choices":[{"message":{"content":"  blocked by policy"}}]}`))
	require.NoError(t, err)

	policyErr := recorder.finish(ctx, info, &dto.Usage{CompletionTokens: 1})
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelResponseContentMatch, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderRejectsMatchedStreamBeforeCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "内容已过滤"), 64*1024)
	ctx.Writer = recorder
	_, err := recorder.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"内容\"}}]}\n\n"))
	require.NoError(t, err)
	_, writeErr := recorder.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"已过滤\"}}]}\n\n"))
	require.ErrorIs(t, writeErr, errResponseContentMatched)

	policyErr := recorder.finish(ctx, info, &dto.Usage{CompletionTokens: 1})
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelResponseContentMatch, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderRejectsOpenAIStreamErrorMessageBeforeCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "服务繁忙，请稍后重试"), 64*1024)
	ctx.Writer = recorder

	_, writeErr := recorder.Write([]byte("data: {\"error\": {\"message\": \"服务繁忙，请稍后重试。\", \"type\": \"server_error\"}}\n\ndata: [DONE]\n\n"))
	require.ErrorIs(t, writeErr, errResponseContentMatched)
	assert.Empty(t, response.Body.String())

	policyErr := recorder.finish(ctx, info, &dto.Usage{})
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelResponseContentMatch, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderRejectsSSEErrorBodyForNonStreamRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: false, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "服务繁忙，请稍后重试"), 64*1024)
	ctx.Writer = recorder

	_, err := recorder.Write([]byte("data: {\"error\": {\"message\": \"服务繁忙，请稍后重试。\", \"type\": \"server_error\"}}\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)
	policyErr := recorder.finish(ctx, info, &dto.Usage{})
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelResponseContentMatch, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestNonStreamTNTErrorSSEMatchesResponseContentPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousStreamingTimeout })
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		IsStream:    false,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				TNTTencentOpenAIConversion: true,
			},
		},
	}
	attempt := &simulatedModelCacheAttempt{
		responseContentRetryPolicy: responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "服务繁忙，请稍后重试"),
	}
	recorder := beginSimulatedModelCacheRecorder(ctx, info, attempt)
	sse := "data: {\"error\": {\"message\": \"服务繁忙，请稍后重试。\", \"type\": \"server_error\"}}\n\ndata: [DONE]\n\n"
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}

	usage, upstreamErr := openaichannel.OaiChatBufferedStreamHandler(ctx, info, upstream)
	assert.Nil(t, usage)
	require.NotNil(t, upstreamErr)
	assert.Equal(t, http.StatusOK, upstreamErr.StatusCode)
	assert.Equal(t, "服务繁忙，请稍后重试。", upstreamErr.Error())

	policyErr := restoreSimulatedModelCacheRecorder(ctx, recorder, upstreamErr)
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelResponseContentMatch, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestRetryableNonStreamErrorBypassesResponseContentPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = originalRanges })
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: false, ChannelMeta: &relaycommon.ChannelMeta{}}
	attempt := &simulatedModelCacheAttempt{
		responseContentRetryPolicy: responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "服务繁忙，请稍后重试"),
	}
	recorder := beginSimulatedModelCacheRecorder(ctx, info, attempt)
	upstreamErr := types.NewErrorWithStatusCode(errors.New("服务繁忙，请稍后重试。"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)

	policyErr := restoreSimulatedModelCacheRecorder(ctx, recorder, upstreamErr)
	assert.Nil(t, policyErr)
	assert.Empty(t, response.Body.String())
}

func TestResponseContentRetryGateFollowsStatusCodeRetryPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = originalRanges })

	newContext := func(settings *dto.StatusCodeRetrySettings) *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		if settings != nil {
			ctx.Set(string(constant.ContextKeyChannelOtherSetting), dto.ChannelOtherSettings{StatusCodeRetry: settings})
		}
		return ctx
	}
	newError := func(code int) *types.NewAPIError {
		return types.NewErrorWithStatusCode(errors.New("服务繁忙，请稍后重试。"), types.ErrorCodeBadResponseStatusCode, code)
	}

	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	assert.False(t, responseContentRetryEligibleError(newContext(nil), newError(http.StatusServiceUnavailable)))
	assert.True(t, responseContentRetryEligibleError(newContext(nil), newError(http.StatusBadGateway)))
	assert.True(t, responseContentRetryEligibleError(newContext(nil), newError(http.StatusGatewayTimeout)))
	assert.True(t, responseContentRetryEligibleError(newContext(nil), newError(http.StatusOK)))
	assert.False(t, responseContentRetryEligibleError(newContext(nil), newError(99)))

	channelOverride := &dto.StatusCodeRetrySettings{Enabled: true, StatusCodes: "429"}
	assert.True(t, responseContentRetryEligibleError(newContext(channelOverride), newError(http.StatusServiceUnavailable)))
	channelOverride.StatusCodes = "503"
	assert.False(t, responseContentRetryEligibleError(newContext(channelOverride), newError(http.StatusServiceUnavailable)))
	channelOverride.StatusCodes = "not-a-status-code"
	assert.False(t, responseContentRetryEligibleError(newContext(channelOverride), newError(http.StatusServiceUnavailable)))
}

func TestNonStreamNonRetryableStatusCodeErrorMatchesResponseContentPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 500, End: 500}}
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = originalRanges })

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: false, ChannelMeta: &relaycommon.ChannelMeta{}}
	attempt := &simulatedModelCacheAttempt{
		responseContentRetryPolicy: responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "服务繁忙，请稍后重试"),
	}
	recorder := beginSimulatedModelCacheRecorder(ctx, info, attempt)
	upstreamErr := types.NewErrorWithStatusCode(errors.New("服务繁忙，请稍后重试。"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)

	policyErr := restoreSimulatedModelCacheRecorder(ctx, recorder, upstreamErr)
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelResponseContentMatch, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}
