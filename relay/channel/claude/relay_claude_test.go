package claude

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	openaiadapter "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleStreamResponseDataMarksClaudeMessageStop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	status := relaycommon.NewStreamStatus()
	status.RequireProtocolEnd()
	info := &relaycommon.RelayInfo{
		RelayFormat:  types.RelayFormatClaude,
		StreamStatus: status,
		ChannelMeta:  &relaycommon.ChannelMeta{},
	}
	claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}

	err := HandleStreamResponseData(c, info, claudeInfo, `{"type":"message_stop"}`)

	require.Nil(t, err)
	snapshot := status.Snapshot()
	assert.True(t, snapshot.ProtocolEndReceived)
	assert.Equal(t, "message_stop", snapshot.ProtocolEndEvent)
}

func TestKimiK3ClaudeStreamThinkingFilterHidesThinkingAndRenumbersText(t *testing.T) {
	filter := relayconvert.NewKimiK3ClaudeStreamThinkingFilter()
	thinkingStart := &dto.ClaudeResponse{
		Type:  "content_block_start",
		Index: common.GetPointer(0),
		ContentBlock: &dto.ClaudeMediaMessage{
			Type:     "thinking",
			Thinking: common.GetPointer(""),
		},
	}
	thinkingDelta := &dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: common.GetPointer(0),
		Delta: &dto.ClaudeMediaMessage{Type: "thinking_delta", Thinking: common.GetPointer("hidden reasoning")},
	}
	thinkingStop := &dto.ClaudeResponse{Type: "content_block_stop", Index: common.GetPointer(0)}
	textStart := &dto.ClaudeResponse{
		Type:         "content_block_start",
		Index:        common.GetPointer(1),
		ContentBlock: &dto.ClaudeMediaMessage{Type: "text", Text: common.GetPointer("")},
	}
	textDelta := &dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: common.GetPointer(1),
		Delta: &dto.ClaudeMediaMessage{Type: "text_delta", Text: common.GetPointer("THINKING_OFF_OK")},
	}

	assert.Nil(t, filter.Filter(thinkingStart))
	assert.Nil(t, filter.Filter(thinkingDelta))
	assert.Nil(t, filter.Filter(thinkingStop))
	require.NotNil(t, filter.Filter(textStart))
	assert.Equal(t, 0, textStart.GetIndex())
	require.NotNil(t, filter.Filter(textDelta))
	assert.Equal(t, 0, textDelta.GetIndex())
	assert.Equal(t, "THINKING_OFF_OK", textDelta.Delta.GetText())
}

func commonPointer[T any](value T) *T {
	return &value
}

func TestTNTTencentRequestURLAndFinalHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: "https://tnt.example/",
			ApiKey:         "secret",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				TNTTencentOpenAIConversion: true,
			},
		},
	}

	requestURL, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://tnt.example/v1/chat/completions", requestURL)

	req := httptest.NewRequest(http.MethodPost, requestURL, nil)
	req.Header.Set("Authorization", "Bearer rewritten")
	req.Header.Set("X-Api-Key", "leaked")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("OpenAI-Project", "project")
	req.Header.Set("X-Stainless-Runtime", "go")
	req.Header.Set("X-Codex-Turn-State", "state")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Length", "999")
	req.Header.Set("X-Keep", "kept")
	req.Host = "rewritten.example"
	require.NoError(t, adaptor.FinalizeRequestHeader(c, req, info))

	assert.Equal(t, "Bearer secret", req.Header.Get("Authorization"))
	assert.Equal(t, "ChatGPT/1.0", req.Header.Get("User-Agent"))
	assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Empty(t, req.Header.Get("X-Api-Key"))
	assert.Empty(t, req.Header.Get("Anthropic-Version"))
	assert.Empty(t, req.Header.Get("OpenAI-Project"))
	assert.Empty(t, req.Header.Get("X-Stainless-Runtime"))
	assert.Empty(t, req.Header.Get("X-Codex-Turn-State"))
	assert.Empty(t, req.Header.Get("Connection"))
	assert.Empty(t, req.Header.Get("Content-Length"))
	assert.Empty(t, req.Host)
	assert.Equal(t, "kept", req.Header.Get("X-Keep"))

	info.RelayFormat = types.RelayFormatOpenAIResponses
	require.NoError(t, adaptor.FinalizeRequestHeader(c, req, info))
	assert.Equal(t, "App/1.0", req.Header.Get("User-Agent"))
}

