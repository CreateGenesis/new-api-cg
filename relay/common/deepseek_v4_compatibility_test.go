package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
)

func TestActivateDeepSeekV4OfficialCompatibilityUsesPublicOrMappedModel(t *testing.T) {
	for _, tt := range []struct {
		name          string
		channelType   int
		originModel   string
		upstreamModel string
	}{
		{name: "OpenAI mapped model", channelType: constant.ChannelTypeOpenAI, originModel: "deepseek-v4-flash-0731", upstreamModel: "deepseek-v4-flash"},
		{name: "Anthropic TNT model", channelType: constant.ChannelTypeAnthropic, originModel: "deepseek-v4-pro-0813", upstreamModel: "deepseek-v4-pro"},
		{name: "DeepSeek native model", channelType: constant.ChannelTypeDeepSeek, originModel: "deepseek-v4-flash", upstreamModel: "deepseek-v4-flash"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := &RelayInfo{
				OriginModelName: tt.originModel,
				ChannelMeta: &ChannelMeta{
					ChannelType:       tt.channelType,
					UpstreamModelName: tt.upstreamModel,
					ChannelOtherSettings: dto.ChannelOtherSettings{
						DeepSeekV4OfficialCompatibility: true,
					},
				},
			}

			info.ActivateDeepSeekV4OfficialCompatibility()

			assert.True(t, info.IsDeepSeekV4OfficialCompatibility())
			assert.True(t, info.RequiresRequestConversion())
			assert.False(t, info.IsOfficialCompatibility(), "DeepSeek must not enter Kimi/GLM response policies")
		})
	}
}

func TestActivateDeepSeekV4OfficialCompatibilityRejectsUnrelatedOrConflictingChannels(t *testing.T) {
	tests := []struct {
		name     string
		channel  int
		model    string
		settings dto.ChannelOtherSettings
	}{
		{name: "setting disabled", channel: constant.ChannelTypeOpenAI, model: "deepseek-v4-flash"},
		{name: "unrelated model", channel: constant.ChannelTypeOpenAI, model: "gpt-5", settings: dto.ChannelOtherSettings{DeepSeekV4OfficialCompatibility: true}},
		{name: "unsupported channel", channel: constant.ChannelTypeGemini, model: "deepseek-v4-flash", settings: dto.ChannelOtherSettings{DeepSeekV4OfficialCompatibility: true}},
		{name: "Kimi conflict", channel: constant.ChannelTypeOpenAI, model: "deepseek-v4-flash", settings: dto.ChannelOtherSettings{DeepSeekV4OfficialCompatibility: true, KimiK3OfficialCompatibility: true}},
		{name: "GLM conflict", channel: constant.ChannelTypeAnthropic, model: "deepseek-v4-flash", settings: dto.ChannelOtherSettings{DeepSeekV4OfficialCompatibility: true, GLM53OfficialCompatibility: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &RelayInfo{
				OriginModelName: tt.model,
				ChannelMeta: &ChannelMeta{
					ChannelType:          tt.channel,
					UpstreamModelName:    tt.model,
					ChannelOtherSettings: tt.settings,
				},
			}
			info.ActivateDeepSeekV4OfficialCompatibility()
			assert.False(t, info.IsDeepSeekV4OfficialCompatibility())
		})
	}
}
