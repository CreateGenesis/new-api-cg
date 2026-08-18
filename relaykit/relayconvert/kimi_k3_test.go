package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeKimiK3ChatRequestAppliesOfficialDefaults(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "kimi-k3"}

	require.NoError(t, NormalizeKimiK3ChatRequest(request))
	require.NotNil(t, request.MaxTokens)
	assert.Equal(t, uint(131072), *request.MaxTokens)
	require.NotNil(t, request.Temperature)
	assert.Equal(t, 1.0, *request.Temperature)
	require.NotNil(t, request.TopP)
	assert.Equal(t, 0.95, *request.TopP)
	require.NotNil(t, request.N)
	assert.Equal(t, 1, *request.N)
	assert.Equal(t, "max", request.ReasoningEffort)
}

func TestNormalizeKimiK3ChatRequestRejectsUnsupportedOfficialValues(t *testing.T) {
	tests := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		want    string
	}{
		{name: "temperature", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: kitutil.GetPointer(0.2)}, want: "temperature"},
		{name: "top p", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", TopP: kitutil.GetPointer(0.1)}, want: "top_p"},
		{name: "choice count", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", N: kitutil.GetPointer(2)}, want: "n must be 1"},
		{name: "penalty", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", PresencePenalty: kitutil.GetPointer(0.1)}, want: "presence_penalty"},
		{name: "named tool", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}}, want: "named function"},
		{name: "incomplete JSON schema", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", ResponseFormat: &dto.ResponseFormat{Type: "json_schema", JsonSchema: []byte(`{"name":"answer"}`)}}, want: "requires name and schema"},
		{
			name: "remote image",
			request: &dto.GeneralOpenAIRequest{
				Model: "kimi-k3",
				Messages: []dto.Message{{
					Role: "user",
					Content: []any{map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "https://example.com/a.png"},
					}},
				}},
			},
			want: "base64 data URL",
		},
		{
			name: "remote video",
			request: &dto.GeneralOpenAIRequest{
				Model: "kimi-k3",
				Messages: []dto.Message{{
					Role: "user",
					Content: []any{map[string]any{
						"type":      "video_url",
						"video_url": "https://example.com/a.mp4",
					}},
				}},
			},
			want: "ms://",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := NormalizeKimiK3ChatRequest(test.request)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestNormalizeKimiK3ReasoningEffortsMatchOfficialAPI(t *testing.T) {
	tests := []struct {
		effort     string
		wantEffort string
	}{
		{effort: "low", wantEffort: "low"},
		{effort: "medium", wantEffort: "medium"},
		{effort: "high", wantEffort: "high"},
		{effort: "max", wantEffort: "max"},
		{effort: "ultra", wantEffort: "ultra"},
		{effort: "xhigh", wantEffort: "xhigh"},
		{effort: "invalid_probe", wantEffort: "invalid_probe"},
		{effort: "none", wantEffort: "low"},
	}

	for _, test := range tests {
		t.Run(test.effort, func(t *testing.T) {
			chatRequest := &dto.GeneralOpenAIRequest{Model: "kimi-k3", ReasoningEffort: test.effort}
			require.NoError(t, NormalizeKimiK3ChatRequest(chatRequest))
			assert.Equal(t, test.wantEffort, chatRequest.ReasoningEffort)
			require.NotNil(t, chatRequest.Temperature)
			assert.Equal(t, 1.0, *chatRequest.Temperature)

			responsesRequest := &dto.OpenAIResponsesRequest{
				Model:     "kimi-k3",
				Input:     []byte(`"hello"`),
				Reasoning: &dto.Reasoning{Effort: test.effort},
			}
			require.NoError(t, NormalizeKimiK3ResponsesRequest(responsesRequest))
			assert.Equal(t, test.wantEffort, responsesRequest.Reasoning.Effort)
			require.NotNil(t, responsesRequest.Temperature)
			assert.Equal(t, 1.0, *responsesRequest.Temperature)

			claudeRequest := &dto.ClaudeRequest{
				Model:        "kimi-k3",
				OutputConfig: []byte(`{"effort":"` + test.effort + `"}`),
			}
			require.NoError(t, NormalizeKimiK3ClaudeRequest(claudeRequest))
			assert.Equal(t, test.wantEffort, claudeRequest.GetEfforts())
			assert.Nil(t, claudeRequest.Temperature)
		})
	}
}

func TestNormalizeKimiK3ThinkingDisabledUsesLowUpstreamThinking(t *testing.T) {
	chatRequest := &dto.GeneralOpenAIRequest{
		Model:           "kimi-k3",
		ReasoningEffort: "low",
		THINKING:        []byte(`{"type":"disabled"}`),
		Temperature:     kitutil.GetPointer(0.6),
	}
	require.NoError(t, NormalizeKimiK3ChatRequest(chatRequest))
	assert.Equal(t, "low", chatRequest.ReasoningEffort)
	assert.Empty(t, chatRequest.THINKING)
	require.NotNil(t, chatRequest.Temperature)
	assert.Equal(t, 1.0, *chatRequest.Temperature)

	claudeRequest := &dto.ClaudeRequest{
		Model:        "kimi-k3",
		OutputConfig: []byte(`{"effort":"low"}`),
		Thinking:     &dto.Thinking{Type: "disabled"},
	}
	require.NoError(t, NormalizeKimiK3ClaudeRequest(claudeRequest))
	assert.Equal(t, "low", claudeRequest.GetEfforts())
	assert.Nil(t, claudeRequest.Thinking)
	assert.Nil(t, claudeRequest.Temperature)
}

func TestNormalizeKimiK3ClaudeSamplingMatchesOfficialRanges(t *testing.T) {
	valid := &dto.ClaudeRequest{
		Model:       "kimi-k3",
		Temperature: kitutil.GetPointer(0.6),
		TopP:        kitutil.GetPointer(1.0),
		TopK:        kitutil.GetPointer(-1),
	}
	require.NoError(t, NormalizeKimiK3ClaudeRequest(valid))
	assert.Equal(t, 0.6, *valid.Temperature)
	assert.Equal(t, 1.0, *valid.TopP)
	assert.Equal(t, -1, *valid.TopK)

	invalidTemperature := &dto.ClaudeRequest{Model: "kimi-k3", Temperature: kitutil.GetPointer(1.1)}
	require.ErrorContains(t, NormalizeKimiK3ClaudeRequest(invalidTemperature), "temperature")

	invalidTopP := &dto.ClaudeRequest{Model: "kimi-k3", TopP: kitutil.GetPointer(1.1)}
	require.ErrorContains(t, NormalizeKimiK3ClaudeRequest(invalidTopP), "top_p")
}

func TestNormalizeKimiK3StopLimitsUTF8Bytes(t *testing.T) {
	valid := &dto.GeneralOpenAIRequest{Model: "kimi-k3", Stop: []string{"a", "二"}}
	require.NoError(t, NormalizeKimiK3ChatRequest(valid))
	assert.Equal(t, "a", valid.Stop)

	tooMany := &dto.GeneralOpenAIRequest{Model: "kimi-k3", Stop: []string{"1", "2", "3", "4", "5", "6"}}
	require.ErrorContains(t, NormalizeKimiK3ChatRequest(tooMany), "at most 5")

	tooLong := &dto.GeneralOpenAIRequest{Model: "kimi-k3", Stop: "中文中文中文中文中文中文中文中文中文中文中"}
	require.ErrorContains(t, NormalizeKimiK3ChatRequest(tooLong), "32 UTF-8 bytes")
}

func TestNormalizeKimiK3ChatRequestConvertsDecodedStopArrayToString(t *testing.T) {
	var original dto.GeneralOpenAIRequest
	require.NoError(t, kitutil.Unmarshal([]byte(`{
		"model":"kimi-k3",
		"messages":[{"role":"user","content":"reply with ALPHA-ZXQSTOP731-OMEGA"}],
		"stop":["ZXQSTOP731"]
	}`), &original))

	originalJSON, err := kitutil.Marshal(&original)
	require.NoError(t, err)
	var request dto.GeneralOpenAIRequest
	require.NoError(t, kitutil.Unmarshal(originalJSON, &request))
	require.NoError(t, err)
	require.NoError(t, NormalizeKimiK3ChatRequest(&request))
	assert.Equal(t, "ZXQSTOP731", request.Stop)
	upstreamJSON, err := kitutil.Marshal(&request)
	require.NoError(t, err)
	var upstream map[string]any
	require.NoError(t, kitutil.Unmarshal(upstreamJSON, &upstream))
	assert.Equal(t, "ZXQSTOP731", upstream["stop"])
	assert.Equal(t, []string{"ZXQSTOP731"}, KimiK3StopSequencesFromRequest(&original))

	response := &dto.OpenAITextResponse{
		Choices: []dto.OpenAITextResponseChoice{{
			Message: dto.Message{Content: "ALPHA-ZXQSTOP731-OMEGA"},
		}},
	}
	matched := ApplyKimiK3StopToChatResponse(response, KimiK3StopSequencesFromRequest(&original))
	assert.Equal(t, "ZXQSTOP731", matched)
	assert.Equal(t, "ALPHA-", response.Choices[0].Message.StringContent())
	assert.Equal(t, "stop", response.Choices[0].FinishReason)
}

func TestNormalizeKimiK3ResponsesRequestPreservesExplicitMetadata(t *testing.T) {
	maxTokens := uint(64)
	request := &dto.OpenAIResponsesRequest{
		Model:           "kimi-k3",
		Input:           []byte(`"hello"`),
		MaxOutputTokens: &maxTokens,
		Metadata:        []byte(`{"trace":"x"}`),
		Reasoning:       &dto.Reasoning{Effort: "high"},
	}

	require.NoError(t, NormalizeKimiK3ResponsesRequest(request))
	assert.Equal(t, maxTokens, *request.MaxOutputTokens)
	assert.JSONEq(t, `{"trace":"x"}`, string(request.Metadata))
	assert.Equal(t, "high", request.Reasoning.Effort)
}

func TestNormalizeKimiK3ResponsesRequestValidatesJSONSchema(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{
		Model: "kimi-k3",
		Input: []byte(`"hello"`),
		Text:  []byte(`{"format":{"type":"json_schema","name":"answer"}}`),
	}

	require.ErrorContains(t, NormalizeKimiK3ResponsesRequest(request), "requires name and schema")
}

func TestNormalizeKimiK3ToolChoiceNoneRemovesToolsAcrossFormats(t *testing.T) {
	chatRequest := &dto.GeneralOpenAIRequest{
		Model:      "kimi-k3",
		ToolChoice: "none",
		Tools: []dto.ToolCallRequest{{
			Type: "function",
			Function: dto.FunctionRequest{
				Name:       "get_weather",
				Parameters: map[string]any{"type": "object"},
			},
		}},
	}
	require.NoError(t, NormalizeKimiK3ChatRequest(chatRequest))
	assert.Empty(t, chatRequest.Tools)
	assert.Equal(t, "none", chatRequest.ToolChoice)
	claudeUpstream, err := OpenAIChatRequestToClaudeMessages(nil, nil, *chatRequest)
	require.NoError(t, err)
	assert.Empty(t, claudeUpstream.Tools)
	claudeToolChoice, err := kitutil.Any2Type[dto.ClaudeToolChoice](claudeUpstream.ToolChoice)
	require.NoError(t, err)
	assert.Equal(t, "none", claudeToolChoice.Type)

	responsesRequest := &dto.OpenAIResponsesRequest{
		Model:      "kimi-k3",
		Input:      []byte(`"weather"`),
		ToolChoice: []byte(`"none"`),
		Tools: []byte(`[{
			"type":"function",
			"name":"get_weather",
			"parameters":{"type":"object"}
		}]`),
	}
	require.NoError(t, NormalizeKimiK3ResponsesRequest(responsesRequest))
	assert.Empty(t, responsesRequest.Tools)
	assert.JSONEq(t, `"none"`, string(responsesRequest.ToolChoice))
	tntResponsesUpstream, err := ConvertTNTTencentResponsesRequest(responsesRequest)
	require.NoError(t, err)
	assert.Empty(t, tntResponsesUpstream.Tools)
	assert.Equal(t, "none", tntResponsesUpstream.ToolChoice)

	claudeRequest := &dto.ClaudeRequest{
		Model: "kimi-k3",
		Tools: []dto.Tool{{
			Name:        "get_weather",
			InputSchema: map[string]any{"type": "object"},
		}},
		ToolChoice: map[string]any{"type": "none"},
	}
	require.NoError(t, NormalizeKimiK3ClaudeRequest(claudeRequest))
	assert.Nil(t, claudeRequest.Tools)
	require.NotNil(t, claudeRequest.ToolChoice)
	openAIUpstream, err := ClaudeMessagesRequestToOpenAIChat(*claudeRequest, nil)
	require.NoError(t, err)
	assert.Empty(t, openAIUpstream.Tools)
	tntChatUpstream, err := ConvertTNTTencentClaudeRequest(claudeRequest)
	require.NoError(t, err)
	assert.Empty(t, tntChatUpstream.Tools)
	assert.Equal(t, "none", tntChatUpstream.ToolChoice)
}

func TestNormalizeKimiK3ToolChoiceAutoPreservesTools(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:      "kimi-k3",
		ToolChoice: "auto",
		Tools: []dto.ToolCallRequest{{
			Type:     "function",
			Function: dto.FunctionRequest{Name: "get_weather"},
		}},
	}

	require.NoError(t, NormalizeKimiK3ChatRequest(request))
	assert.Len(t, request.Tools, 1)
}

