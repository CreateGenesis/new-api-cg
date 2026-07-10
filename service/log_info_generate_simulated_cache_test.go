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
			Mode:                  "partial_rewrite",
			MatchRatio:            0.5,
			OriginalPromptTokens:  100,
			SimulatedPromptTokens: 50,
			SimulatedCachedTokens: 50,
			StreamUsageInjected:   &injected,
		},
	}

	other := GenerateTextOtherInfo(c, info, 1, 1, 1, 50, 0.5, 0, 1)

	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	cacheInfo, ok := adminInfo["simulated_model_cache"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, cacheInfo["stream_usage_injected"])
	assert.Equal(t, 50, cacheInfo["simulated_cached_tokens"])
}
