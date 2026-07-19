package service

import (
	"fmt"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

type StreamInterruptionBillingDecision struct {
	Applied             bool
	Mode                dto.StreamInterruptionBillingMode
	OriginalQuota       int
	FinalQuota          int
	OutputTokens        int
	EndReason           relaycommon.StreamEndReason
	ProtocolEndRequired bool
	ProtocolEndReceived bool
	ProtocolEndEvent    string
}

func EvaluateStreamInterruptionBilling(relayInfo *relaycommon.RelayInfo, outputTokens int, calculatedQuota int) StreamInterruptionBillingDecision {
	decision := StreamInterruptionBillingDecision{
		OriginalQuota: calculatedQuota,
		FinalQuota:    calculatedQuota,
		OutputTokens:  outputTokens,
	}
	if relayInfo == nil || !relayInfo.IsStream || relayInfo.ChannelMeta == nil || relayInfo.StreamStatus == nil {
		return decision
	}

	settings := relayInfo.ChannelOtherSettings.StreamInterruptionBilling
	if settings == nil {
		return decision
	}
	decision.Mode = settings.Mode
	if settings.Mode != dto.StreamInterruptionBillingModeInputOnlyFree &&
		settings.Mode != dto.StreamInterruptionBillingModeAllInterruptedFree {
		return decision
	}

	snapshot := relayInfo.StreamStatus.Snapshot()
	decision.EndReason = snapshot.EndReason
	decision.ProtocolEndRequired = snapshot.ProtocolEndRequired
	decision.ProtocolEndReceived = snapshot.ProtocolEndReceived
	decision.ProtocolEndEvent = snapshot.ProtocolEndEvent
	if !relayInfo.StreamStatus.IsInterrupted() {
		return decision
	}
	if settings.Mode == dto.StreamInterruptionBillingModeInputOnlyFree && outputTokens != 0 {
		return decision
	}

	decision.Applied = true
	decision.FinalQuota = 0
	return decision
}

func attachStreamInterruptionBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other map[string]interface{}, decision StreamInterruptionBillingDecision) {
	if !decision.Applied || other == nil {
		return
	}
	adminInfo, ok := other["admin_info"].(map[string]interface{})
	if !ok || adminInfo == nil {
		adminInfo = map[string]interface{}{}
		other["admin_info"] = adminInfo
	}
	adminInfo["stream_interruption_billing"] = map[string]interface{}{
		"mode":                  decision.Mode,
		"original_quota":        decision.OriginalQuota,
		"output_tokens":         decision.OutputTokens,
		"end_reason":            decision.EndReason,
		"protocol_end_required": decision.ProtocolEndRequired,
		"protocol_end_received": decision.ProtocolEndReceived,
		"protocol_end_event":    decision.ProtocolEndEvent,
	}

	userID := 0
	modelName := ""
	if relayInfo != nil {
		userID = relayInfo.UserId
		modelName = relayInfo.OriginModelName
	}
	logger.LogWarn(ctx, fmt.Sprintf("stream interruption billing waived: mode=%s original_quota=%d output_tokens=%d end_reason=%s protocol_end_event=%s user=%d model=%s",
		decision.Mode, decision.OriginalQuota, decision.OutputTokens, decision.EndReason, decision.ProtocolEndEvent, userID, modelName))
}
