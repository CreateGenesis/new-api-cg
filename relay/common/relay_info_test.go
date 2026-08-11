package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoCacheUsageValidationSplitEnabledIsChannelScoped(t *testing.T) {
	var nilInfo *RelayInfo
	require.False(t, nilInfo.CacheUsageValidationSplitEnabled())
	require.False(t, (&RelayInfo{}).CacheUsageValidationSplitEnabled())
	require.True(t, (&RelayInfo{
		ChannelMeta: &ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{CacheUsageValidationSplit: true},
		},
	}).CacheUsageValidationSplitEnabled())
}

func TestRelayInfoConsumesStreamProtocolEndRequirementPerHandler(t *testing.T) {
	info := &RelayInfo{IsStream: true}
	info.RequireStreamProtocolEnd()

	require.True(t, info.ConsumeStreamProtocolEndRequirement())
	require.False(t, info.ConsumeStreamProtocolEndRequirement())
}

func TestGenRelayInfoFreezesClientRequestMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stream := true
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{Stream: &stream}, nil)
	require.NoError(t, err)
	require.Equal(t, types.RequestModeStream, info.ClientRequestMode)

	info.IsStream = false
	require.Equal(t, types.RequestModeStream, info.ClientRequestMode)
}

func TestGenRelayInfoAssignsExplicitModesForRealtimeAndTasks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	realtimeContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	realtimeContext.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	realtime, err := GenRelayInfo(realtimeContext, types.RelayFormatOpenAIRealtime, nil, nil)
	require.NoError(t, err)
	require.Equal(t, types.RequestModeStream, realtime.ClientRequestMode)
	require.True(t, realtime.IsStream)

	taskContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	taskContext.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	task, err := GenRelayInfo(taskContext, types.RelayFormatTask, nil, nil)
	require.NoError(t, err)
	require.Equal(t, types.RequestModeNonStream, task.ClientRequestMode)
}