func TestKimiK3TNTConversionPreservesOfficialReasoningAndResponseFormat(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:       constant.ChannelTypeAnthropic,
		UpstreamModelName: "kimi-k3",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			TNTTencentOpenAIConversion:  true,
			KimiK3OfficialCompatibility: true,
		},
	}}
	info.ActivateKimiK3OfficialCompatibility()
	adaptor := &Adaptor{}

	maxTokens := uint(64)
	claudeRequest := &dto.ClaudeRequest{
		Model:          "kimi-k3",
		MaxTokens:      &maxTokens,
		OutputConfig:   []byte(`{"effort":"high"}`),
		ResponseFormat: []byte(`{"type":"json_object"}`),
	}
	convertedClaude, err := adaptor.ConvertClaudeRequest(nil, info, claudeRequest)
	require.NoError(t, err)
	chatFromClaude := convertedClaude.(*dto.GeneralOpenAIRequest)
	assert.Equal(t, "high", chatFromClaude.ReasoningEffort)
	assert.Equal(t, maxTokens, chatFromClaude.GetMaxTokens())
	require.NotNil(t, chatFromClaude.ResponseFormat)
	assert.Equal(t, "json_object", chatFromClaude.ResponseFormat.Type)

	responsesRequest := dto.OpenAIResponsesRequest{
		Model:           "kimi-k3",
		Input:           []byte(`"hello"`),
		MaxOutputTokens: &maxTokens,
		Reasoning:       &dto.Reasoning{Effort: "low"},
		Text:            []byte(`{"format":{"type":"json_object"}}`),
	}
	convertedResponses, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, responsesRequest)
	require.NoError(t, err)
	chatFromResponses := convertedResponses.(*dto.GeneralOpenAIRequest)
	assert.Equal(t, "low", chatFromResponses.ReasoningEffort)
	require.NotNil(t, chatFromResponses.ResponseFormat)
	assert.Equal(t, "json_object", chatFromResponses.ResponseFormat.Type)

	info.ChannelOtherSettings.TNTTencentOpenAIConversion = false
	chatRequest := &dto.GeneralOpenAIRequest{
		Model:           "kimi-k3",
		MaxTokens:       &maxTokens,
		ReasoningEffort: "high",
		ResponseFormat:  &dto.ResponseFormat{Type: "json_object"},
	}
	convertedChat, err := adaptor.ConvertOpenAIRequest(nil, info, chatRequest)
	require.NoError(t, err)
	claudeFromChat := convertedChat.(*dto.ClaudeRequest)
	assert.Equal(t, "high", claudeFromChat.GetEfforts())
	assert.Empty(t, claudeFromChat.OutputFormat)
	var responseFormat dto.ResponseFormat
	require.NoError(t, common.Unmarshal(claudeFromChat.ResponseFormat, &responseFormat))
	assert.Equal(t, "json_object", responseFormat.Type)

	convertedFromResponses, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, responsesRequest)
	require.NoError(t, err)
	claudeFromResponses := convertedFromResponses.(*dto.ClaudeRequest)
	assert.Equal(t, "low", claudeFromResponses.GetEfforts())
	require.NoError(t, common.Unmarshal(claudeFromResponses.ResponseFormat, &responseFormat))
	assert.Equal(t, "json_object", responseFormat.Type)
}

func TestGLM53TNTConversionPreservesOfficialFields(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:       constant.ChannelTypeAnthropic,
		UpstreamModelName: "mapped-model",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			TNTTencentOpenAIConversion: true,
			GLM53OfficialCompatibility: true,
		},
	}}
	info.ActivateGLM53OfficialCompatibility()
	require.True(t, info.IsGLM53OfficialCompatibility())
	adaptor := &Adaptor{}
	maxTokens := uint(64)
	zeroTopK := 0

	claudeRequest := &dto.ClaudeRequest{
		Model:          "mapped-model",
		MaxTokens:      &maxTokens,
		Thinking:       &dto.Thinking{Type: "adaptive"},
		OutputConfig:   []byte(`{"effort":"high"}`),
		ResponseFormat: []byte(`{"type":"json_object"}`),
		StopSequences:  []string{"<END>"},
		TopK:           &zeroTopK,
	}
	require.NoError(t, relayconvert.NormalizeGLM53ClaudeRequest(claudeRequest))
	convertedClaude, err := adaptor.ConvertClaudeRequest(nil, info, claudeRequest)
	require.NoError(t, err)
	chatFromClaude := convertedClaude.(*dto.GeneralOpenAIRequest)
	assert.Equal(t, "high", chatFromClaude.ReasoningEffort)
	assert.JSONEq(t, `{"type":"enabled"}`, string(chatFromClaude.THINKING))
	assert.Nil(t, chatFromClaude.EnableThinking)
	assert.Equal(t, maxTokens, chatFromClaude.GetMaxTokens())
	assert.Equal(t, []string{"<END>"}, chatFromClaude.Stop)
	assert.Nil(t, chatFromClaude.TopK)
	require.NotNil(t, chatFromClaude.ResponseFormat)
	assert.Equal(t, "json_object", chatFromClaude.ResponseFormat.Type)

	responsesRequest := dto.OpenAIResponsesRequest{
		Model:           "mapped-model",
		Input:           []byte(`"hello"`),
		MaxOutputTokens: &maxTokens,
		Reasoning:       &dto.Reasoning{Effort: "low"},
		Text:            []byte(`{"format":{"type":"json_object"}}`),
	}
	convertedResponses, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, responsesRequest)
	require.NoError(t, err)
	chatFromResponses := convertedResponses.(*dto.GeneralOpenAIRequest)
	assert.Equal(t, "low", chatFromResponses.ReasoningEffort)
	assert.Equal(t, maxTokens, chatFromResponses.GetMaxTokens())
	require.NotNil(t, chatFromResponses.ResponseFormat)
	assert.Equal(t, "json_object", chatFromResponses.ResponseFormat.Type)

	info.ChannelOtherSettings.TNTTencentOpenAIConversion = false
	chatRequest := &dto.GeneralOpenAIRequest{
		Model:           "mapped-model",
		MaxTokens:       &maxTokens,
		ReasoningEffort: "max",
	}
	convertedChat, err := adaptor.ConvertOpenAIRequest(nil, info, chatRequest)
	require.NoError(t, err)
	claudeFromChat := convertedChat.(*dto.ClaudeRequest)
	require.NotNil(t, claudeFromChat.Thinking)
	assert.Equal(t, "enabled", claudeFromChat.Thinking.Type)
	assert.Equal(t, "max", claudeFromChat.GetEfforts())

	responsesRequest.Reasoning = &dto.Reasoning{Effort: "max"}
	convertedResponses, err = adaptor.ConvertOpenAIResponsesRequest(nil, info, responsesRequest)
	require.NoError(t, err)
	claudeFromResponses := convertedResponses.(*dto.ClaudeRequest)
	require.NotNil(t, claudeFromResponses.Thinking)
	assert.Equal(t, "enabled", claudeFromResponses.Thinking.Type)
	assert.Equal(t, "max", claudeFromResponses.GetEfforts())
}

