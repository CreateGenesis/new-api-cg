package openai

import (
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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	written := make(chan struct{}, 16)
	c.Writer = &streamWriteNotificationWriter{ResponseWriter: c.Writer, written: written}
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
	select {
	case <-written:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("content chunk was not forwarded")
	}

	_, err = io.WriteString(upstreamWriter, "data: "+finish+"\n\n")
	require.NoError(t, err)
	select {
	case <-written:
		assert.Contains(t, response.Body.String(), `"finish_reason":"stop"`)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("finish chunk was held until [DONE]")
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
