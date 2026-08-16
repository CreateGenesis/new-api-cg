package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeGLM53ChatRequestOfficialFields(t *testing.T) {
	maxTokens := uint(131072)
	request := &dto.GeneralOpenAIRequest{
		Model:     "mapped-model",
		MaxTokens: &maxTokens,
		Stop:      []any{"<END>"},
	}

	require.NoError(t, NormalizeGLM53ChatRequest(request))
	assert.Equal(t, "max", request.ReasoningEffort)
	assert.Equal(t, []string{"<END>"}, request.Stop)
}

func TestNormalizeGLM53ChatRequestRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
	}{
		{name: "boolean thinking", request: &dto.GeneralOpenAIRequest{THINKING: []byte(`true`)}},
		{name: "object enable thinking", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`{"type":"enabled"}`)}},
		{name: "unsupported none effort", request: &dto.GeneralOpenAIRequest{ReasoningEffort: "none"}},
		{name: "unsupported effort", request: &dto.GeneralOpenAIRequest{ReasoningEffort: "medium"}},
		{name: "whitespace effort", request: &dto.GeneralOpenAIRequest{ReasoningEffort: " high "}},
		{name: "string stop", request: &dto.GeneralOpenAIRequest{Stop: "<END>"}},
		{name: "empty string stop", request: &dto.GeneralOpenAIRequest{Stop: ""}},
		{name: "too many stops", request: &dto.GeneralOpenAIRequest{Stop: []any{"one", "two", "three", "four", "five"}}},
		{name: "null stop item", request: &dto.GeneralOpenAIRequest{Stop: []any{nil}}},
		{name: "zero max tokens", request: &dto.GeneralOpenAIRequest{MaxTokens: uintPointer(0)}},
		{name: "oversized max tokens", request: &dto.GeneralOpenAIRequest{MaxTokens: uintPointer(131073)}},
		{name: "zero top k", request: &dto.GeneralOpenAIRequest{TopK: intPointer(0)}},
		{name: "negative top p", request: &dto.GeneralOpenAIRequest{TopP: floatPointer(-0.1)}},
		{name: "oversized top p", request: &dto.GeneralOpenAIRequest{TopP: floatPointer(1.1)}},
		{name: "empty tool", request: &dto.GeneralOpenAIRequest{Tools: []dto.ToolCallRequest{{}}}},
		{name: "boolean tool choice", request: &dto.GeneralOpenAIRequest{ToolChoice: true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, NormalizeGLM53ChatRequest(test.request))
		})
	}
}

func TestNormalizeGLM53ThinkingCompatibilityCanonicalizesClientForms(t *testing.T) {
	tests := []struct {
		name       string
		request    *dto.GeneralOpenAIRequest
		wantEffort string
	}{
		{name: "missing", request: &dto.GeneralOpenAIRequest{}, wantEffort: "max"},
		{name: "null", request: &dto.GeneralOpenAIRequest{THINKING: []byte(`null`), EnableThinking: []byte(`null`)}, wantEffort: "max"},
		{name: "empty object", request: &dto.GeneralOpenAIRequest{THINKING: []byte(`{}`)}, wantEffort: "max"},
		{name: "adaptive high", request: &dto.GeneralOpenAIRequest{THINKING: []byte(`{"type":"adaptive"}`), ReasoningEffort: "high"}, wantEffort: "high"},
		{name: "disabled", request: &dto.GeneralOpenAIRequest{THINKING: []byte(`{"type":"disabled"}`), ReasoningEffort: "high"}, wantEffort: "low"},
		{name: "false", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`false`)}, wantEffort: "low"},
		{name: "positive integer", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`2`)}, wantEffort: "max"},
		{name: "negative integer", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`-1`)}, wantEffort: "max"},
		{name: "empty string", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`""`)}, wantEffort: "max"},
		{name: "whitespace string", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`" "`)}, wantEffort: "max"},
		{name: "null string", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`"null"`)}, wantEffort: "max"},
		{name: "true string", request: &dto.GeneralOpenAIRequest{EnableThinking: []byte(`"TRUE"`)}, wantEffort: "max"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, NormalizeGLM53ChatRequest(test.request))
			assert.JSONEq(t, `{"type":"enabled"}`, string(test.request.THINKING))
			assert.Nil(t, test.request.EnableThinking)
			assert.Equal(t, test.wantEffort, test.request.ReasoningEffort)
		})
	}

	for _, raw := range []string{`0`, `1.0`, `"false"`, `"1"`, `{}`, `[]`} {
		request := &dto.GeneralOpenAIRequest{EnableThinking: []byte(raw)}
		require.Error(t, NormalizeGLM53ChatRequest(request), raw)
	}
}