func TestGLM53TNTConversionAcceptsScalarAnthropicResponseFormat(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:       constant.ChannelTypeAnthropic,
		UpstreamModelName: "mapped-model",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			TNTTencentOpenAIConversion: true,
			GLM53OfficialCompatibility: true,
		},
	}}
	info.ActivateGLM53OfficialCompatibility()
	request := &dto.ClaudeRequest{
		Model:          "mapped-model",
		ResponseFormat: []byte(`true`),
	}
	require.NoError(t, relayconvert.NormalizeGLM53ClaudeRequest(request))

	converted, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, request)
	require.NoError(t, err)
	chatRequest := converted.(*dto.GeneralOpenAIRequest)
	assert.Nil(t, chatRequest.ResponseFormat)
}

func TestGLM53ClaudeStopPatchPreservesUnknownResponseFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "mapped-model",
		RelayFormat:     types.RelayFormatClaude,
		Request: &dto.ClaudeRequest{
			StopSequences: []string{"<END>"},
		},
		ChannelMeta:                      &relaycommon.ChannelMeta{UpstreamModelName: "mapped-model"},
		GLM53OfficialCompatibilityActive: true,
	}
	body := []byte(`{"id":"msg_1","type":"message","model":"mapped-model","content":[{"type":"text","text":"answer<END>ignored","block_extension":"kept"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2},"upstream_extension":{"kept":true}}`)
	httpResp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}

	apiErr := HandleClaudeResponseData(c, info, &ClaudeResponseInfo{}, httpResp, body)
	require.Nil(t, apiErr)
	assert.Equal(t, "answer", gjson.Get(recorder.Body.String(), "content.0.text").String())
	assert.Equal(t, "kept", gjson.Get(recorder.Body.String(), "content.0.block_extension").String())
	assert.True(t, gjson.Get(recorder.Body.String(), "upstream_extension.kept").Bool())
	assert.Equal(t, "end_turn", gjson.Get(recorder.Body.String(), "stop_reason").String())
	assert.Equal(t, gjson.Null, gjson.Get(recorder.Body.String(), "stop_sequence").Type)
}

