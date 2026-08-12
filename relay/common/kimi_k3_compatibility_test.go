package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
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
