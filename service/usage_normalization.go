package service

import (
	"math"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

const (
	UsageSemanticOpenAI    = "openai"
	UsageSemanticAnthropic = "anthropic"
	UsageSemanticGemini    = "gemini"

	UsageAccountingModeIncluded = "included"
	UsageAccountingModeSeparate = "separate"

	UsageNormalizationSourceBillingUsage  = "billing_usage"
	UsageNormalizationSourceUsageSource   = "usage_source"
	UsageNormalizationSourceUsageSemantic = "usage_semantic"
	UsageNormalizationSourceTotalTokens   = "total_tokens"
	UsageNormalizationSourceFallback      = "fallback"

	UsageNormalizationStatusMatched    = "matched"
	UsageNormalizationStatusNotChecked = "not_checked"
	UsageNormalizationStatusMismatch   = "mismatch"
)

type NormalizedInputTokens struct {
	TotalInputTokens           int
	UncachedInputTokens        int
	CacheReadInputTokens       int
	CacheCreationInputTokens   int
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int
}

type UsageNormalizationAudit struct {
	Mode                          string `json:"mode"`
	Source                        string `json:"source"`
	Status                        string `json:"status"`
	ReportedInputTokens           int    `json:"reported_input_tokens"`
	ReportedOutputTokens          int    `json:"reported_output_tokens"`
	ReportedTotalTokens           int    `json:"reported_total_tokens"`
	CacheReadInputTokens          int    `json:"cache_read_input_tokens"`
	CacheCreationInputTokens      int    `json:"cache_creation_input_tokens"`
	NormalizedUncachedInputTokens int    `json:"normalized_uncached_input_tokens"`
	NormalizedTotalInputTokens    int    `json:"normalized_total_input_tokens"`
}

type BillingUsageNormalization struct {
	InputTokens       NormalizedInputTokens
	OutputTokens      int
	TotalTokens       int
	InputImageTokens  int
	InputAudioTokens  int
	OutputImageTokens int
	OutputAudioTokens int
	UsageSemantic     string
	Audit             UsageNormalizationAudit
}

// NormalizeUsageForBilling converts upstream usage into the gateway's single
// accounting contract: uncached input, cache read, cache creation and output
// are independent quantities. It intentionally does not mutate or replace the
// protocol-shaped usage returned to the client.
func NormalizeUsageForBilling(usage *dto.Usage) BillingUsageNormalization {
	if billingUsage, ok := usageFromBillingUsage(usage); ok {
		usage = billingUsage
	}
	if usage == nil {
		return BillingUsageNormalization{
			Audit: UsageNormalizationAudit{
				Mode:   UsageAccountingModeIncluded,
				Source: UsageNormalizationSourceFallback,
				Status: UsageNormalizationStatusNotChecked,
			},
		}
	}

	reportedInputTokens := positiveTokenCount(usage.PromptTokens)
	if inputTokens := positiveTokenCount(usage.InputTokens); inputTokens > reportedInputTokens {
		reportedInputTokens = inputTokens
	}
	outputTokens := positiveTokenCount(usage.CompletionTokens)
	if candidate := positiveTokenCount(usage.OutputTokens); candidate > outputTokens {
		outputTokens = candidate
	}
	inputDetails := usage.PromptTokensDetails
	if usage.InputTokensDetails != nil {
		if usage.InputTokensDetails.CachedTokens > inputDetails.CachedTokens {
			inputDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
		}
		if usage.InputTokensDetails.CachedCreationTokens > inputDetails.CachedCreationTokens {
			inputDetails.CachedCreationTokens = usage.InputTokensDetails.CachedCreationTokens
		}
		if usage.InputTokensDetails.CacheWriteTokens > inputDetails.CacheWriteTokens {
			inputDetails.CacheWriteTokens = usage.InputTokensDetails.CacheWriteTokens
		}
		if usage.InputTokensDetails.ImageTokens > inputDetails.ImageTokens {
			inputDetails.ImageTokens = usage.InputTokensDetails.ImageTokens
		}
		if usage.InputTokensDetails.AudioTokens > inputDetails.AudioTokens {
			inputDetails.AudioTokens = usage.InputTokensDetails.AudioTokens
		}
	}
	cacheReadTokens := positiveTokenCount(inputDetails.CachedTokens)
	cacheCreation5mTokens := positiveTokenCount(usage.ClaudeCacheCreation5mTokens)
	cacheCreation1hTokens := positiveTokenCount(usage.ClaudeCacheCreation1hTokens)
	cacheCreationTokens := positiveTokenCount(inputDetails.CacheCreationTokensTotal())
	if splitTotal := saturatingTokenAdd(cacheCreation5mTokens, cacheCreation1hTokens); splitTotal > cacheCreationTokens {
		cacheCreationTokens = splitTotal
	}

	reportedTotalTokens := positiveTokenCount(usage.TotalTokens)
	if usage.BillingUsage != nil {
		switch {
		case usage.BillingUsage.OpenAIUsage != nil:
			reportedTotalTokens = positiveTokenCount(usage.BillingUsage.OpenAIUsage.TotalTokens)
		case usage.BillingUsage.GeminiUsageMetadata != nil:
			reportedTotalTokens = positiveTokenCount(usage.BillingUsage.GeminiUsageMetadata.TotalTokenCount)
		case usage.BillingUsage.ClaudeUsage != nil:
			reportedTotalTokens = 0
		}
	}

	mode, source := usageAccountingModeFromMetadata(usage)
	status := UsageNormalizationStatusNotChecked
	includedTotal := saturatingTokenAdd(reportedInputTokens, outputTokens)
	separateTotal := saturatingTokenAdd(reportedInputTokens, cacheReadTokens, cacheCreationTokens, outputTokens)

	if reportedTotalTokens > 0 {
		includedMatches := reportedTotalTokens == includedTotal
		separateMatches := reportedTotalTokens == separateTotal
		switch {
		case includedMatches && !separateMatches:
			mode = UsageAccountingModeIncluded
			source = UsageNormalizationSourceTotalTokens
			status = UsageNormalizationStatusMatched
		case separateMatches && !includedMatches:
			mode = UsageAccountingModeSeparate
			source = UsageNormalizationSourceTotalTokens
			status = UsageNormalizationStatusMatched
		case includedMatches && separateMatches:
			// With no separately reported cache tokens both contracts are
			// equivalent, so retain known metadata or choose the included form.
			status = UsageNormalizationStatusMatched
			if mode == "" {
				mode = UsageAccountingModeIncluded
				source = UsageNormalizationSourceTotalTokens
			}
		case mode != "":
			status = UsageNormalizationStatusMismatch
		}
	}
	if mode == "" {
		mode = UsageAccountingModeIncluded
		source = UsageNormalizationSourceFallback
		if reportedTotalTokens > 0 {
			status = UsageNormalizationStatusMismatch
		}
	}

	input := NormalizedInputTokens{
		CacheReadInputTokens:       cacheReadTokens,
		CacheCreationInputTokens:   cacheCreationTokens,
		CacheCreation5mInputTokens: cacheCreation5mTokens,
		CacheCreation1hInputTokens: cacheCreation1hTokens,
	}
	if mode == UsageAccountingModeSeparate {
		input.UncachedInputTokens = reportedInputTokens
		input.TotalInputTokens = saturatingTokenAdd(reportedInputTokens, cacheReadTokens, cacheCreationTokens)
	} else {
		input.TotalInputTokens = reportedInputTokens
		input.UncachedInputTokens = uncachedInputTokenCount(reportedInputTokens, cacheReadTokens, cacheCreationTokens)
	}

	return BillingUsageNormalization{
		InputTokens:       input,
		OutputTokens:      outputTokens,
		TotalTokens:       saturatingTokenAdd(input.TotalInputTokens, outputTokens),
		InputImageTokens:  positiveTokenCount(inputDetails.ImageTokens),
		InputAudioTokens:  positiveTokenCount(inputDetails.AudioTokens),
		OutputImageTokens: positiveTokenCount(usage.CompletionTokenDetails.ImageTokens),
		OutputAudioTokens: positiveTokenCount(usage.CompletionTokenDetails.AudioTokens),
		UsageSemantic:     usage.UsageSemantic,
		Audit: UsageNormalizationAudit{
			Mode:                          mode,
			Source:                        source,
			Status:                        status,
			ReportedInputTokens:           reportedInputTokens,
			ReportedOutputTokens:          outputTokens,
			ReportedTotalTokens:           reportedTotalTokens,
			CacheReadInputTokens:          cacheReadTokens,
			CacheCreationInputTokens:      cacheCreationTokens,
			NormalizedUncachedInputTokens: input.UncachedInputTokens,
			NormalizedTotalInputTokens:    input.TotalInputTokens,
		},
	}
}

func usageAccountingModeFromMetadata(usage *dto.Usage) (string, string) {
	if usage.BillingUsage != nil {
		if mode := usageAccountingMode(usage.BillingUsage.Source); mode != "" {
			return mode, UsageNormalizationSourceBillingUsage
		}
		if mode := usageAccountingMode(usage.BillingUsage.Semantic); mode != "" {
			return mode, UsageNormalizationSourceBillingUsage
		}
	}
	if mode := usageAccountingMode(usage.UsageSource); mode != "" {
		return mode, UsageNormalizationSourceUsageSource
	}
	if mode := usageAccountingMode(usage.UsageSemantic); mode != "" {
		return mode, UsageNormalizationSourceUsageSemantic
	}
	return "", ""
}

func usageAccountingMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dto.BillingUsageSourceClaudeMessages, UsageSemanticAnthropic:
		return UsageAccountingModeSeparate
	case dto.BillingUsageSourceOAIChat, dto.BillingUsageSourceOAIResponses,
		dto.BillingUsageSourceGeminiChat, UsageSemanticOpenAI, UsageSemanticGemini:
		return UsageAccountingModeIncluded
	default:
		return ""
	}
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
	normalized.UncachedInputTokens = uncachedInputTokenCount(inputTokens, cacheReadTokens, cacheCreationTokens)
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

func uncachedInputTokenCount(totalInputTokens, cacheReadTokens, cacheCreationTokens int) int {
	separatelyPricedTokens := saturatingTokenAdd(cacheReadTokens, cacheCreationTokens)
	if separatelyPricedTokens >= positiveTokenCount(totalInputTokens) {
		return 0
	}
	return positiveTokenCount(totalInputTokens) - separatelyPricedTokens
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
