package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayRetryPolicyFromContextUsesGlobalDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 2
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	policy := relayRetryPolicyFromContext(ctx)

	assert.False(t, policy.channelOverride)
	assert.Equal(t, 2, policy.retryTimes)
	assert.True(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusInternalServerError), policy, 0))
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusBadRequest), policy, 0))
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusInternalServerError), policy, 2))
}

func TestPrepareMultiKeyChannelRetrySynchronizesContextAndRelayInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	channel := &model.Channel{
		Id:      940001,
		Type:    constant.ChannelTypeOpenAI,
		Name:    "multi-key",
		Key:     "key-a\nkey-b",
		AutoBan: common.GetPointer(1),
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}

	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, channel, "gpt-4o"))
	firstKey := common.GetContextKeyString(ctx, constant.ContextKeyChannelKey)
	firstIndex := common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-4o"}
	info.InitChannelMeta(ctx)

	updatedChannel, newAPIError := prepareMultiKeyChannelRetry(ctx, channel, info)

	require.Nil(t, newAPIError)
	assert.Same(t, channel, updatedChannel)
	assert.NotEqual(t, firstKey, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.NotEqual(t, firstIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	require.NotNil(t, info.ChannelMeta)
	assert.Equal(t, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey), info.ApiKey)
	assert.Equal(t, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex), info.ChannelMultiKeyIndex)
}

func TestRelayRetryPolicyFromContextUsesChannelOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = originalRetryTimes })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{
		StatusCodeRetry: &dto.StatusCodeRetrySettings{
			Enabled:     true,
			RetryTimes:  common.GetPointer(30),
			StatusCodes: "429",
		},
	})

	policy := relayRetryPolicyFromContext(ctx)

	require.True(t, policy.channelOverride)
	assert.Equal(t, 30, policy.retryTimes)
	assert.Equal(t, 50*time.Millisecond, policy.retryDelay)
	assert.True(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusTooManyRequests), policy, 29))
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusTooManyRequests), policy, 30))
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusInternalServerError), policy, 0))
}

func TestRelayRetryPolicyFromContextUsesConfiguredChannelRetryInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{
		StatusCodeRetry: &dto.StatusCodeRetrySettings{
			Enabled:         true,
			RetryTimes:      common.GetPointer(3),
			RetryIntervalMS: common.GetPointer(250),
			StatusCodes:     "500",
		},
	})

	policy := relayRetryPolicyFromContext(ctx)

	require.True(t, policy.channelOverride)
	assert.Equal(t, 250*time.Millisecond, policy.retryDelay)
}

func TestChannelStatusCodeRetryRunsBeforeGlobalRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges("500-503")
	require.NoError(t, err)
	channelPolicy := relayRetryPolicy{
		retryTimes:       2,
		statusCodeRanges: ranges,
		channelOverride:  true,
	}
	globalPolicy := relayRetryPolicy{
		retryTimes:       1,
		statusCodeRanges: ranges,
	}

	upstreamErr := statusCodeError(http.StatusInternalServerError)

	assert.True(t, shouldRetrySameChannelWithPolicy(ctx, upstreamErr, channelPolicy, 0))
	assert.True(t, shouldRetrySameChannelWithPolicy(ctx, upstreamErr, channelPolicy, 1))
	assert.False(t, shouldRetrySameChannelWithPolicy(ctx, upstreamErr, channelPolicy, 2))
	assert.True(t, shouldRetryWithPolicy(ctx, upstreamErr, globalPolicy, 0))
}

func TestChannelStatusCodeRetryAllowsSpecificChannelSameChannelRetryOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("specific_channel_id", 1)
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges("429")
	require.NoError(t, err)
	channelPolicy := relayRetryPolicy{
		retryTimes:       1,
		statusCodeRanges: ranges,
		channelOverride:  true,
	}
	globalPolicy := relayRetryPolicy{
		retryTimes:       1,
		statusCodeRanges: ranges,
	}

	upstreamErr := statusCodeError(http.StatusTooManyRequests)

	assert.True(t, shouldRetrySameChannelWithPolicy(ctx, upstreamErr, channelPolicy, 0))
	assert.False(t, shouldRetryWithPolicy(ctx, upstreamErr, globalPolicy, 0))
}

func TestRelayRetryPolicyFromContextPreservesExplicitZeroRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{
		StatusCodeRetry: &dto.StatusCodeRetrySettings{
			Enabled:     true,
			RetryTimes:  common.GetPointer(0),
			StatusCodes: "429",
		},
	})

	policy := relayRetryPolicyFromContext(ctx)

	require.True(t, policy.channelOverride)
	assert.Equal(t, 0, policy.retryTimes)
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusTooManyRequests), policy, 0))
}

func TestShouldRetryWithPolicyKeepsExistingSkipRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges("429,500-599")
	require.NoError(t, err)
	policy := relayRetryPolicy{
		retryTimes:       3,
		statusCodeRanges: ranges,
	}

	assert.False(t, shouldRetryWithPolicy(ctx, nil, policy, 0))
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusOK), policy, 0))
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusGatewayTimeout), policy, 0))
	assert.True(t, shouldRetryWithPolicy(ctx, statusCodeError(700), policy, 0))
	assert.False(t, shouldRetryWithPolicy(ctx, types.NewErrorWithStatusCode(errors.New("skip"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests, types.ErrOptionWithSkipRetry()), policy, 0))

	ctx.Set("specific_channel_id", 1)
	assert.False(t, shouldRetryWithPolicy(ctx, statusCodeError(http.StatusTooManyRequests), policy, 0))
}

func TestEstimatedInputTokensForRoutingUsesConservativeTextEstimateForRanges(t *testing.T) {
	text := strings.Repeat("routingtoken ", 1000)
	meta := &types.TokenCountMeta{
		TokenType:   types.TokenTypeTokenizer,
		CombineText: text,
	}

	estimatedTokens := estimatedInputTokensForRouting(
		types.RelayFormatOpenAI,
		relayconstant.RelayModeChatCompletions,
		1500,
		meta,
		"glm-5.2",
	)

	require.NotNil(t, estimatedTokens)
	assert.GreaterOrEqual(t, *estimatedTokens, 3001)
	channel := &model.Channel{OtherSettings: `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":3001,"max_tokens":5000}]}}`}
	assert.True(t, channel.MatchesInputTokenRouting(estimatedTokens))
}

func statusCodeError(statusCode int) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, statusCode)
}
