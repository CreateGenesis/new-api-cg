package dto

import (
	"math"
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

func TestChannelOtherSettingsSimulatedModelCacheMissingInputEstimateIsOptIn(t *testing.T) {
	var disabled ChannelOtherSettings
	require.NoError(t, common.UnmarshalJsonStr(`{"simulated_model_cache":{}}`, &disabled))
	require.NotNil(t, disabled.SimulatedModelCache)
	assert.False(t, disabled.SimulatedModelCache.EstimateMissingInputTokens)
	assert.False(t, disabled.SimulatedModelCache.IsActive())

	var enabled ChannelOtherSettings
	require.NoError(t, common.UnmarshalJsonStr(`{
		"simulated_model_cache":{"estimate_missing_input_tokens":true}
	}`, &enabled))
	require.NotNil(t, enabled.SimulatedModelCache)
	assert.True(t, enabled.SimulatedModelCache.EstimateMissingInputTokens)
	assert.True(t, enabled.SimulatedModelCache.IsActive())

	encoded, err := common.Marshal(enabled)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"simulated_model_cache":{"estimate_missing_input_tokens":true}
	}`, string(encoded))
}

func TestMissingTokenMultiplierDefaultsAndValidation(t *testing.T) {
	assert.Equal(t, DefaultMissingTokenMultiplier, MissingTokenMultiplier(nil))

	valid := 1.25
	assert.Equal(t, valid, MissingTokenMultiplier(&valid))
	require.NoError(t, ValidateMissingTokenMultiplier("multiplier", &valid))

	for _, invalid := range []float64{0, 100.01, math.NaN(), math.Inf(1)} {
		invalid := invalid
		assert.Equal(t, DefaultMissingTokenMultiplier, MissingTokenMultiplier(&invalid))
		require.Error(t, ValidateMissingTokenMultiplier("multiplier", &invalid))
	}
}

func TestChannelOtherSettingsMissingTokenMultipliersJSONCompatibility(t *testing.T) {
	var settings ChannelOtherSettings
	require.NoError(t, common.UnmarshalJsonStr(`{
		"retry_zero_output":true,
		"disable_non_stream":true,
		"missing_output_token_multiplier":1.5,
		"simulated_model_cache":{"missing_input_token_multiplier":2.25}
	}`, &settings))

	assert.True(t, settings.RetryZeroOutput)
	assert.True(t, settings.DisableNonStream)
	require.NotNil(t, settings.MissingOutputTokenMultiplier)
	assert.Equal(t, 1.5, *settings.MissingOutputTokenMultiplier)
	require.NotNil(t, settings.SimulatedModelCache)
	require.NotNil(t, settings.SimulatedModelCache.MissingInputTokenMultiplier)
	assert.Equal(t, 2.25, *settings.SimulatedModelCache.MissingInputTokenMultiplier)
	require.NoError(t, settings.SimulatedModelCache.Validate())
}

func TestSimulatedModelCacheMultimodalSettingsRequireExplicitWeights(t *testing.T) {
	settings := SimulatedModelCacheSettings{
		Multimodal: &SimulatedModelCacheMultimodalSettings{Enabled: true},
	}

	err := settings.Validate()

	require.ErrorContains(t, err, "image_tokens_per_megapixel")
}

func TestSimulatedModelCacheMultimodalSettingsValidateConfiguredWeights(t *testing.T) {
	rate := 520.5
	fallback := 4096
	settings := SimulatedModelCacheSettings{
		Multimodal: &SimulatedModelCacheMultimodalSettings{
			Enabled:                       true,
			ImageTokensPerMegapixel:       &rate,
			VideoTokensPerSecondMegapixel: &rate,
			AudioTokensPerSecond:          &rate,
			FileTokensPerMiB:              &rate,
			ImageFallbackTokens:           &fallback,
			VideoFallbackTokens:           &fallback,
			AudioFallbackTokens:           &fallback,
			FileFallbackTokens:            &fallback,
		},
	}

	require.NoError(t, settings.Validate())

	invalidRate := 0.0
	settings.Multimodal.ImageTokensPerMegapixel = &invalidRate
	require.ErrorContains(t, settings.Validate(), "image_tokens_per_megapixel")

	settings.Multimodal.ImageTokensPerMegapixel = &rate
	nonFiniteRate := math.Inf(1)
	settings.Multimodal.AudioTokensPerSecond = &nonFiniteRate
	require.ErrorContains(t, settings.Validate(), "audio_tokens_per_second")

	settings.Multimodal.AudioTokensPerSecond = &rate
	invalidFallback := 1_000_001
	settings.Multimodal.FileFallbackTokens = &invalidFallback
	require.ErrorContains(t, settings.Validate(), "file_fallback_tokens")
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

func TestChannelOtherSettingsResponseHeaderTimeoutDefaults(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{"response_header_timeout":{"enabled":true}}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.ResponseHeaderTimeout)

	normalized := settings.ResponseHeaderTimeout.Normalize()

	assert.True(t, normalized.Enabled)
	assert.Equal(t, 180, normalized.TimeoutSeconds)
}

func TestChannelOtherSettingsResponseHeaderTimeoutPreservesConfiguredValue(t *testing.T) {
	var settings ChannelOtherSettings
	err := common.UnmarshalJsonStr(`{
		"response_header_timeout":{"enabled":true,"timeout_seconds":180}
	}`, &settings)
	require.NoError(t, err)
	require.NotNil(t, settings.ResponseHeaderTimeout)

	normalized := settings.ResponseHeaderTimeout.Normalize()

	assert.True(t, normalized.Enabled)
	assert.Equal(t, 180, normalized.TimeoutSeconds)
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
