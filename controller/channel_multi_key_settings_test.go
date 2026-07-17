package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddChannelRejectsInvalidLeastRequestsWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload, err := common.Marshal(AddChannelRequest{
		Mode:                               "multi_to_single",
		MultiKeyMode:                       constant.MultiKeyModeLeastRequests,
		MultiKeyLeastRequestsWindowSeconds: 15,
		Channel: &model.Channel{
			Type:   constant.ChannelTypeOpenAI,
			Name:   "least-requests",
			Key:    "key-a\nkey-b",
			Models: "gpt-4o",
			Group:  "default",
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")

	AddChannel(ctx)

	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Contains(t, response.Message, "multiple of 10")
}

func TestAddChannelRejectsInvalidCacheAffinityThreshold(t *testing.T) {
	gin.SetMode(gin.TestMode)
	threshold := 101
	payload, err := common.Marshal(AddChannelRequest{
		Mode:                                  "multi_to_single",
		MultiKeyMode:                          constant.MultiKeyModeCacheAffinityLeastRequests,
		MultiKeyLeastRequestsWindowSeconds:    60,
		MultiKeyCacheAffinityThresholdPercent: &threshold,
		Channel: &model.Channel{
			Type:   constant.ChannelTypeOpenAI,
			Name:   "cache-aware",
			Key:    "key-a\nkey-b",
			Models: "gpt-4o",
			Group:  "default",
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")

	AddChannel(ctx)

	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Contains(t, response.Message, "between 0 and 100")
}

func TestMergeChannelOverloadSettingsPreservesOmittedConfig(t *testing.T) {
	target := model.ChannelInfo{
		ChannelOverloadProtection:  model.OverloadProtection{Enabled: true, RequestsPerSecond: 2},
		MultiKeyOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 3},
	}
	incoming := model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, RequestsPerMinute: 20},
	}

	mergeChannelOverloadSettings(&target, incoming, map[string]any{
		"channel_info": map[string]any{"channel_overload_protection": map[string]any{}},
	})

	assert.Equal(t, 20, target.ChannelOverloadProtection.RequestsPerMinute)
	assert.True(t, target.MultiKeyOverloadProtection.Enabled)
	assert.Equal(t, 3, target.MultiKeyOverloadProtection.ConcurrentRequests)
}

func TestMergeChannelOverloadSettingsNormalizesRecoveryAndChannelTPM(t *testing.T) {
	target := model.ChannelInfo{IsMultiKey: true}
	incoming := model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{
			Enabled: true, RequestsPerSecond: 2, TokensPerMinute: 999,
		},
		MultiKeyOverloadProtection: model.OverloadProtection{
			Enabled: true, TokensPerMinute: 1000,
		},
	}

	mergeChannelOverloadSettings(&target, incoming, map[string]any{
		"channel_info": map[string]any{
			"channel_overload_protection":   map[string]any{},
			"multi_key_overload_protection": map[string]any{},
		},
	})
	require.NoError(t, model.ValidateAndNormalizeChannelInfo(&target))

	assert.Zero(t, target.ChannelOverloadProtection.TokensPerMinute)
	assert.Equal(t, model.DefaultOverloadRecoverySeconds, target.ChannelOverloadProtection.RecoverySeconds)
	assert.Equal(t, int64(1000), target.MultiKeyOverloadProtection.TokensPerMinute)
	assert.Equal(t, model.DefaultOverloadRecoverySeconds, target.MultiKeyOverloadProtection.RecoverySeconds)
}

func TestChannelOverloadSettingsProvided(t *testing.T) {
	assert.False(t, channelOverloadSettingsProvided(map[string]any{}))
	assert.False(t, channelOverloadSettingsProvided(map[string]any{
		"channel_info": map[string]any{"multi_key_mode": "random"},
	}))
	assert.True(t, channelOverloadSettingsProvided(map[string]any{
		"channel_info": map[string]any{"channel_overload_protection": map[string]any{}},
	}))
	assert.True(t, channelOverloadSettingsProvided(map[string]any{
		"channel_info": map[string]any{"multi_key_overload_protection": map[string]any{}},
	}))
}

func TestChannelOverloadSettingsChanged(t *testing.T) {
	origin := model.ChannelInfo{
		ChannelOverloadProtection:  model.OverloadProtection{Enabled: true, RequestsPerSecond: 1, RecoverySeconds: 2},
		MultiKeyOverloadProtection: model.OverloadProtection{Enabled: true, TokensPerMinute: 100, RecoverySeconds: 2},
	}
	requestData := map[string]any{
		"channel_info": map[string]any{
			"channel_overload_protection":   map[string]any{},
			"multi_key_overload_protection": map[string]any{},
		},
	}

	assert.False(t, channelOverloadSettingsChanged(origin, origin, requestData))
	changedChannel := origin
	changedChannel.ChannelOverloadProtection.RequestsPerSecond = 2
	assert.True(t, channelOverloadSettingsChanged(origin, changedChannel, requestData))
	changedKey := origin
	changedKey.MultiKeyOverloadProtection.TokensPerMinute = 200
	assert.True(t, channelOverloadSettingsChanged(origin, changedKey, requestData))
	assert.False(t, channelOverloadSettingsChanged(origin, changedKey, map[string]any{
		"channel_info": map[string]any{"multi_key_mode": "random"},
	}))
}
