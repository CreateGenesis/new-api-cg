package relay

import (
	"fmt"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func applyChannelUsageEstimation(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, outputText string, effectiveOutput bool) bool {
	if info == nil || usage == nil {
		return false
	}
	settings, enabled := info.UsageEstimationSettings()
	if !enabled {
		return false
	}
	normalized := service.NormalizeUsageForBilling(usage)
	inputBase := 0
	if info.Request != nil {
		inputBase = service.EstimateModelFamilyInputTokens(settings.ModelFamily, info.RelayFormat, info.Request.GetTokenCountMeta())
	}
	if inputBase <= 0 {
		inputBase = info.GetEstimatePromptTokens()
	}
	outputBase := service.EstimateModelFamilyTextTokens(settings.ModelFamily, outputText)
	if outputBase <= 0 && effectiveOutput {
		outputBase = 1
	}

	inputEstimate, inputClamp := service.ScaleUsageEstimate(inputBase, settings.InputMultiplier)
	outputEstimate, outputClamp := service.ScaleUsageEstimate(outputBase, settings.OutputMultiplier)
	if inputClamp != nil && info.QuotaClamp == nil {
		info.QuotaClamp = inputClamp
	}
	if outputClamp != nil && info.QuotaClamp == nil {
		info.QuotaClamp = outputClamp
	}
	inputApplied, outputApplied := service.ApplyMissingUsageEstimates(usage, inputEstimate, outputEstimate)
	if !inputApplied && !outputApplied {
		return false
	}
	ensureEstimatedBillingUsage(info, usage)
	audit := info.UsageEstimationAudit
	if audit == nil {
		audit = &relaycommon.UsageEstimationAudit{ModelFamily: string(settings.ModelFamily)}
		info.UsageEstimationAudit = audit
	}
	if inputApplied {
		audit.Input = &relaycommon.UsageEstimationDirectionAudit{
			Original:   normalized.InputTokens.TotalInputTokens,
			Base:       inputBase,
			Multiplier: settings.InputMultiplier,
			Final:      inputEstimate,
		}
	}
	if outputApplied {
		audit.Output = &relaycommon.UsageEstimationDirectionAudit{
			Original:   normalized.OutputTokens,
			Base:       outputBase,
			Multiplier: settings.OutputMultiplier,
			Final:      outputEstimate,
		}
	}
	service.MarkRelayDebugUsageEstimation(c)
	logger.LogWarn(c, fmt.Sprintf("channel usage estimation applied: request_id=%s channel_id=%d input=%t output=%t family=%s",
		info.RequestId, info.ChannelId, inputApplied, outputApplied, settings.ModelFamily))
	return true
}

func ensureEstimatedBillingUsage(info *relaycommon.RelayInfo, usage *dto.Usage) {
	if info == nil || usage == nil || usage.BillingUsage != nil {
		return
	}
	normalized := service.NormalizeUsageForBilling(usage)
	switch info.GetFinalRequestRelayFormat() {
	case types.RelayFormatClaude:
		billing := &dto.ClaudeUsage{
			InputTokens:              normalized.InputTokens.UncachedInputTokens,
			CacheReadInputTokens:     normalized.InputTokens.CacheReadInputTokens,
			CacheCreationInputTokens: normalized.InputTokens.CacheCreationInputTokens,
			OutputTokens:             normalized.OutputTokens,
		}
		usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(billing)
	case types.RelayFormatGemini:
		usage.BillingUsage = dto.NewEstimatedGeminiChatBillingUsage(&dto.Usage{
			PromptTokens: normalized.InputTokens.TotalInputTokens, CompletionTokens: normalized.OutputTokens,
		})
	default:
		billing := service.NormalizeUsageForSemantic(usage, service.UsageSemanticOpenAI)
		if info.GetFinalRequestRelayFormat() == types.RelayFormatOpenAIResponses {
			usage.BillingUsage = dto.NewOpenAIResponsesBillingUsage(&billing)
		} else {
			usage.BillingUsage = dto.NewOpenAIChatBillingUsage(&billing)
		}
	}
	if usage.BillingUsage != nil {
		usage.BillingUsage.Estimated = true
	}
}
