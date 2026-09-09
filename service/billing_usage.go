package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

const (
	usageBillingPathLocal              = "local"
	usageBillingPathUpstream           = "upstream"
	usageBillingPathOpenAI             = "billing-usage-openai"
	usageBillingPathOpenAIEstimated    = "billing-usage-openai-estimated"
	usageBillingPathAnthropic          = "billing-usage-anthropic"
	usageBillingPathAnthropicEstimated = "billing-usage-anthropic-estimated"
	usageBillingPathGemini             = "billing-usage-gemini"
	usageBillingPathGeminiEstimated    = "billing-usage-gemini-estimated"
)

func effectiveBillingUsage(usage *dto.Usage) *dto.Usage {
	if billingUsage, ok := usageFromBillingUsage(usage); ok {
		return billingUsage
	}
	return usage
}

func usageBillingPathForLog(isLocalCountTokens bool, usage *dto.Usage) string {
	effectiveUsage, ok := usageFromBillingUsage(usage)
	if !ok {
		if isLocalCountTokens {
			return usageBillingPathLocal
		}
		return usageBillingPathUpstream
	}

	switch effectiveUsage.UsageSemantic {
	case dto.BillingUsageSemanticOpenAI:
		if usage.BillingUsage.Estimated {
			return usageBillingPathOpenAIEstimated
		}
		return usageBillingPathOpenAI
	case dto.BillingUsageSemanticAnthropic:
		if usage.BillingUsage.Estimated {
			return usageBillingPathAnthropicEstimated
		}
		return usageBillingPathAnthropic
	case dto.BillingUsageSemanticGemini:
		if usage.BillingUsage.Estimated {
			return usageBillingPathGeminiEstimated
		}
		return usageBillingPathGemini
	}

	return usageBillingPathUpstream
}

func appendUsageBillingPathForLog(other *model.LogOther, isLocalCountTokens bool, usage *dto.Usage) {
	if other == nil {
		return
	}
	other.SetAdmin("usage_billing_path", usageBillingPathForLog(isLocalCountTokens, usage))
}

func AttachUsageNormalizationAudit(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other *model.LogOther, normalization BillingUsageNormalization) {
	if other == nil || !relayInfo.CacheUsageValidationSplitEnabled() {
		return
	}
	other.SetAdmin("usage_normalization", normalization.Audit)

	if normalization.Audit.Source != UsageNormalizationSourceFallback &&
		normalization.Audit.Status != UsageNormalizationStatusMismatch {
		return
	}
	requestID := ""
	channelID := 0
	modelName := ""
	if relayInfo != nil {
		requestID = relayInfo.RequestId
		channelID = relayInfo.ChannelId
		modelName = relayInfo.OriginModelName
	}
	logger.LogWarn(ctx, fmt.Sprintf(
		"upstream usage normalization fallback or mismatch: request_id=%s channel_id=%d model=%s mode=%s source=%s status=%s input=%d output=%d total=%d cache_read=%d cache_creation=%d normalized_input=%d normalized_total_input=%d",
		requestID,
		channelID,
		modelName,
		normalization.Audit.Mode,
		normalization.Audit.Source,
		normalization.Audit.Status,
		normalization.Audit.ReportedInputTokens,
		normalization.Audit.ReportedOutputTokens,
		normalization.Audit.ReportedTotalTokens,
		normalization.Audit.CacheReadInputTokens,
		normalization.Audit.CacheCreationInputTokens,
		normalization.Audit.NormalizedUncachedInputTokens,
		normalization.Audit.NormalizedTotalInputTokens,
	))
}

func usageFromBillingUsage(usage *dto.Usage) (*dto.Usage, bool) {
	if usage == nil || usage.BillingUsage == nil {
		return nil, false
	}
	billingUsage := usage.BillingUsage
	source := strings.TrimSpace(billingUsage.Source)
	semantic := strings.TrimSpace(billingUsage.Semantic)

	if dto.HasOpenAIUsageTokens(billingUsage.OpenAIUsage) &&
		(strings.EqualFold(source, dto.BillingUsageSourceOAIChat) ||
			strings.EqualFold(source, dto.BillingUsageSourceOAIResponses) ||
			strings.EqualFold(semantic, dto.BillingUsageSemanticOpenAI)) {
		return usageFromOpenAIBillingUsage(billingUsage), true
	}

	if dto.HasClaudeUsageTokens(billingUsage.ClaudeUsage) &&
		(strings.EqualFold(source, dto.BillingUsageSourceClaudeMessages) ||
			strings.EqualFold(semantic, dto.BillingUsageSemanticAnthropic)) {
		return usageFromClaudeBillingUsage(billingUsage), true
	}

	if dto.HasGeminiUsageMetadataTokens(billingUsage.GeminiUsageMetadata) &&
		(strings.EqualFold(source, dto.BillingUsageSourceGeminiChat) ||
			strings.EqualFold(semantic, dto.BillingUsageSemanticGemini)) {
		return usageFromGeminiBillingUsage(billingUsage), true
	}

	return nil, false
}

