package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivateGLM53OfficialCompatibilityIsChannelWide(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic} {
		for _, model := range []string{"glm-5.3", "mapped-name", "custom-alias"} {
			info := &RelayInfo{ChannelMeta: &ChannelMeta{
				ChannelType:       channelType,
				UpstreamModelName: model,
				ChannelOtherSettings: dto.ChannelOtherSettings{
					GLM53OfficialCompatibility: true,
				},
			}}
			info.ActivateGLM53OfficialCompatibility()
			assert.True(t, info.IsGLM53OfficialCompatibility(), "channel=%d model=%s", channelType, model)
		}
	}

	unsupported := &RelayInfo{ChannelMeta: &ChannelMeta{
		ChannelType: constant.ChannelTypeMoonshot,
		ChannelOtherSettings: dto.ChannelOtherSettings{
			GLM53OfficialCompatibility: true,
		},
	}}
	unsupported.ActivateGLM53OfficialCompatibility()
	assert.False(t, unsupported.IsGLM53OfficialCompatibility())
}

func TestGLM53CompatibilityResetsAndConflictsWithKimi(t *testing.T) {
	info := &RelayInfo{ChannelMeta: &ChannelMeta{
		ChannelType: constant.ChannelTypeOpenAI,
		ChannelOtherSettings: dto.ChannelOtherSettings{
			GLM53OfficialCompatibility: true,
		},
	}}
	info.ActivateGLM53OfficialCompatibility()
	require.True(t, info.IsGLM53OfficialCompatibility())
	info.GLM53MatchedStopSequence = "<END>"

	info.ChannelMeta = &ChannelMeta{ChannelType: constant.ChannelTypeOpenAI}
	info.ActivateGLM53OfficialCompatibility()
	assert.False(t, info.IsGLM53OfficialCompatibility())
	assert.Empty(t, info.GLM53MatchedStopSequence)

	info.ChannelMeta = &ChannelMeta{
		ChannelType:       constant.ChannelTypeOpenAI,
		UpstreamModelName: "kimi-k3",
		ChannelOtherSettings: dto.ChannelOtherSettings{
			KimiK3OfficialCompatibility: true,
			GLM53OfficialCompatibility:  true,
		},
	}
	info.ActivateKimiK3OfficialCompatibility()
	info.ActivateGLM53OfficialCompatibility()
	assert.False(t, info.IsOfficialCompatibility())
}
