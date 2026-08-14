package service

import (
	"math"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
)

func prepareKimiK3OfficialBilling(relayInfo *relaycommon.RelayInfo, usage *dto.Usage) *dto.Usage {
	if relayInfo == nil || !relayInfo.IsKimiK3OfficialCompatibility() || usage == nil {
		return usage
	}
	prepared := *usage
	prepared.BillingUsage = dto.CloneBillingUsage(usage.BillingUsage)
	if relayInfo.KimiK3HideThinking {
		relayconvert.HideKimiK3ReasoningUsage(&prepared)
	}
	canonical, audit := canonicalKimiK3OpenAIUsage(&prepared)
	prepared.BillingUsage = &dto.BillingUsage{
		Source:      dto.BillingUsageSourceOAIChat,
		Semantic:    dto.BillingUsageSemanticOpenAI,
		OpenAIUsage: canonical,
	}
	relayInfo.KimiK3BillingAudit = audit
	return &prepared
}

func canonicalKimiK3OpenAIUsage(usage *dto.Usage) (*dto.Usage, *dto.KimiK3BillingAudit) {
	audit := &dto.KimiK3BillingAudit{}
	var rawInput, rawOutput, rawCacheRead, rawCacheCreation int
	var sourceUsage *dto.Usage

	if usage.BillingUsage != nil {
		audit.OriginalSource = usage.BillingUsage.Source
		audit.OriginalSemantic = usage.BillingUsage.Semantic
		if claudeUsage := usage.BillingUsage.ClaudeUsage; claudeUsage != nil &&
			(strings.EqualFold(audit.OriginalSource, dto.BillingUsageSourceClaudeMessages) || strings.EqualFold(audit.OriginalSemantic, dto.BillingUsageSemanticAnthropic)) {
			rawInput = claudeUsage.InputTokens
			rawOutput = claudeUsage.OutputTokens
			rawCacheRead = claudeUsage.CacheReadInputTokens
			rawCacheCreation = claudeUsage.CacheCreationInputTokens
			if rawCacheCreation == 0 {
				rawCacheCreation = signedTokenAdd(claudeUsage.GetCacheCreation5mTokens(), claudeUsage.GetCacheCreation1hTokens())
			}
			audit.Equation = "input_tokens + cache_read_input_tokens + cache_creation_input_tokens"
		} else if usage.BillingUsage.OpenAIUsage != nil {
			sourceUsage = usage.BillingUsage.OpenAIUsage
		}
	}

	if audit.Equation == "" {
		if sourceUsage == nil {
			sourceUsage = usage
		}
		rawInput = sourceUsage.PromptTokens
		if sourceUsage.InputTokens != 0 {
			rawInput = sourceUsage.InputTokens
		}
		rawOutput = sourceUsage.CompletionTokens
		if sourceUsage.OutputTokens != 0 {
			rawOutput = sourceUsage.OutputTokens
		}
		rawCacheRead = sourceUsage.PromptTokensDetails.CachedTokens
		rawCacheCreation = sourceUsage.PromptTokensDetails.CachedCreationTokens
		if sourceUsage.PromptTokensDetails.CacheWriteTokens != 0 {
			rawCacheCreation = sourceUsage.PromptTokensDetails.CacheWriteTokens
		}
		audit.Equation = "prompt_tokens"
		if audit.OriginalSource == "" {
			audit.OriginalSource = sourceUsage.UsageSource
		}
		if audit.OriginalSemantic == "" {
			audit.OriginalSemantic = sourceUsage.UsageSemantic
		}
	}

	audit.OriginalInputTokens = rawInput
	audit.OriginalOutputTokens = rawOutput
	audit.OriginalCacheRead = rawCacheRead
	audit.OriginalCacheCreation = rawCacheCreation
	for _, field := range []struct {
		name  string
		value int
	}{
		{name: "input_tokens", value: rawInput},
		{name: "output_tokens", value: rawOutput},
		{name: "cache_read_input_tokens", value: rawCacheRead},
		{name: "cache_creation_input_tokens", value: rawCacheCreation},
	} {
		if field.value < 0 {
			audit.NegativeFields = append(audit.NegativeFields, field.name)
		}
	}

	signedInput := rawInput
	if audit.Equation != "prompt_tokens" {
		signedInput = signedTokenAdd(rawInput, rawCacheRead, rawCacheCreation)
	}
	audit.SignedTotalInput = signedInput
	totalInput := positiveTokenCount(signedInput)
	output := positiveTokenCount(rawOutput)
	cacheRead := min(positiveTokenCount(rawCacheRead), totalInput)
	cacheCreation := min(positiveTokenCount(rawCacheCreation), totalInput-cacheRead)
	if cacheRead != positiveTokenCount(rawCacheRead) {
		audit.Adjustments = append(audit.Adjustments, "cache_read_capped_to_total_input")
	}
	if cacheCreation != positiveTokenCount(rawCacheCreation) {
		audit.Adjustments = append(audit.Adjustments, "cache_creation_capped_to_remaining_input")
	}
	if signedInput < 0 {
		audit.Adjustments = append(audit.Adjustments, "negative_total_input_clamped_to_zero")
	}
	if rawOutput < 0 {
		audit.Adjustments = append(audit.Adjustments, "negative_output_clamped_to_zero")
	}
	audit.NormalizedTotalInput = totalInput
	audit.NormalizedOutput = output
	audit.NormalizedCacheRead = cacheRead
	audit.NormalizedCacheWrite = cacheCreation

	canonical := &dto.Usage{
		PromptTokens:     totalInput,
		InputTokens:      totalInput,
		CompletionTokens: output,
		OutputTokens:     output,
		TotalTokens:      saturatingTokenAdd(totalInput, output),
		UsageSource:      dto.BillingUsageSourceOAIChat,
		UsageSemantic:    dto.BillingUsageSemanticOpenAI,
	}
	canonical.PromptTokensDetails.CachedTokens = cacheRead
	canonical.PromptTokensDetails.CachedCreationTokens = cacheCreation
	canonical.PromptTokensDetails.CacheWriteTokens = cacheCreation
	if sourceUsage != nil {
		canonical.PromptTokensDetails.TextTokens = positiveTokenCount(sourceUsage.PromptTokensDetails.TextTokens)
		canonical.PromptTokensDetails.ImageTokens = positiveTokenCount(sourceUsage.PromptTokensDetails.ImageTokens)
		canonical.PromptTokensDetails.AudioTokens = positiveTokenCount(sourceUsage.PromptTokensDetails.AudioTokens)
		canonical.CompletionTokenDetails = sourceUsage.CompletionTokenDetails
		canonical.CompletionTokenDetails.TextTokens = positiveTokenCount(canonical.CompletionTokenDetails.TextTokens)
		canonical.CompletionTokenDetails.ImageTokens = positiveTokenCount(canonical.CompletionTokenDetails.ImageTokens)
		canonical.CompletionTokenDetails.AudioTokens = positiveTokenCount(canonical.CompletionTokenDetails.AudioTokens)
		canonical.CompletionTokenDetails.ReasoningTokens = positiveTokenCount(canonical.CompletionTokenDetails.ReasoningTokens)
	}
	return canonical, audit
}

func signedTokenAdd(values ...int) int {
	total := 0
	for _, value := range values {
		if value > 0 && total > math.MaxInt-value {
			return math.MaxInt
		}
		if value < 0 && total < math.MinInt-value {
			return math.MinInt
		}
		total += value
	}
	return total
}