func TestKimiK3TNTConversionPreservesExtendedReasoningEfforts(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:       constant.ChannelTypeAnthropic,
		UpstreamModelName: "kimi-k3",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			TNTTencentOpenAIConversion:  true,
			KimiK3OfficialCompatibility: true,
		},
	}}
	info.ActivateKimiK3OfficialCompatibility()
	adaptor := &Adaptor{}
	maxTokens := uint(64)

	for _, effort := range []string{"medium", "ultra", "xhigh", "custom", "none"} {
		t.Run(effort, func(t *testing.T) {
			claudeRequest := &dto.ClaudeRequest{
				Model:        "kimi-k3",
				MaxTokens:    &maxTokens,
				OutputConfig: []byte(`{"effort":"` + effort + `"}`),
			}
			convertedClaude, err := adaptor.ConvertClaudeRequest(nil, info, claudeRequest)
			require.NoError(t, err)
			chatFromClaude := convertedClaude.(*dto.GeneralOpenAIRequest)
			wantEffort := effort
			if effort == "none" {
				wantEffort = "low"
			}
			assert.Equal(t, wantEffort, chatFromClaude.ReasoningEffort)

			responsesRequest := dto.OpenAIResponsesRequest{
				Model:           "kimi-k3",
				Input:           []byte(`"hello"`),
				MaxOutputTokens: &maxTokens,
				Reasoning:       &dto.Reasoning{Effort: effort},
			}
			convertedResponses, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, responsesRequest)
			require.NoError(t, err)
			chatFromResponses := convertedResponses.(*dto.GeneralOpenAIRequest)
			assert.Equal(t, wantEffort, chatFromResponses.ReasoningEffort)

			require.NotNil(t, chatFromClaude.Temperature)
			assert.Equal(t, 1.0, *chatFromClaude.Temperature)
			require.NotNil(t, chatFromResponses.Temperature)
			assert.Equal(t, 1.0, *chatFromResponses.Temperature)
		})
	}
}

func TestKimiK3OfficialReasoningEffortSurvivesProtocolConversion(t *testing.T) {
	maxTokens := uint(64)
	tests := []struct {
		effort     string
		wantEffort string
	}{
		{effort: "medium", wantEffort: "medium"},
		{effort: "ultra", wantEffort: "ultra"},
		{effort: "xhigh", wantEffort: "xhigh"},
		{effort: "custom", wantEffort: "custom"},
		{effort: "none", wantEffort: "low"},
	}

	for _, test := range tests {
		t.Run(test.effort, func(t *testing.T) {
			openAIInfo := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:       constant.ChannelTypeOpenAI,
				UpstreamModelName: "kimi-k3",
				ChannelOtherSettings: dto.ChannelOtherSettings{
					KimiK3OfficialCompatibility: true,
				},
			}}
			openAIInfo.ActivateKimiK3OfficialCompatibility()

			anthropicRequest := &dto.ClaudeRequest{
				Model:        "kimi-k3",
				MaxTokens:    &maxTokens,
				OutputConfig: []byte(`{"effort":"` + test.effort + `"}`),
			}
			require.NoError(t, relayconvert.NormalizeKimiK3ClaudeRequest(anthropicRequest))
			convertedAnthropic, err := (&openaiadapter.Adaptor{}).ConvertClaudeRequest(nil, openAIInfo, anthropicRequest)
			require.NoError(t, err)
			chatFromAnthropic := convertedAnthropic.(*dto.GeneralOpenAIRequest)
			assert.Equal(t, test.wantEffort, chatFromAnthropic.ReasoningEffort)
			require.NotNil(t, chatFromAnthropic.Temperature)
			assert.Equal(t, 1.0, *chatFromAnthropic.Temperature)

			claudeInfo := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:       constant.ChannelTypeAnthropic,
				UpstreamModelName: "kimi-k3",
				ChannelOtherSettings: dto.ChannelOtherSettings{
					KimiK3OfficialCompatibility: true,
				},
			}}
			claudeInfo.ActivateKimiK3OfficialCompatibility()

			chatRequest := &dto.GeneralOpenAIRequest{
				Model:           "kimi-k3",
				MaxTokens:       &maxTokens,
				ReasoningEffort: test.effort,
			}
			require.NoError(t, relayconvert.NormalizeKimiK3ChatRequest(chatRequest))
			convertedChat, err := (&Adaptor{}).ConvertOpenAIRequest(nil, claudeInfo, chatRequest)
			require.NoError(t, err)
			claudeFromChat := convertedChat.(*dto.ClaudeRequest)
			assert.Equal(t, test.wantEffort, claudeFromChat.GetEfforts())
			require.NotNil(t, claudeFromChat.Temperature)
			assert.Equal(t, 1.0, *claudeFromChat.Temperature)
			if test.effort == "none" {
				require.NotNil(t, claudeFromChat.Thinking)
				assert.Equal(t, "enabled", claudeFromChat.Thinking.Type)
			}

			responsesRequest := dto.OpenAIResponsesRequest{
				Model:           "kimi-k3",
				Input:           []byte(`"hello"`),
				MaxOutputTokens: &maxTokens,
				Reasoning:       &dto.Reasoning{Effort: test.effort},
			}
			require.NoError(t, relayconvert.NormalizeKimiK3ResponsesRequest(&responsesRequest))
			convertedResponses, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, claudeInfo, responsesRequest)
			require.NoError(t, err)
			claudeFromResponses := convertedResponses.(*dto.ClaudeRequest)
			assert.Equal(t, test.wantEffort, claudeFromResponses.GetEfforts())
			require.NotNil(t, claudeFromResponses.Temperature)
			assert.Equal(t, 1.0, *claudeFromResponses.Temperature)
			if test.effort == "none" {
				require.NotNil(t, claudeFromResponses.Thinking)
				assert.Equal(t, "enabled", claudeFromResponses.Thinking.Type)
			}
		})
	}
}

