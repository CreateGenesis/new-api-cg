package relayconvert

import "github.com/QuantumNous/new-api/relaykit/dto"

func HideKimiK3ChatThinking(response *dto.OpenAITextResponse) {
	if response == nil {
		return
	}
	for index := range response.Choices {
		response.Choices[index].Message.ReasoningContent = nil
		response.Choices[index].Message.Reasoning = nil
	}
	HideKimiK3ReasoningUsage(&response.Usage)
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
	HideKimiK3ReasoningUsage(response.Usage)
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
	HideKimiK3ReasoningUsage(response.Usage)
}

// HideKimiK3ReasoningUsage makes pseudo-disabled thinking follow the official
// non-thinking usage contract. Reported reasoning tokens are removed from both
// the response totals and any protocol-specific usage retained for billing.
func HideKimiK3ReasoningUsage(usage *dto.Usage) {
	if usage == nil {
		return
	}
	removeKimiK3ReasoningTokens(usage)
	if usage.BillingUsage == nil {
		return
	}
	removeKimiK3ReasoningTokens(usage.BillingUsage.OpenAIUsage)
	if metadata := usage.BillingUsage.GeminiUsageMetadata; metadata != nil {
		reasoningTokens := max(metadata.ThoughtsTokenCount, 0)
		metadata.ThoughtsTokenCount = 0
		metadata.TotalTokenCount = subtractKimiK3Tokens(metadata.TotalTokenCount, reasoningTokens, metadata.PromptTokenCount)
	}
}

func removeKimiK3ReasoningTokens(usage *dto.Usage) {
	if usage == nil {
		return
	}
	reasoningTokens := max(usage.CompletionTokenDetails.ReasoningTokens, 0)
	usage.CompletionTokenDetails.ReasoningTokens = 0
	if reasoningTokens == 0 {
		return
	}
	usage.CompletionTokens = subtractKimiK3Tokens(usage.CompletionTokens, reasoningTokens, 0)
	usage.OutputTokens = subtractKimiK3Tokens(usage.OutputTokens, reasoningTokens, 0)
	minimumTotal := max(usage.PromptTokens, usage.InputTokens)
	usage.TotalTokens = subtractKimiK3Tokens(usage.TotalTokens, reasoningTokens, minimumTotal)
}

func subtractKimiK3Tokens(total, deduction, minimum int) int {
	if total <= minimum || deduction <= 0 {
		return max(total, 0)
	}
	return max(total-min(deduction, total-minimum), minimum)
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
