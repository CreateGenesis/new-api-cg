package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const (
	usageTokenLimitMinBasisPoints = 3000
	usageTokenLimitMaxBasisPoints = 9500
)

var (
	ErrUpstreamUsageMissingInput  = errors.New("upstream returned zero input tokens")
	ErrUpstreamUsageMissingOutput = errors.New("upstream returned zero output tokens")
)

type usageTokenLimitRandom func() (int, error)

func secureUsageTokenLimitBasisPoints() (int, error) {
	span := int64(usageTokenLimitMaxBasisPoints - usageTokenLimitMinBasisPoints + 1)
	value, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return 0, err
	}
	return usageTokenLimitMinBasisPoints + int(value.Int64()), nil
}

// ApplyTextUsagePolicy applies the channel's configured usage limits before the
// response is delivered or billed. Protocol-level presence checks stay in the relay recorder.
func ApplyTextUsagePolicy(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) (bool, error) {
	return applyTextUsagePolicy(ctx, relayInfo, usage, secureUsageTokenLimitBasisPoints)
}

func applyTextUsagePolicy(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, random usageTokenLimitRandom) (bool, error) {
	if relayInfo == nil || relayInfo.ChannelMeta == nil || relayInfo.ChannelOtherSettings.UsageTokenLimit == nil {
		return false, nil
	}
	settings := relayInfo.ChannelOtherSettings.UsageTokenLimit
	if settings.InputTokens <= 0 && settings.OutputTokens <= 0 {
		return false, nil
	}
	if usage == nil || usage.Estimated {
		return false, nil
	}

	normalized := NormalizeUsageForBilling(usage)
	finalInput := normalized.InputTokens.TotalInputTokens
	finalOutput := normalized.OutputTokens
	audit := &relaycommon.UsageTokenLimitAudit{}

	if settings.InputTokens > 0 && finalInput > settings.InputTokens {
		basisPoints, err := random()
		if err != nil {
			return false, fmt.Errorf("generate input token limit random factor: %w", err)
		}
		finalInput = limitedUsageTokens(settings.InputTokens, basisPoints)
		audit.Input = &relaycommon.UsageTokenLimitDirectionAudit{
			Original:    normalized.InputTokens.TotalInputTokens,
			Limit:       settings.InputTokens,
			RandomBasis: basisPoints,
			Final:       finalInput,
		}
	}
	if settings.OutputTokens > 0 && finalOutput > settings.OutputTokens {
		basisPoints, err := random()
		if err != nil {
			return false, fmt.Errorf("generate output token limit random factor: %w", err)
		}
		finalOutput = limitedUsageTokens(settings.OutputTokens, basisPoints)
		audit.Output = &relaycommon.UsageTokenLimitDirectionAudit{
			Original:    normalized.OutputTokens,
			Limit:       settings.OutputTokens,
			RandomBasis: basisPoints,
			Final:       finalOutput,
		}
	}
	if audit.Input == nil && audit.Output == nil {
		return false, nil
	}

	applyUsageTokenLimits(usage, normalized, finalInput, finalOutput)
	relayInfo.UsageTokenLimitAudit = audit
	logger.LogWarn(ctx, fmt.Sprintf(
		"channel usage token limit applied: request_id=%s channel_id=%d input=%d->%d output=%d->%d",
		relayInfo.RequestId,
		relayInfo.ChannelId,
		normalized.InputTokens.TotalInputTokens,
		finalInput,
		normalized.OutputTokens,
		finalOutput,
	))
	return true, nil
}

func limitedUsageTokens(limit, basisPoints int) int {
	limited := int((int64(limit) * int64(basisPoints)) / 10000)
	if limited < 1 {
		return 1
	}
	return limited
}

