package service

import (
	"math"

	"github.com/QuantumNous/new-api/dto"
)

const (
	UsageSemanticOpenAI    = "openai"
	UsageSemanticAnthropic = "anthropic"
)

type NormalizedInputTokens struct {
	TotalInputTokens           int
	UncachedInputTokens        int
	CacheReadInputTokens       int
	CacheCreationInputTokens   int
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int
}

// NormalizeInputTokens resolves the two supported usage contracts into one
// representation. Anthropic input is uncached input; OpenAI input is the total
// and cached_tokens is a subset of it.
func NormalizeInputTokens(usage *dto.Usage) NormalizedInputTokens {
	if usage == nil {
		return NormalizedInputTokens{}
	}

	inputTokens := positiveTokenCount(usage.PromptTokens)
	if fallback := positiveTokenCount(usage.InputTokens); fallback > inputTokens {
		inputTokens = fallback
	}
	cacheReadTokens := positiveTokenCount(usage.PromptTokensDetails.CachedTokens)
	cacheCreation5mTokens := positiveTokenCount(usage.ClaudeCacheCreation5mTokens)
	cacheCreation1hTokens := positiveTokenCount(usage.ClaudeCacheCreation1hTokens)
	cacheCreationTokens := positiveTokenCount(usage.PromptTokensDetails.CacheCreationTokensTotal())
	if splitTotal := saturatingTokenAdd(cacheCreation5mTokens, cacheCreation1hTokens); splitTotal > cacheCreationTokens {
		cacheCreationTokens = splitTotal
	}

	normalized := NormalizedInputTokens{
		CacheReadInputTokens:       cacheReadTokens,
		CacheCreationInputTokens:   cacheCreationTokens,
		CacheCreation5mInputTokens: cacheCreation5mTokens,
		CacheCreation1hInputTokens: cacheCreation1hTokens,
	}
	if usage.UsageSemantic == UsageSemanticAnthropic {
		normalized.UncachedInputTokens = inputTokens
		normalized.TotalInputTokens = saturatingTokenAdd(inputTokens, cacheReadTokens, cacheCreationTokens)
		return normalized
	}

	normalized.TotalInputTokens = inputTokens
	normalized.UncachedInputTokens = inputTokens - cacheReadTokens - cacheCreationTokens
	if normalized.UncachedInputTokens < 0 {
		normalized.UncachedInputTokens = 0
	}
	return normalized
}

// NormalizeUsageForSemantic returns a copy rendered with the target protocol's
// input-token contract. It never mutates the usage object used for billing.
func NormalizeUsageForSemantic(usage *dto.Usage, targetSemantic string) dto.Usage {
	if usage == nil {
		return dto.Usage{UsageSemantic: targetSemantic}
	}

	clone := *usage
	input := NormalizeInputTokens(usage)
	completionTokens := positiveTokenCount(usage.CompletionTokens)
	if outputTokens := positiveTokenCount(usage.OutputTokens); outputTokens > completionTokens {
		completionTokens = outputTokens
	}

	clone.PromptTokensDetails.CachedTokens = input.CacheReadInputTokens
	clone.PromptTokensDetails.CachedCreationTokens = input.CacheCreationInputTokens
	clone.ClaudeCacheCreation5mTokens = input.CacheCreation5mInputTokens
	clone.ClaudeCacheCreation1hTokens = input.CacheCreation1hInputTokens
	clone.CompletionTokens = completionTokens
	clone.OutputTokens = completionTokens
	if clone.InputTokensDetails != nil {
		details := *clone.InputTokensDetails
		clone.InputTokensDetails = &details
	} else {
		clone.InputTokensDetails = &dto.InputTokenDetails{}
	}
	clone.InputTokensDetails.CachedTokens = input.CacheReadInputTokens
	clone.InputTokensDetails.CachedCreationTokens = input.CacheCreationInputTokens

	if targetSemantic == UsageSemanticAnthropic {
		clone.PromptTokens = input.UncachedInputTokens
		clone.InputTokens = input.UncachedInputTokens
	} else {
		targetSemantic = UsageSemanticOpenAI
		clone.PromptTokens = input.TotalInputTokens
		clone.InputTokens = input.TotalInputTokens
	}
	clone.TotalTokens = saturatingTokenAdd(input.TotalInputTokens, completionTokens)
	if usage.UsageSemantic != "" && usage.UsageSemantic != targetSemantic {
		clone.UsageSource = usage.UsageSemantic
	}
	clone.UsageSemantic = targetSemantic
	return clone
}

func positiveTokenCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func saturatingTokenAdd(values ...int) int {
	total := 0
	for _, value := range values {
		value = positiveTokenCount(value)
		if total > math.MaxInt-value {
			return math.MaxInt
		}
		total += value
	}
	return total
}