func TestNormalizeGLM53OptionalStopAndResponseFormatAcceptEmptyValues(t *testing.T) {
	chatStops := []struct {
		name string
		stop any
	}{
		{name: "nil", stop: nil},
		{name: "empty array", stop: []any{}},
	}
	for _, test := range chatStops {
		t.Run("chat "+test.name, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{
				Stop:           test.stop,
				ResponseFormat: &dto.ResponseFormat{},
			}
			require.NoError(t, NormalizeGLM53ChatRequest(request))
			assert.Nil(t, request.Stop)
			assert.Nil(t, request.ResponseFormat)
		})
	}

	for _, test := range []struct {
		name string
		stop any
		want []string
	}{
		{name: "empty string item", stop: []any{""}, want: []string{""}},
		{name: "multiple items", stop: []any{"ONE", "TWO"}, want: []string{"ONE", "TWO"}},
		{name: "primitive items", stop: []any{123.0, 1.5, true, false}, want: []string{"123", "1.5", "true", "false"}},
	} {
		t.Run("chat "+test.name, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{Stop: test.stop}
			require.NoError(t, NormalizeGLM53ChatRequest(request))
			assert.Equal(t, test.want, request.Stop)
		})
	}

	claudeStops := []struct {
		name string
		stop []string
	}{
		{name: "typed nil", stop: nil},
		{name: "empty array", stop: []string{}},
	}
	for _, test := range claudeStops {
		t.Run("claude "+test.name, func(t *testing.T) {
			request := &dto.ClaudeRequest{
				StopSequences:  test.stop,
				ResponseFormat: []byte(`{}`),
				Thinking:       &dto.Thinking{Type: "enabled"},
			}
			require.NoError(t, NormalizeGLM53ClaudeRequest(request))
			assert.Empty(t, request.StopSequences)
			assert.Nil(t, request.ResponseFormat)
		})
	}

	claude := &dto.ClaudeRequest{
		Thinking:       &dto.Thinking{Type: "enabled"},
		StopSequences:  []string{"", "END"},
		ResponseFormat: []byte(`"text"`),
	}
	require.NoError(t, NormalizeGLM53ClaudeRequest(claude))
	assert.Equal(t, []string{"", "END"}, claude.StopSequences)
	assert.JSONEq(t, `"text"`, string(claude.ResponseFormat))
}

func TestNormalizeGLM53ResponsesAndClaudeRequests(t *testing.T) {
	responses := &dto.OpenAIResponsesRequest{Model: "mapped-model", Input: []byte(`"hello"`)}
	require.NoError(t, NormalizeGLM53ResponsesRequest(responses))
	require.NotNil(t, responses.Reasoning)
	assert.Equal(t, "max", responses.Reasoning.Effort)

	claude := &dto.ClaudeRequest{
		Model:         "mapped-model",
		MaxTokens:     uintPointer(64),
		StopSequences: []string{"<END>"},
		Thinking:      &dto.Thinking{Type: "enabled"},
		OutputConfig:  []byte(`{"effort":"high","format":{"type":"json_object"}}`),
	}
	require.NoError(t, NormalizeGLM53ClaudeRequest(claude))
	assert.Equal(t, "high", claude.GetEfforts())
	assert.Equal(t, []string{"<END>"}, claude.StopSequences)
	assert.JSONEq(t, `{"effort":"high","format":{"type":"json_object"}}`, string(claude.OutputConfig))

	responses.Reasoning.Effort = "none"
	require.Error(t, NormalizeGLM53ResponsesRequest(responses))
	claude.Thinking = &dto.Thinking{Type: "disabled"}
	require.NoError(t, NormalizeGLM53ClaudeRequest(claude))
	assert.Equal(t, "low", claude.GetEfforts())
	claude.OutputConfig = []byte(`{"effort":"none"}`)
	require.Error(t, NormalizeGLM53ClaudeRequest(claude))
}

