package relayconvert

import "github.com/QuantumNous/new-api/dto"

func HideKimiK3ChatThinking(response *dto.OpenAITextResponse) {
	if response == nil {
		return
	}
	for index := range response.Choices {
		response.Choices[index].Message.ReasoningContent = nil
		response.Choices[index].Message.Reasoning = nil
	}
}

func HideKimiK3ChatStreamThinking(response *dto.ChatCompletionsStreamResponse) {
	if response == nil {
		return
	}
	for index := range response.Choices {
		choice := &response.Choices[index]
		choice.Delta.ReasoningContent = nil
		choice.Delta.Reasoning = nil
	}
}

func HideKimiK3ClaudeThinking(response *dto.ClaudeResponse) {
	if response == nil {
		return
	}
	content := response.Content[:0]
	for _, block := range response.Content {
		if block.Type != "thinking" && block.Type != "redacted_thinking" {
			content = append(content, block)
		}
	}
	response.Content = content
}

type KimiK3ClaudeStreamThinkingFilter struct {
	hiddenIndexes map[int]struct{}
}

func NewKimiK3ClaudeStreamThinkingFilter() *KimiK3ClaudeStreamThinkingFilter {
	return &KimiK3ClaudeStreamThinkingFilter{hiddenIndexes: make(map[int]struct{})}
}

func (f *KimiK3ClaudeStreamThinkingFilter) Filter(response *dto.ClaudeResponse) *dto.ClaudeResponse {
	if f == nil || response == nil {
		return response
	}
	index := response.GetIndex()
	if response.Type == "content_block_start" && response.ContentBlock != nil &&
		(response.ContentBlock.Type == "thinking" || response.ContentBlock.Type == "redacted_thinking") {
		f.hiddenIndexes[index] = struct{}{}
		return nil
	}
	if _, hidden := f.hiddenIndexes[index]; hidden &&
		(response.Type == "content_block_delta" || response.Type == "content_block_stop") {
		return nil
	}
	if response.Index != nil {
		hiddenBefore := 0
		for hiddenIndex := range f.hiddenIndexes {
			if hiddenIndex < index {
				hiddenBefore++
			}
		}
		adjusted := index - hiddenBefore
		response.Index = &adjusted
	}
	return response
}

func HideKimiK3ResponsesThinking(response *dto.OpenAIResponsesResponse) {
	if response == nil {
		return
	}
	output := response.Output[:0]
	for _, item := range response.Output {
		if item.Type != "reasoning" {
			output = append(output, item)
		}
	}
	response.Output = output
}

func HideKimiK3ResponsesStreamThinking(event *ChatToResponsesStreamEvent) bool {
	if event == nil {
		return false
	}
	if event.Payload.Response != nil {
		HideKimiK3ResponsesThinking(event.Payload.Response)
	}
	if event.Payload.Item != nil && event.Payload.Item.Type == "reasoning" {
		return false
	}
	switch event.Type {
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		return false
	default:
		return true
	}
}