func TestHideKimiK3ReasoningUsageUpdatesAllOpenAIUsageFields(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     27,
		InputTokens:      27,
		CompletionTokens: 40,
		OutputTokens:     40,
		TotalTokens:      67,
	}
	usage.CompletionTokenDetails.ReasoningTokens = 25
	usage.BillingUsage = dto.NewOpenAIChatBillingUsage(usage)

	HideKimiK3ReasoningUsage(usage)

	assert.Equal(t, 15, usage.CompletionTokens)
	assert.Equal(t, 15, usage.OutputTokens)
	assert.Equal(t, 42, usage.TotalTokens)
	assert.Zero(t, usage.CompletionTokenDetails.ReasoningTokens)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 15, usage.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Equal(t, 15, usage.BillingUsage.OpenAIUsage.OutputTokens)
	assert.Equal(t, 42, usage.BillingUsage.OpenAIUsage.TotalTokens)
	assert.Zero(t, usage.BillingUsage.OpenAIUsage.CompletionTokenDetails.ReasoningTokens)
}

func TestNormalizeKimiK3ClaudeRequestAppliesOfficialReasoningAndValidatesOutputFormat(t *testing.T) {
	request := &dto.ClaudeRequest{
		Model: "kimi-k3",
		OutputFormat: []byte(`{
			"type":"json_schema",
			"json_schema":{"name":"answer","strict":true,"schema":{"type":"object"}}
		}`),
	}

	require.NoError(t, NormalizeKimiK3ClaudeRequest(request))
	assert.Equal(t, "max", request.GetEfforts())
	assert.Empty(t, request.OutputFormat)
	assert.JSONEq(t, `{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object"}}}`, string(request.ResponseFormat))

	invalid := &dto.ClaudeRequest{
		Model:        "kimi-k3",
		OutputFormat: []byte(`{"type":"json_schema","json_schema":{"name":"answer"}}`),
	}
	require.ErrorContains(t, NormalizeKimiK3ClaudeRequest(invalid), "requires name and schema")

	conflicting := &dto.ClaudeRequest{
		Model:          "kimi-k3",
		ResponseFormat: []byte(`{"type":"json_object"}`),
		OutputFormat:   []byte(`{"type":"json_object"}`),
	}
	require.ErrorContains(t, NormalizeKimiK3ClaudeRequest(conflicting), "cannot both be set")
}
