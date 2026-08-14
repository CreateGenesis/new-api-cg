package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivateKimiK3OfficialCompatibilityRequiresExactMappedModelAndChannel(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic, constant.ChannelTypeMoonshot} {
		info := &RelayInfo{ChannelMeta: &ChannelMeta{
			ChannelType:       channelType,
			UpstreamModelName: "kimi-k3",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				KimiK3OfficialCompatibility: true,
			},
		}}
		info.ActivateKimiK3OfficialCompatibility()
		assert.True(t, info.IsKimiK3OfficialCompatibility())
		assert.True(t, info.CacheUsageValidationSplitEnabled())
	}

	for _, model := range []string{"k3", "k3-256k", "kimi-k3[1m]", "kimi-for-coding"} {
		info := &RelayInfo{ChannelMeta: &ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: model,
			ChannelOtherSettings: dto.ChannelOtherSettings{
				KimiK3OfficialCompatibility: true,
			},
		}}
		info.ActivateKimiK3OfficialCompatibility()
		assert.False(t, info.IsKimiK3OfficialCompatibility(), model)
	}
}

func TestActivateKimiK3OfficialCompatibilityResetsAttemptStateOnChannelSwitch(t *testing.T) {
	info := &RelayInfo{ChannelMeta: &ChannelMeta{
		ChannelType:       constant.ChannelTypeAnthropic,
		UpstreamModelName: "kimi-k3",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			KimiK3OfficialCompatibility: true,
		},
	}}

	info.ActivateKimiK3OfficialCompatibility()
	require.True(t, info.IsKimiK3OfficialCompatibility())
	info.KimiK3HideThinking = true
	info.KimiK3BillingAudit = &dto.KimiK3BillingAudit{Equation: "test"}
	info.KimiK3MatchedStopSequence = "<stop>"

	info.ChannelMeta = &ChannelMeta{
		ChannelType:       constant.ChannelTypeOpenAI,
		UpstreamModelName: "k3",
	}
	info.ActivateKimiK3OfficialCompatibility()

	assert.False(t, info.IsKimiK3OfficialCompatibility())
	assert.False(t, info.KimiK3HideThinking)
	assert.Nil(t, info.KimiK3BillingAudit)
	assert.Empty(t, info.KimiK3MatchedStopSequence)

	info.ChannelMeta = &ChannelMeta{
		ChannelType:       constant.ChannelTypeOpenAI,
		UpstreamModelName: "kimi-k3",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			KimiK3OfficialCompatibility: true,
		},
	}
	info.ActivateKimiK3OfficialCompatibility()

	assert.True(t, info.IsKimiK3OfficialCompatibility())
}
