package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessTokenDataCollectsConvertedChatStreams(t *testing.T) {
	tests := []struct {
		name          string
		relayMode     int
		data          string
		wantText      string
		wantToolCount int
		wantFinished  bool
	}{
		{
			name:      "anthropic messages route mode",
			relayMode: relayconstant.RelayModeUnknown,
			data:      `{"choices":[{"delta":{"content":"answer"},"finish_reason":null}]}`,
			wantText:  "answer",
		},
		{
			name:         "gemini route mode reasoning",
			relayMode:    relayconstant.RelayModeGemini,
			data:         `{"choices":[{"delta":{"reasoning_content":"thinking"},"finish_reason":"stop"}]}`,
			wantText:     "thinking",
			wantFinished: true,
		},
		{
			name:          "converted tool call",
			relayMode:     relayconstant.RelayModeUnknown,
			data:          `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`,
			wantText:      `lookup{"q":"x"}`,
			wantToolCount: 1,
		},
		{
			name:         "legacy completions",
			relayMode:    relayconstant.RelayModeCompletions,
			data:         `{"choices":[{"text":"legacy","finish_reason":"stop"}]}`,
			wantText:     "legacy",
			wantFinished: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var responseText strings.Builder
			toolCount := 0

			finished, err := processTokenData(test.relayMode, test.data, &responseText, &toolCount)

			require.NoError(t, err)
			assert.Equal(t, test.wantText, responseText.String())
			assert.Equal(t, test.wantToolCount, toolCount)
			assert.Equal(t, test.wantFinished, finished)
		})
	}
}

type streamWriteNotificationWriter struct {
	gin.ResponseWriter
	written chan struct{}
}

type streamWriteCaptureWriter struct {
	gin.ResponseWriter
	writes chan string
}

func (w *streamWriteNotificationWriter) Write(data []byte) (int, error) {
	written, err := w.ResponseWriter.Write(data)
	if written > 0 {
		select {
		case w.written <- struct{}{}:
		default:
		}
	}
	return written, err
}

func (w *streamWriteNotificationWriter) WriteString(data string) (int, error) {
	written, err := w.ResponseWriter.WriteString(data)
	if written > 0 {
		select {
		case w.written <- struct{}{}:
		default:
		}
	}
	return written, err
}

func (w *streamWriteCaptureWriter) Write(data []byte) (int, error) {
	written, err := w.ResponseWriter.Write(data)
	if written > 0 {
		w.writes <- string(data[:written])
	}
	return written, err
}

func (w *streamWriteCaptureWriter) WriteString(data string) (int, error) {
	written, err := w.ResponseWriter.WriteString(data)
	if written > 0 {
		w.writes <- data[:written]
	}
	return written, err
}