func TestNormalizeGLM53ClaudeCodeAdaptiveRequest(t *testing.T) {
	stream := true
	budget := 4096
	request := &dto.ClaudeRequest{
		Model:             "glm-5.3",
		Stream:            &stream,
		MaxTokens:         uintPointer(4096),
		Thinking:          &dto.Thinking{Type: "adaptive", BudgetTokens: &budget, Display: "summarized"},
		OutputConfig:      []byte(`{"effort":"high"}`),
		ContextManagement: []byte(`{"edits":[{"type":"clear_tool_uses_20250919"}]}`),
		Tools:             []any{map[string]any{"name": "lookup"}},
	}

	require.NoError(t, NormalizeGLM53ClaudeRequest(request))
	require.NotNil(t, request.Thinking)
	assert.Equal(t, "enabled", request.Thinking.Type)
	assert.Nil(t, request.Thinking.BudgetTokens)
	assert.Empty(t, request.Thinking.Display)
	assert.Equal(t, "high", request.GetEfforts())
	assert.JSONEq(t, `{"edits":[{"type":"clear_tool_uses_20250919"}]}`, string(request.ContextManagement))
	assert.True(t, *request.Stream)
}

func TestNormalizeGLM53ProtocolSpecificThinkingRules(t *testing.T) {
	for _, request := range []*dto.GeneralOpenAIRequest{
		{THINKING: []byte(`null`)},
		{THINKING: []byte(`{}`)},
		{THINKING: []byte(`{"type":"enabled"}`)},
		{EnableThinking: []byte(`true`)},
	} {
		require.NoError(t, NormalizeGLM53ChatRequest(request))
	}

	responses := &dto.OpenAIResponsesRequest{EnableThinking: []byte(`false`)}
	require.NoError(t, NormalizeGLM53ResponsesRequest(responses))
	assert.Nil(t, responses.EnableThinking)
	require.NotNil(t, responses.Reasoning)
	assert.Equal(t, "low", responses.Reasoning.Effort)

	for _, thinking := range []*dto.Thinking{nil, {}} {
		request := &dto.ClaudeRequest{Thinking: thinking}
		require.NoError(t, NormalizeGLM53ClaudeRequest(request))
		require.NotNil(t, request.Thinking)
		assert.Equal(t, "enabled", request.Thinking.Type)
	}

	for _, test := range []struct {
		name       string
		thinking   *dto.Thinking
		wantEffort string
	}{
		{name: "disabled", thinking: &dto.Thinking{Type: "disabled"}, wantEffort: "low"},
		{name: "adaptive", thinking: &dto.Thinking{Type: "adaptive"}, wantEffort: "max"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := &dto.ClaudeRequest{Thinking: test.thinking}
			require.NoError(t, NormalizeGLM53ClaudeRequest(request))
			assert.Equal(t, "enabled", request.Thinking.Type)
			assert.Equal(t, test.wantEffort, request.GetEfforts())
		})
	}

	for _, thinking := range []*dto.Thinking{{Type: "ENABLED"}, {Type: " enabled "}} {
		request := &dto.ClaudeRequest{Thinking: thinking}
		require.ErrorContains(t, NormalizeGLM53ClaudeRequest(request), "thinking.type")
	}

	claude := &dto.ClaudeRequest{
		Thinking: &dto.Thinking{Type: "enabled"},
		TopK:     intPointer(0),
	}
	require.NoError(t, NormalizeGLM53ClaudeRequest(claude))

	responses.TopP = floatPointer(1.1)
	require.ErrorContains(t, NormalizeGLM53ResponsesRequest(responses), "top_p")
	claude.TopP = floatPointer(-0.1)
	require.ErrorContains(t, NormalizeGLM53ClaudeRequest(claude), "top_p")
	claude.TopP = nil
	claude.StopSequences = []string{"one", "two", "three", "four", "five"}
	require.ErrorContains(t, NormalizeGLM53ClaudeRequest(claude), "at most 4")
}