func usageFromOpenAIBillingUsage(billingUsage *dto.BillingUsage) *dto.Usage {
	canonical, ok := billingUsage.CanonicalUsage()
	if !ok {
		return nil
	}
	usage := *canonical
	if billingUsage.OpenAIUsage.TotalTokens == 0 {
		usage.TotalTokens = saturatingTokenAdd(usage.PromptTokens, usage.CompletionTokens)
	}
	return &usage
}

func usageFromClaudeBillingUsage(billingUsage *dto.BillingUsage) *dto.Usage {
	claudeUsage := billingUsage.ClaudeUsage
	cacheCreation5m := claudeUsage.GetCacheCreation5mTokens()
	if cacheCreation5m == 0 {
		cacheCreation5m = claudeUsage.ClaudeCacheCreation5mTokens
	}
	cacheCreation1h := claudeUsage.GetCacheCreation1hTokens()
	if cacheCreation1h == 0 {
		cacheCreation1h = claudeUsage.ClaudeCacheCreation1hTokens
	}

	totalInputTokens := saturatingTokenAdd(
		claudeUsage.InputTokens,
		claudeUsage.CacheReadInputTokens,
		claudeUsage.CacheCreationInputTokens,
	)
	usage := &dto.Usage{
		PromptTokens:                claudeUsage.InputTokens,
		CompletionTokens:            claudeUsage.OutputTokens,
		TotalTokens:                 saturatingTokenAdd(totalInputTokens, claudeUsage.OutputTokens),
		InputTokens:                 claudeUsage.InputTokens,
		OutputTokens:                claudeUsage.OutputTokens,
		UsageSemantic:               dto.BillingUsageSemanticAnthropic,
		UsageSource:                 dto.BillingUsageSourceClaudeMessages,
		BillingUsage:                dto.CloneBillingUsage(billingUsage),
		ClaudeCacheCreation5mTokens: cacheCreation5m,
		ClaudeCacheCreation1hTokens: cacheCreation1h,
	}
	usage.PromptTokensDetails.CachedTokens = claudeUsage.CacheReadInputTokens
	usage.PromptTokensDetails.CachedCreationTokens = claudeUsage.CacheCreationInputTokens
	return usage
}

func usageFromGeminiBillingUsage(billingUsage *dto.BillingUsage) *dto.Usage {
	metadata := *billingUsage.GeminiUsageMetadata
	promptTokens := saturatingTokenAdd(metadata.PromptTokenCount, metadata.ToolUsePromptTokenCount)
	usage := &dto.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: saturatingTokenAdd(metadata.CandidatesTokenCount, metadata.ThoughtsTokenCount),
		TotalTokens:      metadata.TotalTokenCount,
		UsageSemantic:    dto.BillingUsageSemanticGemini,
		UsageSource:      dto.BillingUsageSourceGeminiChat,
		BillingUsage:     dto.CloneBillingUsage(billingUsage),
	}
	usage.CompletionTokenDetails.ReasoningTokens = metadata.ThoughtsTokenCount
	usage.PromptTokensDetails.CachedTokens = metadata.CachedContentTokenCount

	for _, detail := range metadata.PromptTokensDetails {
		addGeminiInputTokenDetail(&usage.PromptTokensDetails, detail)
	}
	for _, detail := range metadata.ToolUsePromptTokensDetails {
		addGeminiInputTokenDetail(&usage.PromptTokensDetails, detail)
	}
	for _, detail := range metadata.CandidatesTokensDetails {
		switch detail.Modality {
		case "IMAGE":
			usage.CompletionTokenDetails.ImageTokens = saturatingTokenAdd(usage.CompletionTokenDetails.ImageTokens, detail.TokenCount)
		case "AUDIO":
			usage.CompletionTokenDetails.AudioTokens = saturatingTokenAdd(usage.CompletionTokenDetails.AudioTokens, detail.TokenCount)
		case "TEXT":
			usage.CompletionTokenDetails.TextTokens = saturatingTokenAdd(usage.CompletionTokenDetails.TextTokens, detail.TokenCount)
		}
	}

	if usage.TotalTokens == 0 {
		usage.TotalTokens = saturatingTokenAdd(usage.PromptTokens, usage.CompletionTokens)
	} else if usage.CompletionTokens <= 0 {
		usage.CompletionTokens = usage.TotalTokens - usage.PromptTokens
	}
	if usage.PromptTokens > 0 && usage.PromptTokensDetails.TextTokens == 0 && usage.PromptTokensDetails.AudioTokens == 0 {
		usage.PromptTokensDetails.TextTokens = usage.PromptTokens
	}
	return usage
}

func addGeminiInputTokenDetail(details *dto.InputTokenDetails, detail dto.GeminiPromptTokensDetails) {
	switch detail.Modality {
	case "AUDIO":
		details.AudioTokens = saturatingTokenAdd(details.AudioTokens, detail.TokenCount)
	case "IMAGE":
		details.ImageTokens = saturatingTokenAdd(details.ImageTokens, detail.TokenCount)
	case "TEXT":
		details.TextTokens = saturatingTokenAdd(details.TextTokens, detail.TokenCount)
	}
}