func TestKimiK3OpenAIChannelPreservesAnthropicResponseFormat(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:       constant.ChannelTypeOpenAI,
		UpstreamModelName: "kimi-k3",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			KimiK3OfficialCompatibility: true,
		},
	}}
	info.ActivateKimiK3OfficialCompatibility()
	maxTokens := uint(64)
	request := &dto.ClaudeRequest{
		Model:          "kimi-k3",
		MaxTokens:      &maxTokens,
		OutputConfig:   []byte(`{"effort":"high"}`),
		ResponseFormat: []byte(`{"type":"json_object"}`),
	}

	adaptor := openaiadapter.Adaptor{}
	converted, err := adaptor.ConvertClaudeRequest(nil, info, request)
	require.NoError(t, err)
	chatRequest := converted.(*dto.GeneralOpenAIRequest)
	require.NotNil(t, chatRequest.ResponseFormat)
	assert.Equal(t, "json_object", chatRequest.ResponseFormat.Type)
	assert.Equal(t, "high", chatRequest.ReasoningEffort)
}

func TestClaudeChannelDropsKimiResponseFormatWhenCompatibilityIsDisabled(t *testing.T) {
	request := &dto.ClaudeRequest{
		Model:          "claude-test",
		ResponseFormat: []byte(`{"type":"json_object"}`),
	}

	converted, err := (&Adaptor{}).ConvertClaudeRequest(nil, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}, request)
	require.NoError(t, err)
	assert.Empty(t, converted.(*dto.ClaudeRequest).ResponseFormat)
}

func TestDeepSeekV4TNTFinalRequestThinking(t *testing.T) {
	tests := []struct {
		name                 string
		compatibilityEnabled bool
		thinking             *dto.Thinking
		wantThinking         string
	}{
		{name: "default enables thinking", compatibilityEnabled: true, wantThinking: "enabled"},
		{name: "explicit disabled survives", compatibilityEnabled: true, thinking: &dto.Thinking{Type: "disabled"}, wantThinking: "disabled"},
		{name: "disabled compatibility injects nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				OriginModelName: "deepseek-v4-flash-0731",
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       constant.ChannelTypeAnthropic,
					UpstreamModelName: "deepseek-v4-flash",
					ChannelOtherSettings: dto.ChannelOtherSettings{
						TNTTencentOpenAIConversion:      true,
						DeepSeekV4OfficialCompatibility: tt.compatibilityEnabled,
					},
				},
			}
			info.ActivateDeepSeekV4OfficialCompatibility()
			request := &dto.ClaudeRequest{
				Model:    "deepseek-v4-flash",
				Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
				Thinking: tt.thinking,
			}

			converted, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, request)
			require.NoError(t, err)
			data, err := common.Marshal(converted)
			require.NoError(t, err)
			data, err = relayconvert.FinalizeTNTTencentChatRequestJSON(data)
			require.NoError(t, err)
			if info.IsDeepSeekV4OfficialCompatibility() {
				data, err = relayconvert.NormalizeDeepSeekV4RequestJSON(data, types.RelayFormatOpenAI)
				require.NoError(t, err)
			}

			thinkingType := gjson.GetBytes(data, "thinking.type")
			if tt.wantThinking == "" {
				assert.False(t, thinkingType.Exists())
				return
			}
			assert.Equal(t, tt.wantThinking, thinkingType.String())
		})
	}
}

func TestDeepSeekV4ResponsesRequestConvertsToAnthropic(t *testing.T) {
	maxOutputTokens := uint(64)
	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-pro-0813",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeAnthropic,
			UpstreamModelName: "deepseek-v4-pro",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				DeepSeekV4OfficialCompatibility: true,
			},
		},
	}
	info.ActivateDeepSeekV4OfficialCompatibility()
	request := dto.OpenAIResponsesRequest{
		Model:           "deepseek-v4-pro",
		Input:           []byte(`"hello"`),
		MaxOutputTokens: &maxOutputTokens,
		Reasoning:       &dto.Reasoning{Effort: "none"},
		Text:            []byte(`{"format":{"type":"json_object"}}`),
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
	require.NoError(t, err)
	claudeRequest := converted.(*dto.ClaudeRequest)
	require.NotNil(t, claudeRequest.Thinking)
	assert.Equal(t, "disabled", claudeRequest.Thinking.Type)
	assert.Empty(t, claudeRequest.OutputConfig)
	assert.Equal(t, "json_object", gjson.GetBytes(claudeRequest.ResponseFormat, "type").String())
}