func TestNormalizeGLM53RequestJSONUsesFinalProtocol(t *testing.T) {
	chat, err := NormalizeGLM53RequestJSON(
		[]byte(`{"model":"mapped-model","messages":[],"stop":["<END>"]}`),
		types.RelayFormatOpenAI,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"mapped-model","reasoning_effort":"max","thinking":{"type":"enabled"},"stop":["<END>"]}`, string(chat))

	_, err = NormalizeGLM53RequestJSON(
		[]byte(`{"model":"mapped-model","messages":[],"reasoning_effort":"xhigh"}`),
		types.RelayFormatOpenAI,
	)
	require.Error(t, err)
}

func TestNormalizeGLM53RequestJSONUsesObservedScalarCoercions(t *testing.T) {
	chat, err := NormalizeGLM53RequestJSON(
		[]byte(`{"model":"glm-5.3","messages":[],"max_tokens":1.9,"max_completion_tokens":{"ignored":true},"top_k":1.9,"top_p":" 0.5 ","temperature":"","seed":1.9,"n":{},"frequency_penalty":"abc","presence_penalty":true,"parallel_tool_calls":[],"logprobs":"x","top_logprobs":{},"stream_options":true,"stop":[123,1.0,true,false]}`),
		types.RelayFormatOpenAI,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"glm-5.3","max_tokens":1,"top_k":1,"top_p":0.5,"seed":1,"reasoning_effort":"max","thinking":{"type":"enabled"},"stop":["123","1.0","true","false"]}`, string(chat))

	claude, err := NormalizeGLM53RequestJSON(
		[]byte(`{"model":"glm-5.3","messages":[],"max_tokens":true,"max_tokens_to_sample":{"ignored":true},"top_k":false,"top_p":true,"temperature":false,"thinking":{"type":"enabled"},"response_format":"text"}`),
		types.RelayFormatClaude,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"glm-5.3","max_tokens":1,"top_k":0,"top_p":1,"temperature":0,"thinking":{"type":"enabled"},"output_config":{"effort":"max"},"response_format":"text"}`, string(claude))

	chat, err = NormalizeGLM53RequestJSON(
		[]byte(`{"model":"glm-5.3","messages":[],"max_tokens":"null","top_k":" ","top_p":"null","temperature":"null"}`),
		types.RelayFormatOpenAI,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"glm-5.3","reasoning_effort":"max","thinking":{"type":"enabled"}}`, string(chat))
}

