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
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newMappedOpenAIResponseTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "model-mapping-response-test")

	info := &relaycommon.RelayInfo{
		OriginModelName:    "glm-5.2",
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "xopglm52",
			IsModelMapped:     true,
		},
	}
	return c, recorder, info
}

func TestOpenAIHandlerReturnsOriginalModelNameWhenMapped(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1710000000,
			"model":"xopglm52",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}
		}`)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, err := OpenaiHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
}

func TestOpenAIHandlerHidesKimiK3ThinkingAndReasoningUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	info.KimiK3OfficialCompatibilityActive = true
	info.KimiK3HideThinking = true
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1710000000,
			"model":"kimi-k3",
			"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"hidden reasoning","reasoning":"hidden alias","content":"THINKING_OFF_OK"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":2,"completion_tokens":63,"total_tokens":65,"completion_tokens_details":{"reasoning_tokens":51}}
		}`)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, apiErr := OpenaiHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Equal(t, 12, usage.CompletionTokens)
	require.Equal(t, 14, usage.TotalTokens)
	require.Zero(t, usage.CompletionTokenDetails.ReasoningTokens)

	got := recorder.Body.String()
	require.Contains(t, got, `"content":"THINKING_OFF_OK"`)
	require.Contains(t, got, `"finish_reason":"length"`)
	require.Contains(t, got, `"completion_tokens":12`)
	require.Contains(t, got, `"total_tokens":14`)
	require.NotContains(t, got, `"reasoning_tokens"`)
	require.NotContains(t, got, `"completion_tokens_details"`)
	require.NotContains(t, got, "hidden reasoning")
	require.NotContains(t, got, "hidden alias")
	require.NotContains(t, got, `"reasoning_content"`)
	require.NotContains(t, got, `"reasoning":`)
}

func TestOpenAIStreamHandlerReturnsOriginalModelNameWhenMapped(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	info.SetEstimatePromptTokens(7)
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"xopglm52","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"xopglm52","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, err := OaiStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.True(t, usage.Estimated)
	require.Equal(t, 7, usage.PromptTokens)
	require.Positive(t, usage.CompletionTokens)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)

	got := recorder.Body.String()
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
}

func TestOpenAIStreamHandlerPreservesReasoningForClaudeClient(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	info.RelayFormat = types.RelayFormatClaude
	info.IsStream = true
	info.ClaudeConvertInfo = &relaycommon.ClaudeConvertInfo{
		LastMessagesType: relaycommon.LastMessageTypeNone,
	}
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"reasoning_content":"reasoning","content":"answer"},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, err := OaiStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	require.Equal(t, 1, strings.Count(got, `"type":"message_start"`))
	require.Contains(t, got, `"type":"thinking"`)
	require.Contains(t, got, `"type":"thinking_delta","thinking":"reasoning"`)
	require.Contains(t, got, `"type":"text_delta","text":"answer"`)
	require.Contains(t, got, `"type":"message_stop"`)
}

func TestOpenAIStreamHandlerHidesKimiK3ThinkingForClaudeClient(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	info.RelayFormat = types.RelayFormatClaude
	info.IsStream = true
	info.KimiK3OfficialCompatibilityActive = true
	info.KimiK3HideThinking = true
	info.ClaudeConvertInfo = &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"reasoning_content":"hidden reasoning"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"THINKING_OFF_OK"},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":12,"total_tokens":14,"completion_tokens_details":{"reasoning_tokens":8}}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 6, usage.TotalTokens)
	require.Zero(t, usage.CompletionTokenDetails.ReasoningTokens)

	got := recorder.Body.String()
	require.Equal(t, 1, strings.Count(got, `"type":"message_start"`))
	require.Contains(t, got, `"type":"text_delta","text":"THINKING_OFF_OK"`)
	require.Contains(t, got, `"type":"message_stop"`)
	require.NotContains(t, got, "hidden reasoning")
	require.NotContains(t, got, `"type":"thinking"`)
	require.NotContains(t, got, `"thinking_delta"`)
}

