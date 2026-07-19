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

func TestOpenAIStreamHandlerReturnsOriginalModelNameWhenMapped(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, info := newMappedOpenAIResponseTestContext(t)
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

	got := recorder.Body.String()
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
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