func TestNormalizeGLM53RequestJSONRejectsObservedInvalidShapes(t *testing.T) {
	tests := []struct {
		name   string
		format types.RelayFormat
		body   string
	}{
		{name: "chat boolean max tokens", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"max_tokens":true}`},
		{name: "chat zero top k", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"top_k":0}`},
		{name: "chat null stop item", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"stop":[null]}`},
		{name: "chat empty tool", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"tools":[{}]}`},
		{name: "chat scalar tool choice", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"tool_choice":true}`},
		{name: "chat seed over int32", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"seed":2147483648}`},
		{name: "chat fractional seed string", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"seed":"1.5"}`},
		{name: "chat floating stream", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"stream":1.5}`},
		{name: "chat numeric stream string", format: types.RelayFormatOpenAI, body: `{"model":"glm-5.3","messages":[],"stream":"1"}`},
		{name: "claude fractional max tokens", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"max_tokens":1.5}`},
		{name: "claude null stop item", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"stop_sequences":[null]}`},
		{name: "claude scalar tools", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"tools":{}}`},
		{name: "claude scalar tool choice", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"tool_choice":"auto"}`},
		{name: "claude scalar metadata", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"metadata":"x"}`},
		{name: "claude object mcp servers", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"mcp_servers":{}}`},
		{name: "claude temperature over one", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"temperature":1.1}`},
		{name: "claude stream negative", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"stream":-1}`},
		{name: "claude empty stream string", format: types.RelayFormatClaude, body: `{"model":"glm-5.3","messages":[],"stream":""}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeGLM53RequestJSON([]byte(test.body), test.format)
			require.Error(t, err)
		})
	}
}

func TestNormalizeGLM53RequestJSONUsesObservedStreamCoercions(t *testing.T) {
	chatValues := map[string]bool{
		`true`:    true,
		`false`:   false,
		`0`:       false,
		`1`:       true,
		`-1`:      true,
		`2`:       true,
		`"true"`:  true,
		`"TRUE"`:  true,
		`"false"`: false,
	}
	for raw, want := range chatValues {
		normalized, err := NormalizeGLM53RequestJSON([]byte(`{"model":"glm-5.3","messages":[],"stream":`+raw+`}`), types.RelayFormatOpenAI)
		require.NoError(t, err, raw)
		var request dto.GeneralOpenAIRequest
		require.NoError(t, kitutil.Unmarshal(normalized, &request))
		require.NotNil(t, request.Stream)
		assert.Equal(t, want, *request.Stream)
	}

	claudeValues := map[string]bool{
		`true`:    true,
		`false`:   false,
		`0`:       false,
		`1`:       true,
		`"true"`:  true,
		`"TRUE"`:  true,
		`"false"`: false,
		`"0"`:     false,
		`"1"`:     true,
	}
	for raw, want := range claudeValues {
		normalized, err := NormalizeGLM53RequestJSON([]byte(`{"model":"glm-5.3","messages":[],"stream":`+raw+`}`), types.RelayFormatClaude)
		require.NoError(t, err, raw)
		var request dto.ClaudeRequest
		require.NoError(t, kitutil.Unmarshal(normalized, &request))
		require.NotNil(t, request.Stream)
		assert.Equal(t, want, *request.Stream)
	}
}

func TestNormalizeGLM53ClaudeToolShapes(t *testing.T) {
	valid := &dto.ClaudeRequest{
		Tools:      []any{map[string]any{"name": "", "input_schema": map[string]any{}}},
		ToolChoice: map[string]any{},
		Metadata:   []byte(`{}`),
		McpServers: []byte(`[{"type":"url","name":"test","url":"https://example.com"}]`),
	}
	require.NoError(t, NormalizeGLM53ClaudeRequest(valid))

	invalid := []*dto.ClaudeRequest{
		{Tools: []any{nil}},
		{Tools: []any{map[string]any{}}},
		{Tools: []any{map[string]any{"name": "lookup", "input_schema": nil}}},
		{Tools: []any{map[string]any{"name": "lookup", "description": 1}}},
		{ToolChoice: "auto"},
		{Metadata: []byte(`[]`)},
		{McpServers: []byte(`{}`)},
		{McpServers: []byte(`[null]`)},
		{McpServers: []byte(`[{}]`)},
		{McpServers: []byte(`[{"type":"url","name":"","url":""}]`)},
	}
	for _, request := range invalid {
		require.Error(t, NormalizeGLM53ClaudeRequest(request))
	}
}

func TestNormalizeGLM53ChatToolShapesUseObservedResults(t *testing.T) {
	validBodies := []string{
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{"name":"","parameters":null,"description":null}}]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"code_interpreter","function":{"name":"ci"}}]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"auto"}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"none"}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"required"}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":{"type":"function","function":{"name":"lookup"},"extra":1}}`,
	}
	for _, body := range validBodies {
		_, err := NormalizeGLM53RequestJSON([]byte(body), types.RelayFormatOpenAI)
		require.NoError(t, err, body)
	}

	invalidBodies := []string{
		`{"model":"glm-5.3","messages":[],"tools":[null]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{}]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function"}]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":null}]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{}}]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{"name":"lookup","parameters":"x"}}]}`,
		`{"model":"glm-5.3","messages":[],"tools":[{"type":"function","function":{"name":"lookup","description":1}}]}`,
		`{"model":"glm-5.3","messages":[],"tool_choice":""}`,
		`{"model":"glm-5.3","messages":[],"tool_choice":"any"}`,
		`{"model":"glm-5.3","messages":[],"tool_choice":{}}`,
		`{"model":"glm-5.3","messages":[],"tool_choice":{"type":"function"}}`,
		`{"model":"glm-5.3","messages":[],"tool_choice":{"type":"function","function":{"name":""}}}`,
		`{"model":"glm-5.3","messages":[],"tool_choice":{"type":"code_interpreter"}}`,
	}
	for _, body := range invalidBodies {
		_, err := NormalizeGLM53RequestJSON([]byte(body), types.RelayFormatOpenAI)
		require.Error(t, err, body)
	}
}

func TestNormalizeGLM53RequestJSONOmitsEmptyOptionalFields(t *testing.T) {
	chat, err := NormalizeGLM53RequestJSON(
		[]byte(`{"model":"mapped-model","messages":[],"stop":[],"response_format":{}}`),
		types.RelayFormatOpenAI,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"mapped-model","reasoning_effort":"max","thinking":{"type":"enabled"}}`, string(chat))

	claude, err := NormalizeGLM53RequestJSON(
		[]byte(`{"model":"mapped-model","messages":[],"thinking":{"type":"enabled"},"stop_sequences":[],"response_format":{}}`),
		types.RelayFormatClaude,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"mapped-model","thinking":{"type":"enabled"},"output_config":{"effort":"max"}}`, string(claude))

	claude, err = NormalizeGLM53RequestJSON(
		[]byte(`{"model":"mapped-model","messages":[],"thinking":null}`),
		types.RelayFormatClaude,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"mapped-model","thinking":{"type":"enabled"},"output_config":{"effort":"max"}}`, string(claude))

	claude, err = NormalizeGLM53RequestJSON(
		[]byte(`{"model":"mapped-model","messages":[],"thinking":{},"output_config":null,"response_format":null}`),
		types.RelayFormatClaude,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"mapped-model","thinking":{"type":"enabled"},"output_config":{"effort":"max"}}`, string(claude))
}

