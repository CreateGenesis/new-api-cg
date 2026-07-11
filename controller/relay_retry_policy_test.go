package controller

import (
	"context"
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
	"github.com/QuantumNous/new-api/service"
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

func TestBuildRelayErrorLogDetailsIncludesUpstreamDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("channel_name", "primary")
	ctx.Set("channel_type", constant.ChannelTypeOpenAI)
	ctx.Set("use_channel", []string{"12"})
	ctx.Set("internal_retry_overload_blocked", map[string]interface{}{"reason": "affinity"})

	upstreamErr := types.NewErrorWithStatusCode(
		errors.New("rate limited"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusServiceUnavailable,
	)
	upstreamErr.SetUpstreamResponse(
		http.StatusTooManyRequests,
		`{"error":{"message":"request to https://api.example.com/v1 failed"}}`,
	)

	details := buildRelayErrorLogDetails(ctx, upstreamErr, 12)

	assert.Equal(t, http.StatusServiceUnavailable, details["status_code"])
	adminInfo, ok := details["admin_info"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, http.StatusTooManyRequests, adminInfo["upstream_status_code"])
	assert.Equal(t, []string{"12"}, adminInfo["use_channel"])
	assert.NotContains(t, adminInfo["upstream_response"], "api.example.com")
	assert.Contains(t, adminInfo["upstream_response"], "request to https://***.com")
	assert.Equal(t, map[string]interface{}{"reason": "affinity"}, adminInfo["internal_retry_overload_blocked"])
}

func TestChannelOverloadedNeverRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges("500-503")
	require.NoError(t, err)
	policy := relayRetryPolicy{retryTimes: 3, statusCodeRanges: ranges}
	overloadErr := channelOverloadedError()

	assert.False(t, shouldRetryWithPolicy(ctx, overloadErr, policy, 0))
	assert.False(t, shouldRetrySameChannelWithPolicy(ctx, overloadErr, relayRetryPolicy{
		retryTimes: 3, statusCodeRanges: ranges, channelOverride: true,
	}, 0))
	assert.False(t, shouldRetryTaskRelay(ctx, 1, service.TaskErrorWrapperLocal(
		overloadErr.Err, string(types.ErrorCodeChannelOverloaded), http.StatusServiceUnavailable,
	), 3))
}

func TestLockedChannelOverloadDoesNotFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/task/remix", nil)
	channel := &model.Channel{
		Id: 940002, Type: constant.ChannelTypeOpenAI, Name: "locked", Key: "key-a",
		ChannelInfo: model.ChannelInfo{ChannelOverloadProtection: model.OverloadProtection{
			Enabled: true, ConcurrentRequests: 1,
		}},
	}
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, channel, "sora"))
	info := &relaycommon.RelayInfo{OriginModelName: "sora"}
	param := &service.ChannelSelectParam{Ctx: ctx, TokenGroup: "default", ModelName: "sora"}
	first, scope, err := service.AcquireChannelOverloadLease(ctx.Request.Context(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Empty(t, scope)
	t.Cleanup(func() { first.Release(context.Background()) })

	selected, lease, overloadErr := acquireRelayOverloadLease(ctx, info, param, channel, true)

	assert.Same(t, channel, selected)
	assert.Nil(t, lease)
	require.NotNil(t, overloadErr)
	assert.Equal(t, types.ErrorCodeChannelOverloaded, overloadErr.GetErrorCode())
	assert.Empty(t, param.ExcludedChannelIDs)
}

func TestSpecificChannelStillRunsOverloadAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "950001")
	channel := &model.Channel{
		Id: 950001, Type: constant.ChannelTypeOpenAI, Name: "specific", Key: "key-a",
		ChannelInfo: model.ChannelInfo{ChannelOverloadProtection: model.OverloadProtection{
			Enabled: true, ConcurrentRequests: 1,
		}},
	}
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, channel, "gpt-4o"))
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-4o"}
	param := &service.ChannelSelectParam{Ctx: ctx, TokenGroup: "default", ModelName: "gpt-4o"}
	first, scope, err := service.AcquireChannelOverloadLease(ctx.Request.Context(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Empty(t, scope)
	t.Cleanup(func() { first.Release(context.Background()) })

	selected, lease, overloadErr := acquireRelayOverloadLease(ctx, info, param, channel, false)

	assert.Same(t, channel, selected)
	assert.Nil(t, lease)
	require.NotNil(t, overloadErr)
	assert.Equal(t, types.ErrorCodeChannelOverloaded, overloadErr.GetErrorCode())
	assert.Empty(t, param.ExcludedChannelIDs)
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

func TestIndependentRetryCountersProduceFortyFourAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges("503")
	require.NoError(t, err)
	channelPolicy := relayRetryPolicy{
		retryTimes:       3,
		statusCodeRanges: ranges,
		channelOverride:  true,
	}
	globalPolicy := relayRetryPolicy{
		retryTimes:       10,
		statusCodeRanges: ranges,
	}
	interChannelState := &service.InterChannelRetryState{}
	upstreamErr := statusCodeError(http.StatusServiceUnavailable)
	attempts := 0
	trace := make([][2]int, 0, 44)

	for {
		sameChannelState := &service.SameChannelRetryState{}
		for {
			attempts++
			trace = append(trace, [2]int{interChannelState.Count(), sameChannelState.Count()})
			if !shouldRetrySameChannelWithPolicy(ctx, upstreamErr, channelPolicy, sameChannelState.Count()) {
				break
			}
			sameChannelState.Increase()
		}
		if !shouldRetryWithPolicy(ctx, upstreamErr, globalPolicy, interChannelState.Count()) {
			break
		}
		interChannelState.Increase()
	}

	assert.Equal(t, 44, attempts)
	require.Len(t, trace, 44)
	for interChannelRetry := 0; interChannelRetry <= 10; interChannelRetry++ {
		for sameChannelRetry := 0; sameChannelRetry <= 3; sameChannelRetry++ {
			index := interChannelRetry*4 + sameChannelRetry
			assert.Equal(t, [2]int{interChannelRetry, sameChannelRetry}, trace[index])
		}
	}
}

func TestAffinitySkipDoesNotSuppressConfiguredSameChannelRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("channel_affinity_skip_retry_on_failure", true)
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges("503")
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
	upstreamErr := statusCodeError(http.StatusServiceUnavailable)

	assert.True(t, shouldRetrySameChannelWithPolicy(ctx, upstreamErr, channelPolicy, 0))
	assert.False(t, shouldRetryWithPolicy(ctx, upstreamErr, globalPolicy, 0))
}