func applyUsageTokenLimits(usage *dto.Usage, normalized BillingUsageNormalization, finalInput, finalOutput int) {
	if usage == nil {
		return
	}

	inputParts := proportionalTokenParts([]int{
		normalized.InputTokens.UncachedInputTokens,
		normalized.InputTokens.CacheReadInputTokens,
		normalized.InputTokens.CacheCreationInputTokens,
	}, normalized.InputTokens.TotalInputTokens, finalInput)
	cacheCreationParts := proportionalTokenSubsets([]int{
		normalized.InputTokens.CacheCreation5mInputTokens,
		normalized.InputTokens.CacheCreation1hInputTokens,
	}, normalized.InputTokens.CacheCreationInputTokens, inputParts[2])

	semantic := usage.UsageSemantic
	if semantic == "" && usage.BillingUsage != nil {
		semantic = usage.BillingUsage.Semantic
	}
	applyCanonicalUsage(usage, semantic, normalized, inputParts, cacheCreationParts, finalInput, finalOutput)

	if usage.BillingUsage == nil {
		return
	}
	billing := usage.BillingUsage
	if billing.OpenAIUsage != nil {
		applyCanonicalUsage(billing.OpenAIUsage, UsageSemanticOpenAI, normalized, inputParts, cacheCreationParts, finalInput, finalOutput)
	}
	if billing.ClaudeUsage != nil {
		applyClaudeUsageLimits(billing.ClaudeUsage, normalized, inputParts, cacheCreationParts, finalOutput)
	}
	if billing.GeminiUsageMetadata != nil {
		applyGeminiUsageLimits(billing.GeminiUsageMetadata, normalized, finalInput, finalOutput)
	}
}

func applyCanonicalUsage(usage *dto.Usage, semantic string, normalized BillingUsageNormalization, inputParts, cacheCreationParts []int, finalInput, finalOutput int) {
	if usage == nil {
		return
	}
	inputTokens := finalInput
	if semantic == UsageSemanticAnthropic {
		inputTokens = inputParts[0]
	}
	usage.PromptTokens = inputTokens
	usage.InputTokens = inputTokens
	usage.CompletionTokens = finalOutput
	usage.OutputTokens = finalOutput
	usage.TotalTokens = saturatingTokenAdd(finalInput, finalOutput)
	usage.PromptTokensDetails.CachedTokens = inputParts[1]
	usage.PromptTokensDetails.CachedCreationTokens = inputParts[2]
	usage.PromptTokensDetails.CacheWriteTokens = 0
	usage.PromptTokensDetails.TextTokens = scaleTokenSubset(usage.PromptTokensDetails.TextTokens, normalized.InputTokens.TotalInputTokens, finalInput)
	usage.PromptTokensDetails.AudioTokens = scaleTokenSubset(normalized.InputAudioTokens, normalized.InputTokens.TotalInputTokens, finalInput)
	usage.PromptTokensDetails.ImageTokens = scaleTokenSubset(normalized.InputImageTokens, normalized.InputTokens.TotalInputTokens, finalInput)
	usage.CompletionTokenDetails.TextTokens = scaleTokenSubset(usage.CompletionTokenDetails.TextTokens, normalized.OutputTokens, finalOutput)
	usage.CompletionTokenDetails.AudioTokens = scaleTokenSubset(normalized.OutputAudioTokens, normalized.OutputTokens, finalOutput)
	usage.CompletionTokenDetails.ImageTokens = scaleTokenSubset(normalized.OutputImageTokens, normalized.OutputTokens, finalOutput)
	usage.CompletionTokenDetails.ReasoningTokens = scaleTokenSubset(usage.CompletionTokenDetails.ReasoningTokens, normalized.OutputTokens, finalOutput)
	usage.ClaudeCacheCreation5mTokens = cacheCreationParts[0]
	usage.ClaudeCacheCreation1hTokens = cacheCreationParts[1]
	if usage.InputTokensDetails != nil {
		usage.InputTokensDetails.CachedTokens = inputParts[1]
		usage.InputTokensDetails.CachedCreationTokens = inputParts[2]
		usage.InputTokensDetails.CacheWriteTokens = 0
		usage.InputTokensDetails.TextTokens = usage.PromptTokensDetails.TextTokens
		usage.InputTokensDetails.AudioTokens = usage.PromptTokensDetails.AudioTokens
		usage.InputTokensDetails.ImageTokens = usage.PromptTokensDetails.ImageTokens
	}
}

