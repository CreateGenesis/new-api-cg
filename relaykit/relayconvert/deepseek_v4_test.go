package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDeepSeekV4ChatRequestThinkingPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		thinking     []byte
		effort       string
		wantThinking string
	}{
		{name: "default enables thinking", wantThinking: "enabled"},
		{name: "none alone disables thinking", effort: "none", wantThinking: "disabled"},
		{name: "explicit enabled wins over none", thinking: []byte(`{"type":"enabled"}`), effort: "none", wantThinking: "enabled"},
		{name: "explicit disabled wins over max", thinking: []byte(`{"type":"disabled"}`), effort: "max", wantThinking: "disabled"},
		{name: "adaptive is preserved", thinking: []byte(`{"type":"adaptive"}`), wantThinking: "adaptive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{ReasoningEffort: tt.effort, THINKING: tt.thinking}
			require.NoError(t, NormalizeDeepSeekV4ChatRequest(request))

			var thinking dto.Thinking
			require.NoError(t, kitutil.Unmarshal(request.THINKING, &thinking))
			assert.Equal(t, tt.wantThinking, thinking.Type)
		})
	}
}

func TestNormalizeDeepSeekV4ChatRequestMatchesObservedParameterBoundaries(t *testing.T) {
	maxTokens := uint(393216)
	ignoredMaxCompletionTokens := uint(999999)
	topK := 1000
	temperature := 2.0
	topP := 0.0001
	n := 1
	stream := true
	request := &dto.GeneralOpenAIRequest{
		Messages:            []dto.Message{{Role: "user", Content: "return json"}},
		MaxTokens:           &maxTokens,
		MaxCompletionTokens: &ignoredMaxCompletionTokens,
		TopK:                &topK,
		Temperature:         &temperature,
		TopP:                &topP,
		N:                   &n,
		Stream:              &stream,
		StreamOptions:       &dto.StreamOptions{IncludeUsage: true},
		Stop:                []any{"", "END"},
		ResponseFormat:      &dto.ResponseFormat{Type: "json_object"},
	}

	require.NoError(t, NormalizeDeepSeekV4ChatRequest(request))
	assert.Nil(t, request.MaxCompletionTokens)
	assert.Equal(t, topK, *request.TopK)
	assert.Equal(t, []any{"", "END"}, request.Stop)
}

func TestNormalizeDeepSeekV4ChatRequestRejectsObservedInvalidValues(t *testing.T) {
	zero := uint(0)
	over := uint(393217)
	zeroTopP := 0.0
	overTemperature := 2.1
	nTwo := 2
	stream := false
	topLogProbs := 1
	topLogProbsOver := 21
	logProbs := true
	penaltyOver := 2.1

	tests := []struct {
		name    string
		request *dto.GeneralOpenAIRequest
		wantErr string
	}{
		{name: "zero max tokens", request: &dto.GeneralOpenAIRequest{MaxTokens: &zero}, wantErr: "max_tokens"},
		{name: "over max tokens", request: &dto.GeneralOpenAIRequest{MaxTokens: &over}, wantErr: "max_tokens"},
		{name: "zero top p", request: &dto.GeneralOpenAIRequest{TopP: &zeroTopP}, wantErr: "top_p"},
		{name: "temperature over two", request: &dto.GeneralOpenAIRequest{Temperature: &overTemperature}, wantErr: "temperature"},
		{name: "n other than one", request: &dto.GeneralOpenAIRequest{N: &nTwo}, wantErr: "n must be 1"},
		{name: "stream options without stream", request: &dto.GeneralOpenAIRequest{Stream: &stream, StreamOptions: &dto.StreamOptions{}}, wantErr: "stream_options"},
		{name: "top logprobs without logprobs", request: &dto.GeneralOpenAIRequest{TopLogProbs: &topLogProbs}, wantErr: "logprobs"},
		{name: "top logprobs over limit", request: &dto.GeneralOpenAIRequest{TopLogProbs: &topLogProbsOver, LogProbs: &logProbs}, wantErr: "top_logprobs"},
		{name: "presence penalty over limit", request: &dto.GeneralOpenAIRequest{PresencePenalty: &penaltyOver}, wantErr: "presence_penalty"},
		{name: "empty thinking", request: &dto.GeneralOpenAIRequest{THINKING: []byte(`{}`)}, wantErr: "thinking.type"},
		{name: "invalid effort", request: &dto.GeneralOpenAIRequest{ReasoningEffort: "bogus"}, wantErr: "reasoning effort"},
		{name: "too many stops", request: &dto.GeneralOpenAIRequest{Stop: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17"}}, wantErr: "at most 16"},
		{name: "empty response format", request: &dto.GeneralOpenAIRequest{ResponseFormat: &dto.ResponseFormat{}}, wantErr: "response_format.type"},
		{name: "json schema unavailable", request: &dto.GeneralOpenAIRequest{ResponseFormat: &dto.ResponseFormat{Type: "json_schema"}}, wantErr: "unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NormalizeDeepSeekV4ChatRequest(tt.request)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNormalizeDeepSeekV4ResponsesRequest(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{}
	require.NoError(t, NormalizeDeepSeekV4ResponsesRequest(request))
	require.NotNil(t, request.Reasoning)
	assert.Empty(t, request.Reasoning.Effort)

	zero := uint(0)
	err := NormalizeDeepSeekV4ResponsesRequest(&dto.OpenAIResponsesRequest{MaxOutputTokens: &zero})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_output_tokens")

	request = &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "none"}}
	require.NoError(t, NormalizeDeepSeekV4ResponsesRequest(request))
	assert.Equal(t, "none", request.Reasoning.Effort)
}

func TestNormalizeDeepSeekV4ClaudeRequestMatchesObservedDifferences(t *testing.T) {
	largeMaxTokens := uint(400000)
	request := &dto.ClaudeRequest{MaxTokens: &largeMaxTokens}
	require.NoError(t, NormalizeDeepSeekV4ClaudeRequest(request))
	require.NotNil(t, request.Thinking)
	assert.Equal(t, "enabled", request.Thinking.Type)

	request = &dto.ClaudeRequest{OutputConfig: []byte(`{"effort":"none"}`)}
	err := NormalizeDeepSeekV4ClaudeRequest(request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reasoning effort")

	request = &dto.ClaudeRequest{StopSequences: make([]string, 17)}
	err = NormalizeDeepSeekV4ClaudeRequest(request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most 16")
}

func TestDeepSeekV4TNTClaudeControlsSurviveConversion(t *testing.T) {
	source := &dto.ClaudeRequest{
		Model:    "deepseek-v4-flash",
		Thinking: &dto.Thinking{Type: "disabled"},
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "OK"}},
	}
	target, err := ConvertTNTTencentClaudeRequest(source)
	require.NoError(t, err)
	require.NoError(t, ApplyDeepSeekV4ClaudeControlsToChat(source, target))
	require.NoError(t, NormalizeDeepSeekV4ChatRequest(target))

	var thinking dto.Thinking
	require.NoError(t, kitutil.Unmarshal(target.THINKING, &thinking))
	assert.Equal(t, "disabled", thinking.Type)
}