func TestResponseOpenAI2ClaudeToolUseInputIsObject(t *testing.T) {
	tests := []struct {
		name string
		args string
		want map[string]interface{}
	}{
		{name: "object", args: `{"q":"x"}`, want: map[string]interface{}{"q": "x"}},
		{name: "empty", args: "", want: map[string]interface{}{}},
		{name: "invalid", args: "{", want: map[string]interface{}{}},
		{name: "null", args: "null", want: map[string]interface{}{}},
		{name: "array", args: `["x"]`, want: map[string]interface{}{}},
		{name: "string", args: `"x"`, want: map[string]interface{}{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := dto.Message{Role: "assistant"}
			msg.SetToolCalls([]dto.ToolCallRequest{
				{
					ID:   "call_1",
					Type: "function",
					Function: dto.FunctionRequest{
						Name:      "lookup",
						Arguments: tt.args,
					},
				},
			})
			resp := relayconvert.ResponseOpenAI2Claude(&dto.OpenAITextResponse{
				Id:    "chatcmpl_1",
				Model: "gpt-test",
				Choices: []dto.OpenAITextResponseChoice{
					{Message: msg, FinishReason: "tool_calls"},
				},
			}, nil)

			require.Len(t, resp.Content, 1)
			assert.Equal(t, "tool_use", resp.Content[0].Type)
			assert.Equal(t, tt.want, resp.Content[0].Input)
		})
	}
}

