package service

import (
	"context"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

func recordMultiKeyOverloadUsage(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) {
	recordMultiKeyOverloadTokens(ctx, relayInfo, reliableUsageTotal(usage))
}

func recordMultiKeyOverloadRealtimeUsage(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.RealtimeUsage) {
	if usage == nil {
		return
	}
	recordMultiKeyOverloadTokens(ctx, relayInfo, normalizedRealtimeUsageTotal(usage))
}

func recordUserRequestLimitUsage(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) {
	if ctx == nil || relayInfo == nil || setting.GetUserTokensPerMinuteLimit() <= 0 || usage == nil {
		return
	}
	recordUserRequestLimitTokensForRelay(ctx, relayInfo, int64(NormalizeUsageForBilling(usage).TotalTokens))
}

func recordUserRequestLimitRealtimeUsage(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.RealtimeUsage) {
	if ctx == nil || relayInfo == nil || setting.GetUserTokensPerMinuteLimit() <= 0 || usage == nil {
		return
	}
	recordUserRequestLimitTokensForRelay(ctx, relayInfo, normalizedRealtimeUsageTotal(usage))
}

func recordUserRequestLimitTokensForRelay(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, tokens int64) {
	if common.GetContextKeyBool(ctx, constant.ContextKeyUserRateLimitTokens) {
		return
	}
	if tokens <= 0 {
		return
	}
	common.SetContextKey(ctx, constant.ContextKeyUserRateLimitTokens, true)
	recordContext := context.Background()
	if ctx.Request != nil {
		recordContext = ctx.Request.Context()
	}
	if err := RecordUserRequestLimitTokens(recordContext, relayInfo.UserId, tokens); err != nil {
		common.SysError(fmt.Sprintf("record user request limit tokens failed: user_id=%d, err=%v", relayInfo.UserId, err))
	}
}

func recordMultiKeyOverloadTokens(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, tokens int64) {
	if ctx == nil || relayInfo == nil || relayInfo.ChannelMeta == nil || !relayInfo.ChannelIsMultiKey || tokens <= 0 {
		return
	}
	if common.GetContextKeyBool(ctx, constant.ContextKeyChannelOverloadTokens) {
		return
	}
	common.SetContextKey(ctx, constant.ContextKeyChannelOverloadTokens, true)
	recordContext := context.Background()
	if ctx.Request != nil {
		recordContext = ctx.Request.Context()
	}
	if err := RecordChannelOverloadTokens(recordContext, relayInfo.ChannelId, relayInfo.ChannelMultiKeyIndex, tokens); err != nil {
		common.SysError(fmt.Sprintf("record channel overload tokens failed: channel_id=%d, key_index=%d, err=%v", relayInfo.ChannelId, relayInfo.ChannelMultiKeyIndex, err))
	}
}

func normalizedUsageTotal(usage *dto.Usage) int64 {
	if usage == nil {
		return 0
	}
	if total := positiveInt64(usage.TotalTokens); total > 0 {
		return total
	}
	if total := saturatingTokenSum(usage.InputTokens, usage.OutputTokens); total > 0 {
		return total
	}
	return saturatingTokenSum(usage.PromptTokens, usage.CompletionTokens)
}

func reliableUsageTotal(usage *dto.Usage) int64 {
	if usage == nil || usage.Estimated {
		return 0
	}
	return normalizedUsageTotal(usage)
}

func normalizedRealtimeUsageTotal(usage *dto.RealtimeUsage) int64 {
	if usage == nil {
		return 0
	}
	if total := positiveInt64(usage.TotalTokens); total > 0 {
		return total
	}
	return saturatingTokenSum(usage.InputTokens, usage.OutputTokens)
}

func saturatingTokenSum(left, right int) int64 {
	leftValue := positiveInt64(left)
	rightValue := positiveInt64(right)
	if leftValue > math.MaxInt64-rightValue {
		return math.MaxInt64
	}
	return leftValue + rightValue
}

func positiveInt64(value int) int64 {
	if value <= 0 {
		return 0
	}
	return int64(value)
}