func TestSameChannelRetryRejectsPermanentChannelErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("layered_relay_retry", true)
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges("400-599")
	require.NoError(t, err)
	policy := relayRetryPolicy{retryTimes: 1, statusCodeRanges: ranges, channelOverride: true}

	permanentCodes := []types.ErrorCode{
		types.ErrorCodeChannelInvalidKey,
		types.ErrorCodeChannelNoAvailableKey,
		types.ErrorCodeChannelParamOverrideInvalid,
		types.ErrorCodeChannelHeaderOverrideInvalid,
		types.ErrorCodeChannelModelMappedError,
		types.ErrorCodeChannelAwsClientError,
	}
	for _, code := range permanentCodes {
		err := types.NewErrorWithStatusCode(errors.New("permanent"), code, http.StatusInternalServerError)
		assert.False(t, shouldRetrySameChannelWithPolicy(ctx, err, policy, 0), code)
	}

	transient := types.NewErrorWithStatusCode(errors.New("slow"), types.ErrorCodeChannelResponseTimeExceeded, http.StatusRequestTimeout)
	assert.True(t, shouldRetrySameChannelWithPolicy(ctx, transient, policy, 0))
}

func TestPermanentChannelErrorCanSwitchChannelUnlessSkipRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	policy := relayRetryPolicy{retryTimes: 1}

	invalidKey := types.NewError(errors.New("invalid key"), types.ErrorCodeChannelInvalidKey)
	assert.True(t, shouldRetryWithPolicy(ctx, invalidKey, policy, 0))

	invalidOverride := types.NewError(errors.New("invalid override"), types.ErrorCodeChannelParamOverrideInvalid, types.ErrOptionWithSkipRetry())
	assert.False(t, shouldRetryWithPolicy(ctx, invalidOverride, policy, 0))
}

func TestInternalRetryOverloadForcesChannelSwitchWithoutStatusMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	lastUpstreamErr := statusCodeError(http.StatusTeapot)
	policy := relayRetryPolicy{retryTimes: 1}

	assert.True(t, shouldSwitchChannelAfterInternalRetryOverload(ctx, lastUpstreamErr, policy, 0))
	assert.False(t, shouldSwitchChannelAfterInternalRetryOverload(ctx, lastUpstreamErr, policy, 1))

	ctx.Set("channel_affinity_skip_retry_on_failure", true)
	assert.False(t, shouldSwitchChannelAfterInternalRetryOverload(ctx, lastUpstreamErr, policy, 0))
	ctx.Set("channel_affinity_skip_retry_on_failure", false)
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "1")
	assert.False(t, shouldSwitchChannelAfterInternalRetryOverload(ctx, lastUpstreamErr, policy, 0))
}

func TestControllerOwnsOverloadLeaseOnlyForP0TextProtocols(t *testing.T) {
	owned := []struct {
		format types.RelayFormat
		mode   int
	}{
		{types.RelayFormatOpenAI, relayconstant.RelayModeChatCompletions},
		{types.RelayFormatOpenAI, relayconstant.RelayModeCompletions},
		{types.RelayFormatOpenAIResponses, relayconstant.RelayModeResponses},
		{types.RelayFormatClaude, relayconstant.RelayModeChatCompletions},
	}
	for _, testCase := range owned {
		assert.True(t, controllerOwnsRelayOverloadLease(testCase.format, testCase.mode))
	}
	assert.False(t, controllerOwnsRelayOverloadLease(types.RelayFormatOpenAIImage, relayconstant.RelayModeImagesGenerations))
	assert.False(t, controllerOwnsRelayOverloadLease(types.RelayFormatEmbedding, relayconstant.RelayModeEmbeddings))
	assert.False(t, controllerOwnsRelayOverloadLease(types.RelayFormatOpenAIResponsesCompaction, relayconstant.RelayModeResponsesCompact))
	assert.False(t, controllerOwnsRelayOverloadLease(types.RelayFormatGemini, relayconstant.RelayModeGemini))
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