func TestFormatClaudeResponseInfo_MessageStart(t *testing.T) {
	claudeInfo := &ClaudeResponseInfo{
		Usage: &dto.Usage{},
	}
	claudeResponse := &dto.ClaudeResponse{
		Type: "message_start",
		Message: &dto.ClaudeMediaMessage{
			Id:    "msg_123",
			Model: "claude-3-5-sonnet",
			Usage: &dto.ClaudeUsage{
				InputTokens:              100,
				OutputTokens:             1,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     30,
			},
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	if claudeInfo.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", claudeInfo.Usage.PromptTokens)
	}
	if claudeInfo.Usage.PromptTokensDetails.CachedTokens != 30 {
		t.Errorf("CachedTokens = %d, want 30", claudeInfo.Usage.PromptTokensDetails.CachedTokens)
	}
	if claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens != 50 {
		t.Errorf("CachedCreationTokens = %d, want 50", claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
	}
	if claudeInfo.ResponseId != "msg_123" {
		t.Errorf("ResponseId = %s, want msg_123", claudeInfo.ResponseId)
	}
	if claudeInfo.Model != "claude-3-5-sonnet" {
		t.Errorf("Model = %s, want claude-3-5-sonnet", claudeInfo.Model)
	}
}

func TestFormatClaudeResponseInfo_MessageDelta_FullUsage(t *testing.T) {
	// message_start 先积累 usage
	claudeInfo := &ClaudeResponseInfo{
		Usage: &dto.Usage{
			PromptTokens: 100,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         30,
				CachedCreationTokens: 50,
			},
			CompletionTokens: 1,
		},
	}

	// message_delta 带完整 usage（原生 Anthropic 场景）
	claudeResponse := &dto.ClaudeResponse{
		Type: "message_delta",
		Usage: &dto.ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             200,
			CacheCreationInputTokens: 50,
			CacheReadInputTokens:     30,
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	if claudeInfo.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", claudeInfo.Usage.PromptTokens)
	}
	if claudeInfo.Usage.CompletionTokens != 200 {
		t.Errorf("CompletionTokens = %d, want 200", claudeInfo.Usage.CompletionTokens)
	}
	if claudeInfo.Usage.TotalTokens != 300 {
		t.Errorf("TotalTokens = %d, want 300", claudeInfo.Usage.TotalTokens)
	}
	if !claudeInfo.Done {
		t.Error("expected Done = true")
	}
}

func TestFormatClaudeResponseInfo_MessageDelta_OnlyOutputTokens(t *testing.T) {
	// 模拟 Bedrock: message_start 已积累 usage
	claudeInfo := &ClaudeResponseInfo{
		Usage: &dto.Usage{
			PromptTokens: 100,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         30,
				CachedCreationTokens: 50,
			},
			CompletionTokens:            1,
			ClaudeCacheCreation5mTokens: 10,
			ClaudeCacheCreation1hTokens: 20,
		},
	}

	// Bedrock 的 message_delta 只有 output_tokens，缺少 input_tokens 和 cache 字段
	claudeResponse := &dto.ClaudeResponse{
		Type: "message_delta",
		Usage: &dto.ClaudeUsage{
			OutputTokens: 200,
			// InputTokens, CacheCreationInputTokens, CacheReadInputTokens 都是 0
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	// PromptTokens 应保持 message_start 的值（因为 message_delta 的 InputTokens=0，不更新）
	if claudeInfo.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", claudeInfo.Usage.PromptTokens)
	}
	if claudeInfo.Usage.CompletionTokens != 200 {
		t.Errorf("CompletionTokens = %d, want 200", claudeInfo.Usage.CompletionTokens)
	}
	if claudeInfo.Usage.TotalTokens != 300 {
		t.Errorf("TotalTokens = %d, want 300", claudeInfo.Usage.TotalTokens)
	}
	// cache 字段应保持 message_start 的值
	if claudeInfo.Usage.PromptTokensDetails.CachedTokens != 30 {
		t.Errorf("CachedTokens = %d, want 30", claudeInfo.Usage.PromptTokensDetails.CachedTokens)
	}
	if claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens != 50 {
		t.Errorf("CachedCreationTokens = %d, want 50", claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
	}
	if claudeInfo.Usage.ClaudeCacheCreation5mTokens != 10 {
		t.Errorf("ClaudeCacheCreation5mTokens = %d, want 10", claudeInfo.Usage.ClaudeCacheCreation5mTokens)
	}
	if claudeInfo.Usage.ClaudeCacheCreation1hTokens != 20 {
		t.Errorf("ClaudeCacheCreation1hTokens = %d, want 20", claudeInfo.Usage.ClaudeCacheCreation1hTokens)
	}
	if !claudeInfo.Done {
		t.Error("expected Done = true")
	}
}

func TestFormatClaudeResponseInfo_NilClaudeInfo(t *testing.T) {
	claudeResponse := &dto.ClaudeResponse{Type: "message_start"}
	ok := FormatClaudeResponseInfo(claudeResponse, nil, nil)
	if ok {
		t.Error("expected false for nil claudeInfo")
	}
}

func TestFormatClaudeResponseInfo_ContentBlockDelta(t *testing.T) {
	text := "hello"
	claudeInfo := &ClaudeResponseInfo{
		Usage:        &dto.Usage{},
		ResponseText: strings.Builder{},
	}
	claudeResponse := &dto.ClaudeResponse{
		Type: "content_block_delta",
		Delta: &dto.ClaudeMediaMessage{
			Text: &text,
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	if claudeInfo.ResponseText.String() != "hello" {
		t.Errorf("ResponseText = %q, want %q", claudeInfo.ResponseText.String(), "hello")
	}
}

func TestBuildOpenAIStyleUsageFromClaudeUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         30,
			CachedCreationTokens: 50,
		},
		ClaudeCacheCreation5mTokens: 10,
		ClaudeCacheCreation1hTokens: 20,
		UsageSemantic:               "anthropic",
	}

	openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(usage)

	if openAIUsage.PromptTokens != 180 {
		t.Fatalf("PromptTokens = %d, want 180", openAIUsage.PromptTokens)
	}
	if openAIUsage.InputTokens != 180 {
		t.Fatalf("InputTokens = %d, want 180", openAIUsage.InputTokens)
	}
	if openAIUsage.TotalTokens != 200 {
		t.Fatalf("TotalTokens = %d, want 200", openAIUsage.TotalTokens)
	}
	if openAIUsage.UsageSemantic != "openai" {
		t.Fatalf("UsageSemantic = %s, want openai", openAIUsage.UsageSemantic)
	}
	if openAIUsage.UsageSource != "anthropic" {
		t.Fatalf("UsageSource = %s, want anthropic", openAIUsage.UsageSource)
	}
}

func TestBuildOpenAIStyleUsageFromClaudeUsagePreservesCacheCreationRemainder(t *testing.T) {
	tests := []struct {
		name                    string
		cachedCreationTokens    int
		cacheCreationTokens5m   int
		cacheCreationTokens1h   int
		expectedTotalInputToken int
	}{
		{
			name:                    "prefers aggregate when it includes remainder",
			cachedCreationTokens:    50,
			cacheCreationTokens5m:   10,
			cacheCreationTokens1h:   20,
			expectedTotalInputToken: 180,
		},
		{
			name:                    "falls back to split tokens when aggregate missing",
			cachedCreationTokens:    0,
			cacheCreationTokens5m:   10,
			cacheCreationTokens1h:   20,
			expectedTotalInputToken: 160,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens:     100,
				CompletionTokens: 20,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens:         30,
					CachedCreationTokens: tt.cachedCreationTokens,
				},
				ClaudeCacheCreation5mTokens: tt.cacheCreationTokens5m,
				ClaudeCacheCreation1hTokens: tt.cacheCreationTokens1h,
				UsageSemantic:               "anthropic",
			}

			openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(usage)

			if openAIUsage.PromptTokens != tt.expectedTotalInputToken {
				t.Fatalf("PromptTokens = %d, want %d", openAIUsage.PromptTokens, tt.expectedTotalInputToken)
			}
			if openAIUsage.InputTokens != tt.expectedTotalInputToken {
				t.Fatalf("InputTokens = %d, want %d", openAIUsage.InputTokens, tt.expectedTotalInputToken)
			}
		})
	}
}

