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

func UsageFromOpenAI(usage *dto.Usage) *dto.ClaudeUsage {
	if usage == nil {
		return nil
	}
	// An existing sidecar snapshots the original provider usage; carry it
	// across this bridge unchanged regardless of its dialect. Only synthesize
	// an OpenAI snapshot when no sidecar exists yet.
	existingBillingUsage := dto.CloneBillingUsage(usage.BillingUsage)
	if existingBillingUsage != nil && existingBillingUsage.ClaudeUsage != nil &&
		(existingBillingUsage.Source == dto.BillingUsageSourceClaudeMessages || existingBillingUsage.Semantic == dto.BillingUsageSemanticAnthropic) {
		result := existingBillingUsage.ClaudeUsage
		result.BillingUsage = dto.CloneBillingUsage(usage.BillingUsage)
		return result
	}
	billingUsage := existingBillingUsage
	if billingUsage == nil {
		billingUsage = dto.NewOpenAIChatBillingUsage(usage)
	}
	cacheCreation5m, cacheCreation1h := NormalizeCacheCreationSplit(
		usage.PromptTokensDetails.CachedCreationTokens,
		usage.ClaudeCacheCreation5mTokens,
		usage.ClaudeCacheCreation1hTokens,
	)
	cacheCreationTokens := usage.PromptTokensDetails.CacheCreationTokensTotal()
	inputTokens := usage.PromptTokens
	if usage.UsageSemantic != dto.BillingUsageSemanticAnthropic {
		// OpenAI-style prompt/input totals include cache reads and writes, while
		// Claude reports both separately from input_tokens.
		inputTokens = usage.PromptTokens - usage.PromptTokensDetails.CachedTokens - cacheCreationTokens
		if inputTokens < 0 {
			inputTokens = 0
		}
	}
	result := &dto.ClaudeUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.CompletionTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CacheReadInputTokens:     usage.PromptTokensDetails.CachedTokens,
		BillingUsage:             billingUsage,
	}
	if cacheCreation5m > 0 || cacheCreation1h > 0 {
		result.CacheCreation = &dto.ClaudeCacheCreationUsage{
			Ephemeral5mInputTokens: cacheCreation5m,
			Ephemeral1hInputTokens: cacheCreation1h,
		}
	}
	return result
}

// OpenAIUsageFromBilling projects Anthropic counters into inclusive OpenAI totals.
func OpenAIUsageFromBilling(billing *dto.BillingUsage) *dto.Usage {
	if billing == nil || billing.ClaudeUsage == nil {
		return nil
	}
	usage, ok := billing.CanonicalUsage()
	if !ok {
		return nil
	}
	cacheWrite := max(usage.PromptTokensDetails.CachedCreationTokens, saturatingTokenAdd(usage.ClaudeCacheCreation5mTokens, usage.ClaudeCacheCreation1hTokens))
	usage.PromptTokens = saturatingTokenAdd(saturatingTokenAdd(usage.PromptTokens, usage.PromptTokensDetails.CachedTokens), cacheWrite)
	usage.InputTokens = usage.PromptTokens
	usage.TotalTokens = saturatingTokenAdd(usage.PromptTokens, usage.CompletionTokens)
	usage.PromptTokensDetails.CacheWriteTokens = cacheWrite
	usage.UsageSemantic = dto.BillingUsageSemanticOpenAI
	return usage
}
