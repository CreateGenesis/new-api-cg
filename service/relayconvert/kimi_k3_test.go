package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
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
		{name: "temperature", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", Temperature: common.GetPointer(0.2)}, want: "temperature"},
		{name: "top p", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", TopP: common.GetPointer(0.1)}, want: "top_p"},
		{name: "choice count", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", N: common.GetPointer(2)}, want: "n must be 1"},
		{name: "penalty", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", PresencePenalty: common.GetPointer(0.1)}, want: "presence_penalty"},
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
		effort          string
		wantTemperature float64
	}{
		{effort: "low", wantTemperature: 1.0},
		{effort: "medium", wantTemperature: 1.0},
		{effort: "high", wantTemperature: 1.0},
		{effort: "max", wantTemperature: 1.0},
		{effort: "ultra", wantTemperature: 1.0},
		{effort: "xhigh", wantTemperature: 1.0},
		{effort: "invalid_probe", wantTemperature: 1.0},
		{effort: "none", wantTemperature: 0.6},
	}

	for _, test := range tests {
		t.Run(test.effort, func(t *testing.T) {
			chatRequest := &dto.GeneralOpenAIRequest{Model: "kimi-k3", ReasoningEffort: test.effort}
			require.NoError(t, NormalizeKimiK3ChatRequest(chatRequest))
			assert.Equal(t, test.effort, chatRequest.ReasoningEffort)
			require.NotNil(t, chatRequest.Temperature)
			assert.Equal(t, test.wantTemperature, *chatRequest.Temperature)

			responsesRequest := &dto.OpenAIResponsesRequest{
				Model:     "kimi-k3",
				Input:     []byte(`"hello"`),
				Reasoning: &dto.Reasoning{Effort: test.effort},
			}
			require.NoError(t, NormalizeKimiK3ResponsesRequest(responsesRequest))
			assert.Equal(t, test.effort, responsesRequest.Reasoning.Effort)
			require.NotNil(t, responsesRequest.Temperature)
			assert.Equal(t, test.wantTemperature, *responsesRequest.Temperature)

			claudeRequest := &dto.ClaudeRequest{
				Model:        "kimi-k3",
				OutputConfig: []byte(`{"effort":"` + test.effort + `"}`),
			}
			require.NoError(t, NormalizeKimiK3ClaudeRequest(claudeRequest))
			assert.Equal(t, test.effort, claudeRequest.GetEfforts())
			assert.Nil(t, claudeRequest.Temperature)
		})
	}
}

func TestNormalizeKimiK3ThinkingDisabledUsesOfficialNonThinkingSampling(t *testing.T) {
	chatRequest := &dto.GeneralOpenAIRequest{
		Model:           "kimi-k3",
		ReasoningEffort: "low",
		THINKING:        []byte(`{"type":"disabled"}`),
	}
	require.NoError(t, NormalizeKimiK3ChatRequest(chatRequest))
	require.NotNil(t, chatRequest.Temperature)
	assert.Equal(t, 0.6, *chatRequest.Temperature)

	claudeRequest := &dto.ClaudeRequest{
		Model:        "kimi-k3",
		OutputConfig: []byte(`{"effort":"low"}`),
		Thinking:     &dto.Thinking{Type: "disabled"},
	}
	require.NoError(t, NormalizeKimiK3ClaudeRequest(claudeRequest))
	assert.Nil(t, claudeRequest.Temperature)
}

func TestNormalizeKimiK3ClaudeSamplingMatchesOfficialRanges(t *testing.T) {
	valid := &dto.ClaudeRequest{
		Model:       "kimi-k3",
		Temperature: common.GetPointer(0.6),
		TopP:        common.GetPointer(1.0),
		TopK:        common.GetPointer(-1),
	}
	require.NoError(t, NormalizeKimiK3ClaudeRequest(valid))
	assert.Equal(t, 0.6, *valid.Temperature)
	assert.Equal(t, 1.0, *valid.TopP)
	assert.Equal(t, -1, *valid.TopK)

	invalidTemperature := &dto.ClaudeRequest{Model: "kimi-k3", Temperature: common.GetPointer(1.1)}
	require.ErrorContains(t, NormalizeKimiK3ClaudeRequest(invalidTemperature), "temperature")

	invalidTopP := &dto.ClaudeRequest{Model: "kimi-k3", TopP: common.GetPointer(1.1)}
	require.ErrorContains(t, NormalizeKimiK3ClaudeRequest(invalidTopP), "top_p")
}

func TestNormalizeKimiK3StopLimitsUTF8Bytes(t *testing.T) {
	valid := &dto.GeneralOpenAIRequest{Model: "kimi-k3", Stop: []string{"a", "二"}}
	require.NoError(t, NormalizeKimiK3ChatRequest(valid))

	tooMany := &dto.GeneralOpenAIRequest{Model: "kimi-k3", Stop: []string{"1", "2", "3", "4", "5", "6"}}
	require.ErrorContains(t, NormalizeKimiK3ChatRequest(tooMany), "at most 5")

	tooLong := &dto.GeneralOpenAIRequest{Model: "kimi-k3", Stop: "中文中文中文中文中文中文中文中文中文中文中"}
	require.ErrorContains(t, NormalizeKimiK3ChatRequest(tooLong), "32 UTF-8 bytes")
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
