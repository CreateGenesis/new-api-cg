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
		{name: "effort", request: &dto.GeneralOpenAIRequest{Model: "kimi-k3", ReasoningEffort: "ultra"}, want: "reasoning_effort"},
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
