package dto

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelOtherSettingsSimulatedModelCacheDefaults(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{"simulated_model_cache":{"enabled":true}}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.SimulatedModelCache)

	normalized := settings.SimulatedModelCache.Normalize()

	assert.True(t, normalized.Enabled)
	assert.True(t, normalized.IsActive())
	assert.Equal(t, 86400, normalized.TTLSeconds)
	assert.Equal(t, 0.01, normalized.MinMatchRatio)
}

func TestChannelOtherSettingsSimulatedModelCacheIgnoresLegacyReplaySettings(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{
		"simulated_model_cache":{
			"enabled":false,
			"exact_replay_enabled":true,
			"reuse_limit":5
		}
	}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.SimulatedModelCache)

	normalized := settings.SimulatedModelCache.Normalize()

	assert.False(t, normalized.Enabled)
	assert.False(t, normalized.IsActive())
}

func TestChannelOtherSettingsSimulatedModelCacheKeepsExplicitValues(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{
		"simulated_model_cache":{
			"enabled":true,
			"ttl_seconds":60,
			"reuse_limit":5,
			"min_match_ratio":0.42
		}
	}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.SimulatedModelCache)

	normalized := settings.SimulatedModelCache.Normalize()

	assert.Equal(t, 60, normalized.TTLSeconds)
	assert.Equal(t, 0.42, normalized.MinMatchRatio)
}

func TestChannelOtherSettingsStatusCodeRetryDefaults(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{"status_code_retry":{"enabled":true}}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.StatusCodeRetry)

	normalized := settings.StatusCodeRetry.Normalize()

	assert.True(t, normalized.Enabled)
	assert.Equal(t, 10, normalized.RetryTimes)
	assert.Equal(t, 50, normalized.RetryIntervalMS)
	assert.Equal(t, "100-199,300-399,401-407,409-499,500-503,505-523,525-599", normalized.StatusCodes)
}

func TestChannelOtherSettingsStatusCodeRetryKeepsExplicitValues(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{
		"status_code_retry":{
			"enabled":true,
			"retry_times":30,
			"retry_interval_ms":250,
			"status_codes":"429,500-501"
		}
	}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.StatusCodeRetry)

	normalized := settings.StatusCodeRetry.Normalize()

	assert.True(t, normalized.Enabled)
	assert.Equal(t, 30, normalized.RetryTimes)
	assert.Equal(t, 250, normalized.RetryIntervalMS)
	assert.Equal(t, "429,500-501", normalized.StatusCodes)
}

func TestChannelOtherSettingsStatusCodeRetryPreservesExplicitZeroRetries(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{
		"status_code_retry":{
			"enabled":true,
			"retry_times":0,
			"status_codes":"429"
		}
	}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.StatusCodeRetry)

	normalized := settings.StatusCodeRetry.Normalize()

	assert.Equal(t, 0, normalized.RetryTimes)
	assert.Equal(t, "429", normalized.StatusCodes)
}

func TestChannelOtherSettingsInputTokenRoutingDefaults(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{"input_token_routing":{"enabled":true}}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.InputTokenRouting)

	normalized := settings.InputTokenRouting.Normalize()

	assert.True(t, normalized.Enabled)
	assert.Equal(t, 0, normalized.MinTokens)
	assert.Equal(t, 0, normalized.MaxTokens)
	assert.Empty(t, normalized.Ranges)
}

func TestChannelOtherSettingsInputTokenRoutingClampsInvalidBounds(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{
		"input_token_routing":{
			"enabled":true,
			"min_tokens":-10,
			"max_tokens":-20
		}
	}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.InputTokenRouting)

	normalized := settings.InputTokenRouting.Normalize()

	assert.Equal(t, 0, normalized.MinTokens)
	assert.Equal(t, 0, normalized.MaxTokens)
}

func TestChannelOtherSettingsInputTokenRoutingNormalizesRanges(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{
		"input_token_routing":{
			"enabled":true,
			"ranges":[
				{"min_tokens":0,"max_tokens":200},
				{"min_tokens":5000000,"max_tokens":1000000},
				{"min_tokens":-10,"max_tokens":-1}
			]
		}
	}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.InputTokenRouting)

	normalized := settings.InputTokenRouting.Normalize()

	require.Len(t, normalized.Ranges, 2)
	assert.Equal(t, InputTokenRoutingRange{MinTokens: 0, MaxTokens: 200}, normalized.Ranges[0])
	assert.Equal(t, InputTokenRoutingRange{MinTokens: 1000000, MaxTokens: 5000000}, normalized.Ranges[1])
}