func TestNormalizeGLM53RequestJSONIsIdempotent(t *testing.T) {
	first, err := NormalizeGLM53RequestJSON(
		[]byte(`{"model":"glm-5.3","messages":[],"thinking":{"type":"adaptive","budget_tokens":4096,"display":"summarized"},"output_config":{"effort":"high"}}`),
		types.RelayFormatClaude,
	)
	require.NoError(t, err)
	second, err := NormalizeGLM53RequestJSON(first, types.RelayFormatClaude)
	require.NoError(t, err)
	assert.JSONEq(t, string(first), string(second))
}

func TestGLM53ChatStreamStopFilterForwardsSafeContentWithBoundedPendingState(t *testing.T) {
	filter := NewChatStreamStopFilter([]string{"<STOP>"})
	require.NotNil(t, filter)

	first := chatTextChunk("answer<ST", "", "", "")
	filter.Filter(first)
	assert.Equal(t, "answer", first.Choices[0].Delta.GetContentString())
	require.Len(t, filter.matchers, 1)
	assert.LessOrEqual(t, len(filter.matchers[0].pending), len("<STOP>")-1)

	second := chatTextChunk("OP>ignored", "", "", "length")
	filter.Filter(second)
	assert.Empty(t, second.Choices[0].Delta.GetContentString())
	require.NotNil(t, second.Choices[0].FinishReason)
	assert.Equal(t, "stop", *second.Choices[0].FinishReason)
	assert.Equal(t, "<STOP>", filter.MatchedSequence())
}

func TestGLM53EmptyStopEndsChatBeforeReasoningWithoutBuffering(t *testing.T) {
	filter := NewGLM53ChatStreamStopFilter([]string{""})
	require.NotNil(t, filter)

	content := "answer"
	reasoning := "reasoning"
	first := chatTextChunk(content, reasoning, "", "")
	filter.Filter(first)
	assert.Empty(t, first.Choices[0].Delta.GetReasoningContent())
	assert.Empty(t, first.Choices[0].Delta.GetContentString())
	require.Len(t, filter.matchers, 1)
	assert.Empty(t, filter.matchers[0].pending)
	assert.False(t, filter.matchers[0].didMatch)
	require.Len(t, filter.reasoningMatcher, 1)
	assert.True(t, filter.reasoningMatcher[0].didMatch)

	terminal := chatTextChunk("", "", "", "length")
	filter.Filter(terminal)
	require.NotNil(t, terminal.Choices[0].FinishReason)
	assert.Equal(t, "stop", *terminal.Choices[0].FinishReason)
}

func TestGLM53ChatStreamStopFilterKeepsUnmatchedReasoningPrefixInReasoning(t *testing.T) {
	filter := NewGLM53ChatStreamStopFilter([]string{"<STOP>"})
	require.NotNil(t, filter)

	reasoning := "analysis<ST"
	first := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Index: 0,
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: &reasoning},
		}},
	}
	filter.Filter(first)
	assert.Equal(t, "analysis", first.Choices[0].Delta.GetReasoningContent())
	assert.Nil(t, first.Choices[0].Delta.Content)

	second := chatTextChunk("answer", "", "", "")
	filter.Filter(second)
	assert.Equal(t, "<ST", second.Choices[0].Delta.GetReasoningContent())
	assert.Equal(t, "answer", second.Choices[0].Delta.GetContentString())
	assert.Empty(t, filter.MatchedSequence())
}

func TestGLM53ClaudeStopKeepsOfficialEndTurnMetadata(t *testing.T) {
	reasoning := "reasoning<STOP>"
	text := "answer<STOP>ignored"
	response := &dto.ClaudeResponse{
		StopReason:   "end_turn",
		StopSequence: nil,
		Content: []dto.ClaudeMediaMessage{
			{Type: "thinking", Thinking: &reasoning},
			{Type: "text", Text: &text},
		},
	}

	matched, didMatch := ApplyGLM53StopToClaudeResponse(response, []string{"<STOP>"})
	assert.True(t, didMatch)
	assert.Equal(t, "<STOP>", matched)
	assert.Equal(t, "reasoning<STOP>", *response.Content[0].Thinking)
	assert.Equal(t, "answer", response.Content[1].GetText())
	assert.Equal(t, "end_turn", response.StopReason)
	assert.Nil(t, response.StopSequence)
}

func uintPointer(value uint) *uint {
	return &value
}

func floatPointer(value float64) *float64 {
	return &value
}

func intPointer(value int) *int {
	return &value
}
