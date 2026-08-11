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

func TestOaiChatToResponsesHandlerRestoresTNTRequestMetadata(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	maxOutputTokens := uint(64)
	zeroOutputTokens := uint(0)
	temperature := 0.25
	topP := 0.75
	zeroSampling := float64(0)
	tests := []struct {
		name               string
		request            *dto.OpenAIResponsesRequest
		wantMaxOutputToken *uint
		wantTemperature    float64
		wantTopP           float64
	}{
		{
			name: "explicit values",
			request: &dto.OpenAIResponsesRequest{
				Model:           "kimi-k3",
				MaxOutputTokens: &maxOutputTokens,
				Temperature:     &temperature,
				TopP:            &topP,
			},
			wantMaxOutputToken: &maxOutputTokens,
			wantTemperature:    temperature,
			wantTopP:           topP,
		},
		{
			name:               "responses defaults",
			request:            &dto.OpenAIResponsesRequest{Model: "kimi-k3"},
			wantMaxOutputToken: nil,
			wantTemperature:    1,
			wantTopP:           1,
		},
		{
			name: "explicit zero values",
			request: &dto.OpenAIResponsesRequest{
				Model:           "kimi-k3",
				MaxOutputTokens: &zeroOutputTokens,
				Temperature:     &zeroSampling,
				TopP:            &zeroSampling,
			},
			wantMaxOutputToken: &zeroOutputTokens,
			wantTemperature:    0,
			wantTopP:           0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set(common.RequestIdKey, "resp_tnt_metadata")

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"chatcmpl_1","object":"chat.completion","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
				)),
			}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "kimi-k3",
					ChannelOtherSettings: dto.ChannelOtherSettings{
						TNTTencentOpenAIConversion: true,
					},
				},
				RelayFormat: types.RelayFormatOpenAIResponses,
				Request:     test.request,
			}

			usage, apiErr := OaiChatToResponsesHandler(c, info, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)

			var got struct {
				MaxOutputTokens *uint   `json:"max_output_tokens"`
				Temperature     float64 `json:"temperature"`
				TopP            float64 `json:"top_p"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &got))
			assert.Equal(t, test.wantMaxOutputToken, got.MaxOutputTokens)
			assert.Equal(t, test.wantTemperature, got.Temperature)
			assert.Equal(t, test.wantTopP, got.TopP)
			assert.Equal(t, 1, strings.Count(recorder.Body.String(), `"max_output_tokens"`))
		})
	}
}

func TestOaiChatToResponsesStreamHandlerRestoresTNTRequestMetadata(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	maxOutputTokens := uint(64)
	temperature := 0.25
	topP := 0.75
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.ChannelMeta.ChannelOtherSettings.TNTTencentOpenAIConversion = true
	info.RelayFormat = types.RelayFormatOpenAIResponses
	info.Request = &dto.OpenAIResponsesRequest{
		Model:           "kimi-k3",
		MaxOutputTokens: &maxOutputTokens,
		Temperature:     &temperature,
		TopP:            &topP,
	}

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	assert.Equal(t, 2, strings.Count(got, `"max_output_tokens":64`))
	assert.Equal(t, 2, strings.Count(got, `"temperature":0.25`))
	assert.Equal(t, 2, strings.Count(got, `"top_p":0.75`))
	assert.NotContains(t, got, `"max_output_tokens":0`)
}
