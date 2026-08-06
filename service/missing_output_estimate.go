package service

import (
	"math"

	"github.com/QuantumNous/new-api/dto"
)

// ApplyMissingOutputEstimate fills protocol and billing usage only when the
// normalized upstream output usage is zero.
func ApplyMissingOutputEstimate(usage *dto.Usage, estimatedTokens int) bool {
	if usage == nil || estimatedTokens <= 0 || NormalizeUsageForBilling(usage).OutputTokens > 0 {
		return false
	}
	if estimatedTokens > math.MaxInt32 {
		estimatedTokens = math.MaxInt32
	}

	normalized := NormalizeUsageForBilling(usage)
	usage.CompletionTokens = estimatedTokens
	usage.OutputTokens = estimatedTokens
	usage.TotalTokens = saturatingTokenAdd(normalized.InputTokens.TotalInputTokens, estimatedTokens)
	usage.Estimated = true

	if usage.BillingUsage == nil {
		return true
	}
	usage.BillingUsage.Estimated = true
	switch {
	case usage.BillingUsage.OpenAIUsage != nil:
		openAIUsage := usage.BillingUsage.OpenAIUsage
		openAIUsage.CompletionTokens = estimatedTokens
		openAIUsage.OutputTokens = estimatedTokens
		openAIUsage.TotalTokens = saturatingTokenAdd(normalized.InputTokens.TotalInputTokens, estimatedTokens)
		openAIUsage.Estimated = true
	case usage.BillingUsage.ClaudeUsage != nil:
		usage.BillingUsage.ClaudeUsage.OutputTokens = estimatedTokens
	case usage.BillingUsage.GeminiUsageMetadata != nil:
		metadata := usage.BillingUsage.GeminiUsageMetadata
		metadata.CandidatesTokenCount = estimatedTokens
		metadata.TotalTokenCount = saturatingTokenAdd(normalized.InputTokens.TotalInputTokens, estimatedTokens)
	}
	return true
}
