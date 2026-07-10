package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateTextOtherInfoRecordsSimulatedCacheStreamUsageInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	injected := false
	now := time.Now()
	info := &relaycommon.RelayInfo{
		StartTime:         now,
		FirstResponseTime: now,
		ChannelMeta:       &relaycommon.ChannelMeta{},
		SimulatedModelCacheInfo: &relaycommon.SimulatedModelCacheInfo{
			Mode:                  "partial_fingerprint",
			MatchRatio:            0.5,
			OriginalPromptTokens:  100,
			SimulatedPromptTokens: 50,
			SimulatedCachedTokens: 50,
			StreamUsageInjected:   &injected,
			FingerprintVersion:    "v2",
			CandidateCount:        12,
			MatchDurationMS:       7,
		},
	}

	other := GenerateTextOtherInfo(c, info, 1, 1, 1, 50, 0.5, 0, 1)

	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	cacheInfo, ok := adminInfo["simulated_model_cache"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, cacheInfo["stream_usage_injected"])
	assert.Equal(t, 50, cacheInfo["simulated_cached_tokens"])
	assert.Equal(t, "v2", cacheInfo["fingerprint_version"])
	assert.Equal(t, 12, cacheInfo["candidate_count"])
	assert.Equal(t, int64(7), cacheInfo["match_duration_ms"])
}

func TestGenerateTextOtherInfoRecordsSimulatedCacheBypassWithoutHitFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	now := time.Now()
	info := &relaycommon.RelayInfo{
		StartTime:         now,
		FirstResponseTime: now,
		ChannelMeta:       &relaycommon.ChannelMeta{},
		SimulatedModelCacheInfo: &relaycommon.SimulatedModelCacheInfo{
			FingerprintVersion: "v2",
			CandidateCount:     100,
			MatchDurationMS:    3,
			BypassReason:       "memory_budget",
		},
	}

	other := GenerateTextOtherInfo(c, info, 1, 1, 1, 50, 0.5, 0, 1)

	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	cacheInfo, ok := adminInfo["simulated_model_cache"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "memory_budget", cacheInfo["bypass_reason"])
	assert.NotContains(t, cacheInfo, "mode")
	assert.NotContains(t, cacheInfo, "match_ratio")
	assert.NotContains(t, cacheInfo, "simulated_cached_tokens")
}