func TestOaiStreamHandlerForwardsFirstChunkBeforeNextUpstreamEvent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	tests := []struct {
		name              string
		model             string
		firstChunk        string
		want              string
		kimiCompatibility bool
	}{
		{
			name:              "Kimi reasoning",
			model:             "kimi-k3",
			firstChunk:        `{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}`,
			want:              "thinking",
			kimiCompatibility: true,
		},
		{
			name:       "generic content",
			model:      "gpt-test",
			firstChunk: `{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
			want:       "hello",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			written := make(chan struct{}, 1)
			c.Writer = &streamWriteNotificationWriter{ResponseWriter: c.Writer, written: written}
			info := &relaycommon.RelayInfo{
				IsStream:        true,
				DisablePing:     true,
				RelayFormat:     types.RelayFormatOpenAI,
				RelayMode:       relayconstant.RelayModeChatCompletions,
				OriginModelName: test.model,
				Request:         &dto.GeneralOpenAIRequest{},
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       constant.ChannelTypeOpenAI,
					UpstreamModelName: test.model,
				},
			}
			if test.kimiCompatibility {
				info.ChannelMeta.ChannelOtherSettings = dto.ChannelOtherSettings{
					TNTTencentOpenAIConversion:  true,
					KimiK3OfficialCompatibility: true,
				}
				info.ActivateKimiK3OfficialCompatibility()
			}
			upstreamReader, upstreamWriter := io.Pipe()
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       upstreamReader,
			}
			type handlerResult struct {
				apiErr *types.NewAPIError
			}
			done := make(chan handlerResult, 1)
			go func() {
				_, apiErr := OaiStreamHandler(c, info, resp)
				done <- handlerResult{apiErr: apiErr}
			}()

			_, err := io.WriteString(upstreamWriter, "data: "+test.firstChunk+"\n\n")
			require.NoError(t, err)
			forwardedBeforeNextEvent := false
			select {
			case <-written:
				forwardedBeforeNextEvent = true
			case <-time.After(500 * time.Millisecond):
			}

			finishChunk := `{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"` + test.model + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
			_, err = io.WriteString(upstreamWriter, "data: "+finishChunk+"\n\ndata: [DONE]\n\n")
			require.NoError(t, err)
			require.NoError(t, upstreamWriter.Close())
			select {
			case result := <-done:
				require.Nil(t, result.apiErr)
			case <-time.After(2 * time.Second):
				t.Fatal("stream handler did not finish")
			}

			assert.True(t, forwardedBeforeNextEvent, "the first semantic chunk was held until another upstream event arrived")
			assert.Contains(t, response.Body.String(), test.want)
		})
	}
}

func TestOaiStreamHandlerEstimatesAnthropicUsageAfterClientAbort(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancel := context.WithCancel(context.Background())
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil).WithContext(requestContext)
	writes := make(chan string, 64)
	c.Writer = &streamWriteCaptureWriter{ResponseWriter: c.Writer, writes: writes}
	info := &relaycommon.RelayInfo{
		IsStream:                         true,
		DisablePing:                      true,
		RelayFormat:                      types.RelayFormatClaude,
		RelayMode:                        relayconstant.RelayModeUnknown,
		FinalRequestRelayFormat:          types.RelayFormatOpenAI,
		OriginModelName:                  "glm-5.3",
		Request:                          &dto.ClaudeRequest{Model: "glm-5.3"},
		ClaudeConvertInfo:                &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone},
		GLM53OfficialCompatibilityActive: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeAnthropic,
			UpstreamModelName: "glm-5.3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				TNTTencentOpenAIConversion: true,
				GLM53OfficialCompatibility: true,
				StreamInterruptionBilling: &dto.StreamInterruptionBillingSettings{
					Mode: dto.StreamInterruptionBillingModeInputOnlyFree,
				},
			},
		},
	}
	info.SetEstimatePromptTokens(17)
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
		usage, apiErr := OaiStreamHandler(c, info, resp)
		done <- handlerResult{usage: usage, apiErr: apiErr}
	}()

	content := `{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","content":"billable output"},"finish_reason":null}]}`
	_, err := io.WriteString(upstreamWriter, "data: "+content+"\n\n")
	require.NoError(t, err)
	var delivered strings.Builder
	for !strings.Contains(delivered.String(), `"type":"text_delta","text":"billable output"`) {
		select {
		case write := <-writes:
			delivered.WriteString(write)
		case <-time.After(500 * time.Millisecond):
			t.Fatal("Anthropic text delta was not forwarded before cancellation")
		}
	}

	cancel()
	select {
	case result := <-done:
		require.Nil(t, result.apiErr)
		require.NotNil(t, result.usage)
		assert.True(t, result.usage.Estimated)
		assert.Equal(t, 17, result.usage.PromptTokens)
		assert.Positive(t, result.usage.CompletionTokens)
		assert.Equal(t, result.usage.PromptTokens+result.usage.CompletionTokens, result.usage.TotalTokens)
		decision := service.EvaluateStreamInterruptionBilling(info, result.usage.CompletionTokens, 900)
		assert.False(t, decision.Applied)
		assert.Equal(t, 900, decision.FinalQuota)
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not finish after client cancellation")
	}
	require.NoError(t, upstreamWriter.Close())

	require.NotNil(t, info.StreamStatus)
	status := info.StreamStatus.Snapshot()
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, status.EndReason)
	assert.True(t, status.ProtocolEndRequired)
	assert.False(t, status.ProtocolEndReceived)
}

