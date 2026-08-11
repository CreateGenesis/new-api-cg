package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertTNTTencentClaudeRequestNormalizesToolHistory(t *testing.T) {
	zeroTokens := uint(0)
	zeroFloat := 0.0
	zeroTopK := 0
	stream := false
	toolText := "Run Anthropic security testing"
	request := &dto.ClaudeRequest{
		Model:       "kimi-k3",
		System:      "Claude Code\nx-anthropic-test: remove me\nkeep this",
		MaxTokens:   &zeroTokens,
		Temperature: &zeroFloat,
		TopP:        &zeroFloat,
		TopK:        &zeroTopK,
		Stream:      &stream,
		Messages: []dto.ClaudeMessage{
			{
				Role: "assistant",
				Content: []dto.ClaudeMediaMessage{
					{Type: "text", Text: &toolText},
					{Type: "tool_use", Id: "call_missing", Name: "first", Input: map[string]any{"q": "Anthropic"}},
					{Type: "tool_use", Id: "call_done", Name: "second", Input: map[string]any{"q": "ok"}},
				},
			},
			{
				Role: "user",
				Content: []dto.ClaudeMediaMessage{
					{Type: "text", Text: commonPointer("continue")},
					{Type: "tool_result", ToolUseId: "call_done", Content: "Anthropic result"},
				},
			},
		},
		Tools: []dto.Tool{{
			Name:        "lookup",
			Description: "Anthropic security review",
			InputSchema: map[string]any{
				"type":        "object",
				"description": "Claude Code schema",
			},
		}},
	}

	converted, err := ConvertTNTTencentClaudeRequest(request)
	require.NoError(t, err)
	require.NotNil(t, converted.Stream)
	assert.True(t, *converted.Stream)
	assert.Same(t, request.MaxTokens, converted.MaxTokens)
	assert.Same(t, request.Temperature, converted.Temperature)
	assert.Same(t, request.TopP, converted.TopP)
	assert.Same(t, request.TopK, converted.TopK)

	require.Len(t, converted.Messages, 6)
	assert.Equal(t, "system", converted.Messages[0].Role)
	assert.Equal(t, "AI Assistant\nkeep this", converted.Messages[0].StringContent())
	assert.Equal(t, "assistant", converted.Messages[1].Role)
	assert.Equal(t, "Run Provider testing", converted.Messages[1].StringContent())
	assert.Equal(t, "assistant", converted.Messages[2].Role)
	assert.Nil(t, converted.Messages[2].Content)
	require.Len(t, converted.Messages[2].ParseToolCalls(), 2)
	assert.Equal(t, dto.Message{Role: "tool", ToolCallId: "call_missing", Content: "(interrupted)"}, converted.Messages[3])
	assert.Equal(t, "tool", converted.Messages[4].Role)
	assert.Equal(t, "call_done", converted.Messages[4].ToolCallId)
	assert.Equal(t, "Provider result", converted.Messages[4].StringContent())
	assert.Equal(t, dto.Message{Role: "user", Content: "continue"}, converted.Messages[5])

	require.Len(t, converted.Tools, 1)
	assert.Equal(t, "Provider code review", converted.Tools[0].Function.Description)
	schema := converted.Tools[0].Function.Parameters.(map[string]any)
	assert.Equal(t, "AI Assistant schema", schema["description"])

	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, common.Unmarshal(encoded, &body))
	assert.Contains(t, body, "max_tokens")
	assert.Contains(t, body, "temperature")
	assert.Contains(t, body, "top_p")
	assert.Contains(t, body, "top_k")
}

func TestConvertTNTTencentResponsesRequestForcesStreamingAndFlattensInput(t *testing.T) {
	zeroTokens := uint(0)
	zeroFloat := 0.0
	stream := false
	request := &dto.OpenAIResponsesRequest{
		Model:              "kimi-k3",
		Instructions:       tntRawMessage(t, "Codex CLI from Anthropic"),
		MaxOutputTokens:    &zeroTokens,
		Temperature:        &zeroFloat,
		TopP:               &zeroFloat,
		Stream:             &stream,
		Conversation:       tntRawMessage(t, "conv_ignored"),
		PreviousResponseID: "resp_ignored",
		Input: tntRawMessage(t, []map[string]any{
			{
				"role":    "developer",
				"content": []map[string]any{{"type": "input_text", "text": "Claude Code rules"}},
			},
			{
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": "calling"}},
			},
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": map[string]any{"q": "Anthropic"},
			},
		}),
		Tools: tntRawMessage(t, []map[string]any{
			{
				"type":        "function",
				"name":        "lookup",
				"description": "security testing",
				"parameters":  map[string]any{"type": "object", "description": "Anthropic schema"},
			},
			{"type": "web_search_preview"},
		}),
	}

	converted, err := ConvertTNTTencentResponsesRequest(request)
	require.NoError(t, err)
	require.NotNil(t, converted.Stream)
	assert.True(t, *converted.Stream)
	assert.Same(t, request.MaxOutputTokens, converted.MaxTokens)
	assert.Nil(t, converted.MaxCompletionTokens)
	assert.Same(t, request.Temperature, converted.Temperature)
	assert.Same(t, request.TopP, converted.TopP)

	require.Len(t, converted.Messages, 5)
	assert.Equal(t, dto.Message{Role: "system", Content: "Code Assistant from Provider"}, converted.Messages[0])
	assert.Equal(t, dto.Message{Role: "system", Content: "AI Assistant rules"}, converted.Messages[1])
	assert.Equal(t, dto.Message{Role: "assistant", Content: "calling"}, converted.Messages[2])
	assert.Equal(t, "assistant", converted.Messages[3].Role)
	assert.Nil(t, converted.Messages[3].Content)
	assert.Equal(t, dto.Message{Role: "tool", ToolCallId: "call_1", Content: "(interrupted)"}, converted.Messages[4])
	require.Len(t, converted.Tools, 1)
	assert.Equal(t, "testing", converted.Tools[0].Function.Description)
	assert.Equal(t, "Provider schema", converted.Tools[0].Function.Parameters.(map[string]any)["description"])

	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, common.Unmarshal(encoded, &body))
	assert.Contains(t, body, "max_tokens")
	assert.NotContains(t, body, "max_completion_tokens")
	assert.Contains(t, body, "temperature")
	assert.Contains(t, body, "top_p")
}

func TestFinalizeTNTTencentChatRequestJSONForcesStreamAndSanitizesSchemas(t *testing.T) {
	body := tntRawMessage(t, map[string]any{
		"model":       "kimi-k3",
		"stream":      false,
		"temperature": 0,
		"messages": []map[string]any{{
			"role":    "user",
			"content": "Claude Code via api.anthropic.com\nkeep",
		}},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "answer",
				"schema": map[string]any{
					"type":        "object",
					"description": "Anthropic security review",
				},
			},
		},
	})

	finalized, err := FinalizeTNTTencentChatRequestJSON(body)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, common.Unmarshal(finalized, &got))
	assert.Equal(t, true, got["stream"])
	assert.Equal(t, float64(0), got["temperature"])
	assert.Equal(t, "keep", got["messages"].([]any)[0].(map[string]any)["content"])
	responseFormat := got["response_format"].(map[string]any)
	jsonSchema := responseFormat["json_schema"].(map[string]any)
	schema := jsonSchema["schema"].(map[string]any)
	assert.Equal(t, "Provider code review", schema["description"])
}

func commonPointer[T any](value T) *T {
	return &value
}

func tntRawMessage(t *testing.T, value any) []byte {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return data
}