func TestBuildOpenAIStyleUsageFromClaudeUsageDefaultsAggregateCacheCreationTo5m(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         30,
			CachedCreationTokens: 50,
		},
		UsageSemantic: "anthropic",
	}

	openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(usage)

	require.Equal(t, 50, openAIUsage.ClaudeCacheCreation5mTokens)
	require.Equal(t, 0, openAIUsage.ClaudeCacheCreation1hTokens)
}

func TestOpenAIChatRequestToClaudeMessages_ClaudeOpus48HighUsesAdaptiveThinking(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model:       "claude-opus-4-8-high",
		Temperature: commonPointer(0.7),
		TopP:        commonPointer(0.9),
		TopK:        commonPointer(40),
		Messages: []dto.Message{
			{
				Role:    "user",
				Content: "hello",
			},
		},
	}

	claudeRequest, err := relayconvert.OpenAIChatRequestToClaudeMessages(nil, &relaycommon.RelayInfo{}, request)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8", claudeRequest.Model)
	require.NotNil(t, claudeRequest.Thinking)
	require.Equal(t, "adaptive", claudeRequest.Thinking.Type)
	require.Equal(t, "summarized", claudeRequest.Thinking.Display)
	require.JSONEq(t, `{"effort":"high"}`, string(claudeRequest.OutputConfig))
	require.Nil(t, claudeRequest.Temperature)
	require.Nil(t, claudeRequest.TopP)
	require.Nil(t, claudeRequest.TopK)
}

func TestOpenAIChatRequestToClaudeMessages_ClaudeOpus48ThinkingUsesAdaptiveHighEffort(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model:       "claude-opus-4-8-thinking",
		Temperature: commonPointer(0.7),
		TopP:        commonPointer(0.9),
		TopK:        commonPointer(40),
		Messages: []dto.Message{
			{
				Role:    "user",
				Content: "hello",
			},
		},
	}

	claudeRequest, err := relayconvert.OpenAIChatRequestToClaudeMessages(nil, &relaycommon.RelayInfo{}, request)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8", claudeRequest.Model)
	require.NotNil(t, claudeRequest.Thinking)
	require.Equal(t, "adaptive", claudeRequest.Thinking.Type)
	require.Equal(t, "summarized", claudeRequest.Thinking.Display)
	require.JSONEq(t, `{"effort":"high"}`, string(claudeRequest.OutputConfig))
	require.Nil(t, claudeRequest.Temperature)
	require.Nil(t, claudeRequest.TopP)
	require.Nil(t, claudeRequest.TopK)
}

func TestOpenAIChatRequestToClaudeMessages_NoneDisablesThinking(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantModel string
	}{
		{name: "plain model", model: "claude-sonnet-4-5", wantModel: "claude-sonnet-4-5"},
		{name: "thinking suffix", model: "claude-opus-4-8-thinking", wantModel: "claude-opus-4-8"},
		{name: "effort suffix", model: "claude-opus-4-8-high", wantModel: "claude-opus-4-8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := dto.GeneralOpenAIRequest{
				Model:           tt.model,
				MaxTokens:       commonPointer(uint(1024)),
				ReasoningEffort: "none",
				Reasoning:       []byte(`{"enabled":true,"max_tokens":2048}`),
				Temperature:     commonPointer(0.7),
				TopP:            commonPointer(0.9),
				TopK:            commonPointer(40),
				Messages: []dto.Message{
					{
						Role:    "user",
						Content: "hello",
					},
				},
			}

			claudeRequest, err := relayconvert.OpenAIChatRequestToClaudeMessages(nil, &relaycommon.RelayInfo{}, request)
			require.NoError(t, err)
			assert.Equal(t, tt.wantModel, claudeRequest.Model)
			require.NotNil(t, claudeRequest.Thinking)
			assert.Equal(t, "disabled", claudeRequest.Thinking.Type)
			assert.Nil(t, claudeRequest.Thinking.BudgetTokens)
			assert.Empty(t, claudeRequest.Thinking.Display)
			assert.Empty(t, claudeRequest.OutputConfig)
			require.NotNil(t, claudeRequest.Temperature)
			assert.Equal(t, 0.7, *claudeRequest.Temperature)
			require.NotNil(t, claudeRequest.TopP)
			assert.Equal(t, 0.9, *claudeRequest.TopP)
			require.NotNil(t, claudeRequest.TopK)
			assert.Equal(t, 40, *claudeRequest.TopK)

			requestJSON, err := common.Marshal(claudeRequest)
			require.NoError(t, err)
			assert.JSONEq(t, `{"type":"disabled"}`, gjson.GetBytes(requestJSON, "thinking").Raw)
			assert.False(t, gjson.GetBytes(requestJSON, "output_config").Exists())
		})
	}
}
