package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type streamWriteNotificationWriter struct {
	gin.ResponseWriter
	written chan struct{}
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

func TestOaiStreamHandlerForwardsFirstReasoningChunkBeforeNextUpstreamEvent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

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
		OriginModelName: "kimi-k3",
		Request:         &dto.GeneralOpenAIRequest{},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				TNTTencentOpenAIConversion:  true,
				KimiK3OfficialCompatibility: true,
			},
		},
	}
	info.ActivateKimiK3OfficialCompatibility()
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

	_, err := io.WriteString(upstreamWriter, "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\n")
	require.NoError(t, err)
	forwardedBeforeNextEvent := false
	select {
	case <-written:
		forwardedBeforeNextEvent = true
	case <-time.After(500 * time.Millisecond):
	}

	_, err = io.WriteString(upstreamWriter, "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	require.NoError(t, upstreamWriter.Close())
	select {
	case result := <-done:
		require.Nil(t, result.apiErr)
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not finish")
	}

	assert.True(t, forwardedBeforeNextEvent, "the first reasoning chunk was held until another upstream event arrived")
	assert.Contains(t, response.Body.String(), "thinking")
}
