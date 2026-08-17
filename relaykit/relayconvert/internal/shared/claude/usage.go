package claude

import (
	"math"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func NormalizeInputIncludesCache(usage *dto.ClaudeUsage) bool {
	if usage == nil || usage.BillingUsage.HasNormalizedAnthropicInputCache() {
		return false
	}

	original := *usage
	if usage.CacheCreation != nil {
		cacheCreation := *usage.CacheCreation
		original.CacheCreation = &cacheCreation
	}
	originalBilling := dto.CloneBillingUsage(usage.BillingUsage)
	usage.InputTokens = positiveTokenCount(usage.InputTokens)
	usage.CacheReadInputTokens = positiveTokenCount(usage.CacheReadInputTokens)
	usage.CacheCreationInputTokens = positiveTokenCount(usage.CacheCreationInputTokens)
	usage.ClaudeCacheCreation5mTokens = positiveTokenCount(usage.ClaudeCacheCreation5mTokens)
	usage.ClaudeCacheCreation1hTokens = positiveTokenCount(usage.ClaudeCacheCreation1hTokens)

	cacheCreation5m := 0
	cacheCreation1h := 0
	if usage.CacheCreation != nil {
		cacheCreation5m = positiveTokenCount(usage.CacheCreation.Ephemeral5mInputTokens)
		cacheCreation1h = positiveTokenCount(usage.CacheCreation.Ephemeral1hInputTokens)
		usage.CacheCreation.Ephemeral5mInputTokens = cacheCreation5m
		usage.CacheCreation.Ephemeral1hInputTokens = cacheCreation1h
	}
	if usage.CacheCreationInputTokens == 0 {
		standardSplit := saturatingTokenAdd(cacheCreation5m, cacheCreation1h)
		legacySplit := saturatingTokenAdd(usage.ClaudeCacheCreation5mTokens, usage.ClaudeCacheCreation1hTokens)
		if legacySplit > standardSplit {
			standardSplit = legacySplit
		}
		usage.CacheCreationInputTokens = standardSplit
	}

	usage.InputTokens = subtractTokenCount(usage.InputTokens, usage.CacheReadInputTokens)
	usage.InputTokens = subtractTokenCount(usage.InputTokens, usage.CacheCreationInputTokens)

	if originalBilling != nil && originalBilling.IsRecognized() && originalBilling.ClaudeUsage == nil {
		usage.BillingUsage = originalBilling
	} else {
		usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(usage)
	}
	if usage.BillingUsage != nil {
		usage.BillingUsage.AnthropicInputCacheNormalized = true
	}

	return original.InputTokens != usage.InputTokens ||
		original.CacheReadInputTokens != usage.CacheReadInputTokens ||
		original.CacheCreationInputTokens != usage.CacheCreationInputTokens ||
		original.ClaudeCacheCreation5mTokens != usage.ClaudeCacheCreation5mTokens ||
		original.ClaudeCacheCreation1hTokens != usage.ClaudeCacheCreation1hTokens ||
		cacheCreationChanged(original.CacheCreation, usage.CacheCreation) ||
		!billingUsageEquivalent(originalBilling, usage.BillingUsage)
}

func positiveTokenCount(tokens int) int {
	if tokens < 0 {
		return 0
	}
	return tokens
}

func subtractTokenCount(total int, part int) int {
	if part >= total {
		return 0
	}
	return total - part
}

func saturatingTokenAdd(left int, right int) int {
	if left > math.MaxInt-right {
		return math.MaxInt
	}
	return left + right
}

func cacheCreationChanged(left *dto.ClaudeCacheCreationUsage, right *dto.ClaudeCacheCreationUsage) bool {
	if left == nil || right == nil {
		return left != right
	}
	return left.Ephemeral5mInputTokens != right.Ephemeral5mInputTokens ||
		left.Ephemeral1hInputTokens != right.Ephemeral1hInputTokens
}

func billingUsageEquivalent(left *dto.BillingUsage, right *dto.BillingUsage) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Source == right.Source &&
		left.Semantic == right.Semantic &&
		left.Estimated == right.Estimated &&
		left.AnthropicInputCacheNormalized == right.AnthropicInputCacheNormalized
}
