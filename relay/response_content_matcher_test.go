package relay

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
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

func TestResponsesCompletedEventRemainsVisibleToMatcherAfterReasoningOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIResponses, RelayMode: relayconstant.RelayModeResponses, IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"), 1, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.Write([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n"))
	require.NoError(t, err)
	_, err = recorder.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"blocked\"}]}]}}\n\n"))
	require.ErrorIs(t, err, errResponseContentMatched)
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderRejectsMatchedNonStreamWithoutWritingBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "blocked"), 1, 64*1024)
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
	recorder := newChannelOutputRecorder(ctx.Writer, info, false, responseRetryPolicy(operation_setting.ResponseContentMatchPrefix, "内容已过滤"), 1, 64*1024)
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