func TestOpenAIStreamHandlerHidesKimiK3ReasoningUsageForOpenAIClient(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	info.IsStream = true
	info.KimiK3OfficialCompatibilityActive = true
	info.KimiK3HideThinking = true
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"reasoning_content":"hidden reasoning"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"THINKING_OFF_OK"},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":12,"total_tokens":14,"completion_tokens_details":{"reasoning_tokens":8}}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 6, usage.TotalTokens)
	require.Zero(t, usage.CompletionTokenDetails.ReasoningTokens)

	got := recorder.Body.String()
	require.Contains(t, got, `"content":"THINKING_OFF_OK"`)
	require.Contains(t, got, `"completion_tokens":4`)
	require.Contains(t, got, `"total_tokens":6`)
	require.NotContains(t, got, "hidden reasoning")
	require.NotContains(t, got, `"reasoning_tokens"`)
	require.NotContains(t, got, `"completion_tokens_details"`)
}

func TestOpenAIHandlerPreservesReasoningForClaudeClient(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	info.RelayFormat = types.RelayFormatClaude
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1710000000,
			"model":"kimi-k3",
			"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"reasoning","content":"answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}
		}`)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, err := OpenaiHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	require.Contains(t, got, `"type":"thinking","thinking":"reasoning"`)
	require.Contains(t, got, `"type":"text","text":"answer"`)
}

func TestOpenAIStreamHandlerFinishReasonCompletesProtocolWithoutDone(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, _, info := newMappedOpenAIResponseTestContext(t)
	info.IsStream = true
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"xopglm52","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"xopglm52","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}

	usage, err := OaiStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.NotNil(t, info.StreamStatus)
	require.False(t, info.StreamStatus.IsInterrupted())
	snapshot := info.StreamStatus.Snapshot()
	require.Equal(t, "finish_reason", snapshot.ProtocolEndEvent)
	require.Equal(t, relaycommon.StreamEndReasonEOF, snapshot.EndReason)
}

func TestOpenAIStreamModelRewritePreservesRawChunkFields(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	data := `{"id":"cmpl-1","object":"text_completion","created":1710000000,"model":"xopglm52","choices":[{"text":"ok","index":0,"logprobs":null,"finish_reason":null}]}`

	err := sendStreamData(c, info, data, false, false)
	require.NoError(t, err)

	got := recorder.Body.String()
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.Contains(t, got, `"text":"ok"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
}

func TestOpenAIResponsesHandlerReturnsOriginalModelNameWhenMapped(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_1",
			"object":"response",
			"created_at":1710000000,
			"model":"xopglm52",
			"status":"completed",
			"output":[],
			"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}
		}`)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, err := OaiResponsesHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
}

func TestOpenAIResponsesStreamHandlerReturnsOriginalModelNameWhenMapped(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"xopglm52","created_at":1710000000}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"xopglm52","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, err := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
}

func TestOpenAIResponsesStreamTerminalEventsCompleteProtocol(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	for _, eventType := range []string{"response.completed", "response.done", "response.incomplete"} {
		t.Run(eventType, func(t *testing.T) {
			c, _, info := newMappedOpenAIResponseTestContext(t)
			info.IsStream = true
			info.RequestConversionChain = []types.RelayFormat{types.RelayFormatOpenAIResponses}
			body := `data: {"type":"` + eventType + `","response":{"id":"resp_1","model":"xopglm52","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}` + "\n"
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}

			usage, err := OaiResponsesStreamHandler(c, info, resp)
			require.Nil(t, err)
			require.NotNil(t, usage)
			require.Equal(t, 3, usage.CompletionTokens)
			require.False(t, info.StreamStatus.IsInterrupted())
			require.Equal(t, eventType, info.StreamStatus.Snapshot().ProtocolEndEvent)
		})
	}
}

func TestOpenAIResponsesStreamFailureReturnsErrorWithoutTerminalCompletion(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, _, info := newMappedOpenAIResponseTestContext(t)
	info.IsStream = true
	info.RequestConversionChain = []types.RelayFormat{types.RelayFormatOpenAIResponses}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"server_error\",\"message\":\"failed\"}}}\n",
		)),
	}

	usage, err := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, err)
	require.True(t, info.StreamStatus.IsInterrupted())
	require.False(t, info.StreamStatus.Snapshot().ProtocolEndReceived)
}