func TestOaiStreamHandlerStripsTNTJSONFencesWithoutWaitingForStreamEnd(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writes := make(chan string, 32)
	c.Writer = &streamWriteCaptureWriter{ResponseWriter: c.Writer, writes: writes}
	info := &relaycommon.RelayInfo{
		IsStream:        true,
		DisablePing:     true,
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "kimi-k3",
		Request: &dto.GeneralOpenAIRequest{
			ResponseFormat: &dto.ResponseFormat{Type: "json_object"},
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
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
		apiErr *types.NewAPIError
	}
	done := make(chan handlerResult, 1)
	go func() {
		_, apiErr := OaiStreamHandler(c, info, resp)
		done <- handlerResult{apiErr: apiErr}
	}()

	opening := "{\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"```json\\n\"},\"finish_reason\":null}]}"
	body := "{\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\\\"answer\\\":\\\"streamed\\\"}\"},\"finish_reason\":null}]}"
	_, err := io.WriteString(upstreamWriter, "data: "+opening+"\n\ndata: "+body+"\n\n")
	require.NoError(t, err)

	var delivered strings.Builder
	deliveredBodyBeforeStreamEnd := false
	for !deliveredBodyBeforeStreamEnd {
		select {
		case write := <-writes:
			delivered.WriteString(write)
			deliveredBodyBeforeStreamEnd = strings.Contains(delivered.String(), `answer`)
		case <-time.After(500 * time.Millisecond):
			t.Fatal("JSON body was held until the upstream stream ended")
		}
	}
	assert.NotContains(t, delivered.String(), "```json")

	closing := "{\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\\n```\"},\"finish_reason\":null}]}"
	finish := `{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	_, err = io.WriteString(upstreamWriter, "data: "+closing+"\n\ndata: "+finish+"\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	require.NoError(t, upstreamWriter.Close())
	select {
	case result := <-done:
		require.Nil(t, result.apiErr)
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not finish")
	}

	assert.Contains(t, response.Body.String(), `answer`)
	assert.NotContains(t, response.Body.String(), "```")
}

func TestOaiStreamHandlerForwardsFinishChunkBeforeDone(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writes := make(chan string, 16)
	c.Writer = &streamWriteCaptureWriter{ResponseWriter: c.Writer, writes: writes}
	info := &relaycommon.RelayInfo{
		IsStream:           true,
		DisablePing:        true,
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		ShouldIncludeUsage: false,
		OriginModelName:    "gpt-test",
		Request:            &dto.GeneralOpenAIRequest{},
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	upstreamReader, upstreamWriter := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       upstreamReader,
	}
	done := make(chan *types.NewAPIError, 1)
	go func() {
		_, apiErr := OaiStreamHandler(c, info, resp)
		done <- apiErr
	}()

	content := `{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`
	finish := `{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	_, err := io.WriteString(upstreamWriter, "data: "+content+"\n\n")
	require.NoError(t, err)
	delivered := strings.Builder{}
	for !strings.Contains(delivered.String(), `"content":"answer"`) {
		select {
		case write := <-writes:
			delivered.WriteString(write)
		case <-time.After(500 * time.Millisecond):
			t.Fatal("content chunk was not forwarded")
		}
	}

	_, err = io.WriteString(upstreamWriter, "data: "+finish+"\n\n")
	require.NoError(t, err)
	for !strings.Contains(delivered.String(), `"finish_reason":"stop"`) {
		select {
		case write := <-writes:
			delivered.WriteString(write)
		case <-time.After(500 * time.Millisecond):
			t.Fatal("finish chunk was held until [DONE]")
		}
	}

	_, err = io.WriteString(upstreamWriter, "data: [DONE]\n\n")
	require.NoError(t, err)
	require.NoError(t, upstreamWriter.Close())
	select {
	case apiErr := <-done:
		require.Nil(t, apiErr)
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not finish")
	}
}
