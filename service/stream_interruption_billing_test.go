package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func interruptedRelayInfo(mode dto.StreamInterruptionBillingMode, outputEnded bool) *relaycommon.RelayInfo {
	status := relaycommon.NewStreamStatus()
	status.RequireProtocolEnd()
	if outputEnded {
		status.MarkProtocolEnd("response.completed")
		status.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	} else {
		status.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	}
	return &relaycommon.RelayInfo{
		IsStream:     true,
		StreamStatus: status,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{
				StreamInterruptionBilling: &dto.StreamInterruptionBillingSettings{Mode: mode},
			},
		},
	}
}

func TestEvaluateStreamInterruptionBillingMatrix(t *testing.T) {
	tests := []struct {
		name         string
		mode         dto.StreamInterruptionBillingMode
		outputTokens int
		normalEnd    bool
		stream       bool
		wantApplied  bool
		wantQuota    int
	}{
		{name: "disabled", outputTokens: 0, stream: true, wantQuota: 900},
		{name: "input only zero output", mode: dto.StreamInterruptionBillingModeInputOnlyFree, outputTokens: 0, stream: true, wantApplied: true, wantQuota: 0},
		{name: "input only partial output", mode: dto.StreamInterruptionBillingModeInputOnlyFree, outputTokens: 3, stream: true, wantQuota: 900},
		{name: "all interrupted zero output", mode: dto.StreamInterruptionBillingModeAllInterruptedFree, outputTokens: 0, stream: true, wantApplied: true, wantQuota: 0},
		{name: "all interrupted partial output", mode: dto.StreamInterruptionBillingModeAllInterruptedFree, outputTokens: 3, stream: true, wantApplied: true, wantQuota: 0},
		{name: "normal completion", mode: dto.StreamInterruptionBillingModeAllInterruptedFree, outputTokens: 3, normalEnd: true, stream: true, wantQuota: 900},
		{name: "non stream", mode: dto.StreamInterruptionBillingModeAllInterruptedFree, outputTokens: 0, wantQuota: 900},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := interruptedRelayInfo(tt.mode, tt.normalEnd)
			info.IsStream = tt.stream
			decision := EvaluateStreamInterruptionBilling(info, tt.outputTokens, 900)

			assert.Equal(t, tt.wantApplied, decision.Applied)
			assert.Equal(t, tt.wantQuota, decision.FinalQuota)
			assert.Equal(t, 900, decision.OriginalQuota)
			assert.Equal(t, tt.outputTokens, decision.OutputTokens)
		})
	}
}

func TestEvaluateStreamInterruptionBillingTreatsClientDisconnectAfterTerminalAsComplete(t *testing.T) {
	info := interruptedRelayInfo(dto.StreamInterruptionBillingModeAllInterruptedFree, true)
	status := relaycommon.NewStreamStatus()
	status.RequireProtocolEnd()
	status.MarkProtocolEnd("finish_reason")
	status.SetEndReason(relaycommon.StreamEndReasonClientGone, nil)
	info.StreamStatus = status

	decision := EvaluateStreamInterruptionBilling(info, 4, 1200)

	assert.False(t, decision.Applied)
	assert.Equal(t, 1200, decision.FinalQuota)
}

func TestEvaluateStreamInterruptionBillingPreservesTieredQuotaBeforeWaiver(t *testing.T) {
	info := interruptedRelayInfo(dto.StreamInterruptionBillingModeAllInterruptedFree, false)

	decision := EvaluateStreamInterruptionBilling(info, 6, 4321)

	require.True(t, decision.Applied)
	assert.Equal(t, 4321, decision.OriginalQuota)
	assert.Zero(t, decision.FinalQuota)
}

func TestAttachStreamInterruptionBillingAddsAdminAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	other := map[string]interface{}{
		"admin_info": map[string]interface{}{"use_channel": []string{"7"}},
	}
	decision := StreamInterruptionBillingDecision{
		Applied:             true,
		Mode:                dto.StreamInterruptionBillingModeAllInterruptedFree,
		OriginalQuota:       321,
		OutputTokens:        8,
		EndReason:           relaycommon.StreamEndReasonTimeout,
		ProtocolEndRequired: true,
	}

	attachStreamInterruptionBilling(ctx, &relaycommon.RelayInfo{UserId: 9, OriginModelName: "gpt-test"}, other, decision)

	adminInfo := other["admin_info"].(map[string]interface{})
	assert.Equal(t, []string{"7"}, adminInfo["use_channel"])
	audit, ok := adminInfo["stream_interruption_billing"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, dto.StreamInterruptionBillingModeAllInterruptedFree, audit["mode"])
	assert.Equal(t, 321, audit["original_quota"])
	assert.Equal(t, 8, audit["output_tokens"])
	assert.Equal(t, relaycommon.StreamEndReasonTimeout, audit["end_reason"])
}

type streamInterruptionBillingSettler struct {
	preConsumed int
	settled     int
}

func (s *streamInterruptionBillingSettler) Settle(actualQuota int) error {
	s.settled = actualQuota
	return nil
}

func (s *streamInterruptionBillingSettler) Refund(*gin.Context) {}
func (s *streamInterruptionBillingSettler) NeedsRefund() bool   { return true }
func (s *streamInterruptionBillingSettler) GetPreConsumedQuota() int {
	return s.preConsumed
}
func (s *streamInterruptionBillingSettler) Reserve(int) error { return nil }

func TestStreamInterruptionBillingSettlesPreConsumedQuotaToZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	settler := &streamInterruptionBillingSettler{preConsumed: 500}
	info := interruptedRelayInfo(dto.StreamInterruptionBillingModeAllInterruptedFree, false)
	info.Billing = settler

	decision := EvaluateStreamInterruptionBilling(info, 12, 700)
	require.True(t, decision.Applied)
	require.NoError(t, SettleBilling(ctx, info, decision.FinalQuota))

	assert.Equal(t, 0, settler.settled)
}
