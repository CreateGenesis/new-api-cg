package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiK3ChatStreamStopFilterMatchesAcrossChunks(t *testing.T) {
	filter := NewKimiK3ChatStreamStopFilter([]string{"<STOP>"})
	first := chatTextChunk("answer<ST", "reasoning<STOP>", `{"value":"<STOP>"}`, "")
	second := chatTextChunk("OP>ignored", "", "", "length")

	filter.Filter(first)
	filter.Filter(second)

	assert.Equal(t, "answer", first.Choices[0].Delta.GetContentString())
	assert.Equal(t, "reasoning<STOP>", first.Choices[0].Delta.GetReasoningContent())
	require.Len(t, first.Choices[0].Delta.ToolCalls, 1)
	assert.Equal(t, `{"value":"<STOP>"}`, first.Choices[0].Delta.ToolCalls[0].Function.Arguments)
	assert.Equal(t, "", second.Choices[0].Delta.GetContentString())
	require.NotNil(t, second.Choices[0].FinishReason)
	assert.Equal(t, "stop", *second.Choices[0].FinishReason)
}

func TestKimiK3ChatStreamStopFilterFlushesUnmatchedPrefix(t *testing.T) {
	filter := NewKimiK3ChatStreamStopFilter([]string{"<STOP>"})
	first := chatTextChunk("answer<ST", "", "", "")
	second := chatTextChunk("", "", "", "length")

	filter.Filter(first)
	filter.Filter(second)

	assert.Equal(t, "answer", first.Choices[0].Delta.GetContentString())
	assert.Equal(t, "<ST", second.Choices[0].Delta.GetContentString())
	require.NotNil(t, second.Choices[0].FinishReason)
	assert.Equal(t, "length", *second.Choices[0].FinishReason)
}

func TestApplyKimiK3StopToClaudeResponseOnlyTruncatesText(t *testing.T) {
	response := &dto.ClaudeResponse{
		StopReason: "end_turn",
		Content: []dto.ClaudeMediaMessage{
			{Type: "thinking", Thinking: common.GetPointer("reasoning<STOP>")},
			{Type: "text", Text: common.GetPointer("answer<STOP>ignored")},
			{Type: "tool_use", Input: map[string]any{"value": "<STOP>"}},
		},
	}

	ApplyKimiK3StopToClaudeResponse(response, []string{"<STOP>"})

	assert.Equal(t, "reasoning<STOP>", *response.Content[0].Thinking)
	assert.Equal(t, "answer", response.Content[1].GetText())
	assert.Equal(t, map[string]any{"value": "<STOP>"}, response.Content[2].Input)
	assert.Equal(t, "stop_sequence", response.StopReason)
	require.NotNil(t, response.StopSequence)
	assert.Equal(t, "<STOP>", *response.StopSequence)
}

func TestKimiK3ClaudeStreamStopFilterMatchesAcrossTextDeltas(t *testing.T) {
	filter := NewKimiK3ClaudeStreamStopFilter([]string{"END"})
	first := claudeTextDelta(0, "answerE")
	second := claudeTextDelta(0, "NDignored")
	stopReason := "end_turn"
	terminal := &dto.ClaudeResponse{
		Type: "message_delta",
		Delta: &dto.ClaudeMediaMessage{
			StopReason:  &stopReason,
			PartialJson: common.GetPointer(`{"value":"END"}`),
		},
	}

	firstResults := filter.Filter(first)
	secondResults := filter.Filter(second)
	terminalResults := filter.Filter(terminal)

	require.Len(t, firstResults, 1)
	assert.Equal(t, "answer", firstResults[0].Delta.GetText())
	require.Len(t, secondResults, 1)
	assert.Equal(t, "", secondResults[0].Delta.GetText())
	require.Len(t, terminalResults, 1)
	require.NotNil(t, terminalResults[0].Delta.StopReason)
	assert.Equal(t, "stop_sequence", *terminalResults[0].Delta.StopReason)
	assert.Equal(t, `{"value":"END"}`, *terminalResults[0].Delta.PartialJson)
}

func chatTextChunk(content, reasoning, arguments, finishReason string) *dto.ChatCompletionsStreamResponse {
	choice := dto.ChatCompletionsStreamResponseChoice{Index: 0}
	choice.Delta.SetContentString(content)
	if reasoning != "" {
		choice.Delta.ReasoningContent = &reasoning
	}
	if arguments != "" {
		choice.Delta.ToolCalls = []dto.ToolCallResponse{{Function: dto.FunctionResponse{Arguments: arguments}}}
	}
	if finishReason != "" {
		choice.FinishReason = &finishReason
	}
	return &dto.ChatCompletionsStreamResponse{Choices: []dto.ChatCompletionsStreamResponseChoice{choice}}
}

func claudeTextDelta(index int, text string) *dto.ClaudeResponse {
	return &dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: &index,
		Delta: &dto.ClaudeMediaMessage{Type: "text_delta", Text: &text},
	}
}
