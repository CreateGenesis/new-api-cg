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

func TestOaiChatToResponsesHandlerRestoresTNTRequestContractFields(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "resp_tnt_contract")

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
		Request: &dto.OpenAIResponsesRequest{
			Model:             "kimi-k3",
			Instructions:      []byte(`"Use the weather tool."`),
			Tools:             []byte(`[{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object"}}]`),
			ToolChoice:        []byte(`"required"`),
			Truncation:        []byte(`"auto"`),
			ParallelToolCalls: []byte(`false`),
			Metadata:          []byte(`{"case":"tnt"}`),
			User:              []byte(`"user-1"`),
			Reasoning:         &dto.Reasoning{Effort: "high", Summary: "auto"},
		},
	}

	usage, apiErr := OaiChatToResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	var got struct {
		Instructions      string            `json:"instructions"`
		Tools             []map[string]any  `json:"tools"`
		ToolChoice        string            `json:"tool_choice"`
		Truncation        string            `json:"truncation"`
		ParallelToolCalls bool              `json:"parallel_tool_calls"`
		Metadata          map[string]string `json:"metadata"`
		User              string            `json:"user"`
		Reasoning         *dto.Reasoning    `json:"reasoning"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &got))
	assert.Equal(t, "Use the weather tool.", got.Instructions)
	require.Len(t, got.Tools, 1)
	assert.Equal(t, "get_weather", got.Tools[0]["name"])
	assert.Equal(t, "required", got.ToolChoice)
	assert.Equal(t, "auto", got.Truncation)
	assert.False(t, got.ParallelToolCalls)
	assert.Equal(t, map[string]string{"case": "tnt"}, got.Metadata)
	assert.Equal(t, "user-1", got.User)
	require.NotNil(t, got.Reasoning)
	assert.Equal(t, "high", got.Reasoning.Effort)
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
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":4,"total_tokens":6}}`,
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
		Model:             "kimi-k3",
		MaxOutputTokens:   &maxOutputTokens,
		Temperature:       &temperature,
		TopP:              &topP,
		Tools:             []byte(`[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]`),
		ToolChoice:        []byte(`"required"`),
		Truncation:        []byte(`"auto"`),
		ParallelToolCalls: []byte(`false`),
	}

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	assert.Equal(t, 2, strings.Count(got, `"max_output_tokens":64`))
	assert.Equal(t, 2, strings.Count(got, `"temperature":0.25`))
	assert.Equal(t, 2, strings.Count(got, `"top_p":0.75`))
	assert.NotContains(t, got, `"max_output_tokens":0`)

	events := make([]map[string]any, 0)
	for _, line := range strings.Split(got, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "" {
			continue
		}
		var event map[string]any
		require.NoError(t, common.Unmarshal([]byte(data), &event))
		events = append(events, event)
	}
	require.NotEmpty(t, events)
	for index, event := range events {
		assert.EqualValues(t, index, event["sequence_number"])
	}

	var textDone map[string]any
	var reasoningDone map[string]any
	var argumentsDone map[string]any
	var completed map[string]any
	for _, event := range events {
		switch event["type"] {
		case "response.output_text.done":
			textDone = event
		case "response.reasoning_summary_text.done":
			reasoningDone = event
		case "response.function_call_arguments.done":
			argumentsDone = event
		case "response.completed":
			completed = event
		}
	}
	require.NotNil(t, textDone)
	assert.Equal(t, "OK", textDone["text"])
	assert.Empty(t, textDone["logprobs"])
	require.NotNil(t, reasoningDone)
	assert.Equal(t, "thinking", reasoningDone["text"])
	assert.NotContains(t, reasoningDone, "part")
	require.NotNil(t, argumentsDone)
	assert.Equal(t, `{"city":"Paris"}`, argumentsDone["arguments"])
	assert.Equal(t, "get_weather", argumentsDone["name"])
	require.NotNil(t, completed)
	completedResponse, ok := completed["response"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "required", completedResponse["tool_choice"])
	assert.Equal(t, "auto", completedResponse["truncation"])
	assert.Equal(t, false, completedResponse["parallel_tool_calls"])
	assert.Len(t, completedResponse["tools"], 1)
}

func TestOaiChatToResponsesStreamHandlerDoesNotApplyTNTEnvelopeWithoutFlag(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.RelayFormat = types.RelayFormatOpenAIResponses
	info.Request = &dto.OpenAIResponsesRequest{Model: "gpt-test"}

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.NotContains(t, recorder.Body.String(), `"sequence_number"`)
}

func TestOaiChatToResponsesHandlerRestoresKimiK3MetadataAndModel(t *testing.T) {
	maxOutputTokens := uint(64)
	temperature := 1.0
	topP := 0.95
	c, recorder, resp, info := newResponsesChatTestContext(t, `{
		"id":"chatcmpl_1",
		"object":"chat.completion",
		"created":1710000000,
		"model":"kimi-k3",
		"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
	}`, false)
	info.ChannelMeta.ChannelOtherSettings.KimiK3OfficialCompatibility = true
	info.ChannelMeta.ChannelType = constant.ChannelTypeOpenAI
	info.ChannelMeta.UpstreamModelName = "kimi-k3"
	info.UpstreamModelName = "kimi-k3"
	info.OriginModelName = "client-k3"
	info.IsModelMapped = true
	info.ActivateKimiK3OfficialCompatibility()
	info.RelayFormat = types.RelayFormatOpenAIResponses
	info.Request = &dto.OpenAIResponsesRequest{
		Model:           "client-k3",
		MaxOutputTokens: &maxOutputTokens,
		Temperature:     &temperature,
		TopP:            &topP,
		Metadata:        []byte(`{"trace":"k3"}`),
		Reasoning:       &dto.Reasoning{Effort: "high"},
	}

	usage, apiErr := OaiChatToResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	var got struct {
		Model           string            `json:"model"`
		MaxOutputTokens uint              `json:"max_output_tokens"`
		Temperature     float64           `json:"temperature"`
		TopP            float64           `json:"top_p"`
		Metadata        map[string]string `json:"metadata"`
		Reasoning       *dto.Reasoning    `json:"reasoning"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &got))
	assert.Equal(t, "client-k3", got.Model)
	assert.Equal(t, maxOutputTokens, got.MaxOutputTokens)
	assert.Equal(t, temperature, got.Temperature)
	assert.Equal(t, topP, got.TopP)
	assert.Equal(t, map[string]string{"trace": "k3"}, got.Metadata)
	require.NotNil(t, got.Reasoning)
	assert.Equal(t, "high", got.Reasoning.Effort)
}

func TestOaiChatToResponsesStreamHandlerRestoresKimiK3MetadataWithoutTNTEnvelope(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"kimi-k3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	maxOutputTokens := uint(64)
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.ChannelMeta.ChannelOtherSettings.KimiK3OfficialCompatibility = true
	info.ChannelMeta.ChannelType = constant.ChannelTypeOpenAI
	info.ChannelMeta.UpstreamModelName = "kimi-k3"
	info.UpstreamModelName = "kimi-k3"
	info.ActivateKimiK3OfficialCompatibility()
	info.RelayFormat = types.RelayFormatOpenAIResponses
	info.Request = &dto.OpenAIResponsesRequest{Model: "kimi-k3", MaxOutputTokens: &maxOutputTokens}

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	got := recorder.Body.String()
	assert.Equal(t, 2, strings.Count(got, `"max_output_tokens":64`))
	assert.NotContains(t, got, `"sequence_number"`)
	assert.NotContains(t, got, `"max_output_tokens":0`)
}
