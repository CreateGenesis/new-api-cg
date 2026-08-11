package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiChatBufferedStreamHandlerAggregatesTextToolsUsageAndFinishReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousStreamingTimeout })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		IsStream:    false,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				TNTTencentOpenAIConversion: true,
			},
		},
	}
	sse := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":123,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant","content":"Claude "},"finish_reason":null}]}`,
		"",
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":123,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"Code","reasoning_content":"think","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"Anthropic"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":123,"model":"kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}

	usage, apiErr := OaiChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)

	var output dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output))
	require.Len(t, output.Choices, 1)
	assert.Equal(t, "AI Assistant", output.Choices[0].Message.StringContent())
	assert.Equal(t, "think", output.Choices[0].Message.GetReasoningContent())
	assert.Equal(t, "tool_calls", output.Choices[0].FinishReason)
	toolCalls := output.Choices[0].Message.ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call_1", toolCalls[0].ID)
	assert.Equal(t, "lookup", toolCalls[0].Function.Name)
	assert.JSONEq(t, `{"q":"Provider"}`, toolCalls[0].Function.Arguments)
}

func TestOpenaiHandlerReturnsSanitizedTNTNonStreamResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				TNTTencentOpenAIConversion: true,
			},
		},
	}
	body := `{"id":"chat_1","object":"chat.completion","created":123,"model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"Claude Code from Anthropic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5},"upstream_extension":"kept"}`
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := OpenaiHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	var output map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Equal(t, "kept", output["upstream_extension"])
	choices := output["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	assert.Equal(t, "AI Assistant from Provider", message["content"])
}
