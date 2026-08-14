package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiChatBufferedStreamHandlerDoesNotCommitSSEBeforeJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	setting := operation_setting.GetGeneralSetting()
	previousPingEnabled := setting.PingIntervalEnabled
	previousPingSeconds := setting.PingIntervalSeconds
	setting.PingIntervalEnabled = true
	setting.PingIntervalSeconds = 1
	t.Cleanup(func() {
		constant.StreamingTimeout = previousStreamingTimeout
		setting.PingIntervalEnabled = previousPingEnabled
		setting.PingIntervalSeconds = previousPingSeconds
	})

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
	upstreamReader, upstreamWriter := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       upstreamReader,
	}
	type handlerResult struct {
		usage  *dto.Usage
		apiErr *types.NewAPIError
	}
	done := make(chan handlerResult, 1)
	go func() {
		usage, apiErr := OaiChatBufferedStreamHandler(c, info, resp)
		done <- handlerResult{usage: usage, apiErr: apiErr}
	}()

	time.Sleep(1200 * time.Millisecond)
	assert.Empty(t, recorder.Body.String())
	assert.Empty(t, recorder.Header().Get("Content-Type"))

	_, err := io.WriteString(upstreamWriter, strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":123,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":123,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n"))
	require.NoError(t, err)
	require.NoError(t, upstreamWriter.Close())

	select {
	case result := <-done:
		require.Nil(t, result.apiErr)
		require.NotNil(t, result.usage)
	case <-time.After(5 * time.Second):
		t.Fatal("buffered stream handler did not finish")
	}
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	assert.NotContains(t, recorder.Body.String(), ": PING")
	var output dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output))
	require.Len(t, output.Choices, 1)
	assert.Equal(t, "hello", output.Choices[0].Message.StringContent())
}

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

func TestOaiChatBufferedStreamHandlerStripsTNTJSONFences(t *testing.T) {
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
		Request: &dto.GeneralOpenAIRequest{
			ResponseFormat: &dto.ResponseFormat{Type: "json_object"},
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				TNTTencentOpenAIConversion: true,
			},
		},
	}
	sse := strings.Join([]string{
		"data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"```json\\n{\\\"answer\\\":\"},\"finish_reason\":null}]}",
		"data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"true}\\n```\"},\"finish_reason\":null}]}",
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":123,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}

	_, apiErr := OaiChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, apiErr)

	var output dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output))
	require.Len(t, output.Choices, 1)
	assert.Equal(t, `{"answer":true}`, output.Choices[0].Message.StringContent())
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

func TestTNTJSONFenceStreamFilterUsesBoundedState(t *testing.T) {
	info := &relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{
			ResponseFormat: &dto.ResponseFormat{Type: "json_schema"},
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{TNTTencentOpenAIConversion: true},
		},
	}
	filter := newTNTJSONFenceStreamFilter(info)
	require.NotNil(t, filter)

	inputs := []struct {
		content      string
		finishReason *string
		want         string
	}{
		{content: "`", want: ""},
		{content: "``js", want: ""},
		{content: "on\r", want: ""},
		{content: "\n{\"answer\":\"", want: "{\"answer\":\""},
		{content: strings.Repeat("x", 1024), want: strings.Repeat("x", 1024)},
		{content: "ok\"}\n`", want: "ok\"}"},
		{content: "``\r\n", finishReason: pointer("stop"), want: ""},
	}
	for _, input := range inputs {
		chunk := &dto.ChatCompletionsStreamResponse{Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Index:        0,
			FinishReason: input.finishReason,
		}}}
		chunk.Choices[0].Delta.SetContentString(input.content)
		filter.Filter(chunk)
		assert.Equal(t, input.want, chunk.Choices[0].Delta.GetContentString())
		if choice := filter.choices[0]; choice != nil {
			assert.LessOrEqual(t, len(choice.pending), len("\r\n```\r\n"))
		}
	}
}

func TestTNTJSONFenceStreamFilterDisablesItselfWhenOpeningDoesNotMatch(t *testing.T) {
	info := &relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{ResponseFormat: &dto.ResponseFormat{Type: "json_object"}},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{TNTTencentOpenAIConversion: true},
		},
	}
	filter := newTNTJSONFenceStreamFilter(info)
	require.NotNil(t, filter)

	first := &dto.ChatCompletionsStreamResponse{Choices: []dto.ChatCompletionsStreamResponseChoice{{Index: 0}}}
	first.Choices[0].Delta.SetContentString("``not-json")
	filter.Filter(first)
	assert.Equal(t, "``not-json", first.Choices[0].Delta.GetContentString())
	assert.Equal(t, tntJSONFencePassThrough, filter.choices[0].state)

	second := &dto.ChatCompletionsStreamResponse{Choices: []dto.ChatCompletionsStreamResponseChoice{{Index: 0}}}
	second.Choices[0].Delta.SetContentString("```json must stay")
	filter.Filter(second)
	assert.Equal(t, "```json must stay", second.Choices[0].Delta.GetContentString())
}

func TestTNTJSONFenceStreamFilterOnlyAppliesToStructuredOutputRequests(t *testing.T) {
	info := &relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{TNTTencentOpenAIConversion: true},
		},
	}
	assert.Nil(t, newTNTJSONFenceStreamFilter(info))

	responsesText, err := common.Marshal(map[string]any{
		"format": map[string]any{"type": "json_schema", "name": "answer", "schema": map[string]any{"type": "object"}},
	})
	require.NoError(t, err)
	info.Request = &dto.OpenAIResponsesRequest{Text: responsesText}
	assert.NotNil(t, newTNTJSONFenceStreamFilter(info))

	outputFormat, err := common.Marshal(map[string]any{"type": "json_object"})
	require.NoError(t, err)
	info.Request = &dto.ClaudeRequest{OutputFormat: outputFormat}
	assert.NotNil(t, newTNTJSONFenceStreamFilter(info))
}

func pointer[T any](value T) *T {
	return &value
}

func TestOpenaiHandlerStripsTNTJSONFencesFromNonStreamResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		Request:     &dto.GeneralOpenAIRequest{ResponseFormat: &dto.ResponseFormat{Type: "json_object"}},
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName:    "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{TNTTencentOpenAIConversion: true},
		},
	}
	body := "{\"id\":\"chat_1\",\"object\":\"chat.completion\",\"created\":123,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"answer\\\":true}\\n```\\n\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}"
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}

	_, apiErr := OpenaiHandler(c, info, resp)
	require.Nil(t, apiErr)

	var output dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output))
	require.Len(t, output.Choices, 1)
	assert.Equal(t, `{"answer":true}`, output.Choices[0].Message.StringContent())
}