func applyClaudeUsageLimits(usage *dto.ClaudeUsage, normalized BillingUsageNormalization, inputParts, cacheCreationParts []int, finalOutput int) {
	usage.InputTokens = inputParts[0]
	usage.CacheReadInputTokens = inputParts[1]
	usage.CacheCreationInputTokens = inputParts[2]
	usage.OutputTokens = finalOutput
	usage.ClaudeCacheCreation5mTokens = cacheCreationParts[0]
	usage.ClaudeCacheCreation1hTokens = cacheCreationParts[1]
	if usage.CacheCreation != nil {
		usage.CacheCreation.Ephemeral5mInputTokens = cacheCreationParts[0]
		usage.CacheCreation.Ephemeral1hInputTokens = cacheCreationParts[1]
	}
}

func applyGeminiUsageLimits(metadata *dto.GeminiUsageMetadata, normalized BillingUsageNormalization, finalInput, finalOutput int) {
	inputParts := proportionalTokenParts(
		[]int{metadata.PromptTokenCount, metadata.ToolUsePromptTokenCount},
		normalized.InputTokens.TotalInputTokens,
		finalInput,
	)
	outputParts := proportionalTokenParts(
		[]int{metadata.CandidatesTokenCount, metadata.ThoughtsTokenCount},
		normalized.OutputTokens,
		finalOutput,
	)
	metadata.PromptTokenCount = inputParts[0]
	metadata.ToolUsePromptTokenCount = inputParts[1]
	metadata.CandidatesTokenCount = outputParts[0]
	metadata.ThoughtsTokenCount = outputParts[1]
	metadata.TotalTokenCount = saturatingTokenAdd(finalInput, finalOutput)
	metadata.CachedContentTokenCount = scaleTokenSubset(metadata.CachedContentTokenCount, normalized.InputTokens.TotalInputTokens, finalInput)
	scaleGeminiTokenDetails(metadata.PromptTokensDetails, normalized.InputTokens.TotalInputTokens, finalInput)
	scaleGeminiTokenDetails(metadata.ToolUsePromptTokensDetails, normalized.InputTokens.TotalInputTokens, finalInput)
	scaleGeminiTokenDetails(metadata.CandidatesTokensDetails, normalized.OutputTokens, finalOutput)
}

func scaleGeminiTokenDetails(details []dto.GeminiPromptTokensDetails, originalTotal, finalTotal int) {
	for i := range details {
		details[i].TokenCount = scaleTokenSubset(details[i].TokenCount, originalTotal, finalTotal)
	}
}

func proportionalTokenParts(values []int, originalTotal, finalTotal int) []int {
	result := make([]int, len(values))
	if originalTotal <= 0 || finalTotal <= 0 || len(values) == 0 {
		return result
	}
	remaining := finalTotal
	for i, value := range values {
		if i == len(values)-1 {
			result[i] = remaining
			break
		}
		result[i] = scaleTokenSubset(value, originalTotal, finalTotal)
		if result[i] > remaining {
			result[i] = remaining
		}
		remaining -= result[i]
	}
	return result
}

func proportionalTokenSubsets(values []int, originalTotal, finalTotal int) []int {
	result := make([]int, len(values))
	for i, value := range values {
		result[i] = scaleTokenSubset(value, originalTotal, finalTotal)
	}
	return result
}

func scaleTokenSubset(value, originalTotal, finalTotal int) int {
	if value <= 0 || originalTotal <= 0 || finalTotal <= 0 {
		return 0
	}
	result := int((int64(value) * int64(finalTotal)) / int64(originalTotal))
	if result > finalTotal {
		return finalTotal
	}
	return result
}
