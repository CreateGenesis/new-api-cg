package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayRetryHarnessStopsAfterUniqueChannelsAndUpgradesBoundedTokenRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var (
		attemptsMu            sync.Mutex
		attempts              []string
		succeedOnAttempt      int
		routeOnBoundedHTTP400 bool
		streamFallback        bool
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsMu.Lock()
		channelKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		attempts = append(attempts, channelKey)
		attemptNumber := len(attempts)
		shouldSucceed := succeedOnAttempt > 0 && attemptNumber == succeedOnAttempt
		if routeOnBoundedHTTP400 && channelKey != "channel-1" {
			shouldSucceed = true
		}
		boundedHTTP400 := routeOnBoundedHTTP400 && channelKey == "channel-1"
		useStreamFallback := streamFallback
		attemptsMu.Unlock()
		if useStreamFallback {
			w.Header().Set("Content-Type", "text/event-stream")
			if channelKey == "channel-1" {
				_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"first stream failed\",\"type\":\"upstream_error\",\"code\":\"stream_failed\"}}\n\n"))
				return
			}
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream-retry\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"retry-harness-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream fallback ok\"},\"finish_reason\":null}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream-retry\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"retry-harness-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if shouldSucceed {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-retry-success","object":"chat.completion","created":1,"model":"retry-harness-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		if boundedHTTP400 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"input exceeds this route capacity","type":"invalid_request_error","code":"context_length_exceeded"}}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"busy","type":"api_error","code":"busy"}}`))
	}))
	t.Cleanup(upstream.Close)

	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalIsMasterNode := common.IsMasterNode
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	t.Setenv("SQL_DSN", "")
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	common.IsMasterNode = false
	common.RedisEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}, &model.User{}, &model.RelayDebugPayload{}))
	common.MemoryCacheEnabled = false
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.IsMasterNode = originalIsMasterNode
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
	})

	modelName := "retry-harness-model"
	priorities := []int64{10, 10, 5}
	weight := uint(1)
	autoBan := 0
	retryTimes := 3
	retryInterval := 0
	channels := make([]model.Channel, 0, 3)
	abilities := make([]model.Ability, 0, 3)
	for id := 1; id <= 3; id++ {
		baseURL := upstream.URL
		priority := priorities[id-1]
		channels = append(channels, model.Channel{
			Id:       id,
			Type:     constant.ChannelTypeOpenAI,
			Key:      fmt.Sprintf("channel-%d", id),
			Status:   common.ChannelStatusEnabled,
			Name:     fmt.Sprintf("retry-harness-%d", id),
			Weight:   &weight,
			BaseURL:  &baseURL,
			Models:   modelName,
			Group:    "default",
			Priority: &priority,
			AutoBan:  &autoBan,
			OtherSettings: fmt.Sprintf(
				`{"status_code_retry":{"enabled":true,"retry_times":%d,"retry_interval_ms":%d,"status_codes":"503"}}`,
				retryTimes,
				retryInterval,
			),
		})
		abilities = append(abilities, model.Ability{
			Group: "default", Model: modelName, ChannelId: id, Enabled: true, Priority: &priority, Weight: weight,
		})
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&abilities).Error)

	originalRetryTimes := common.RetryTimes
	originalLogConsumeEnabled := common.LogConsumeEnabled
	originalRelayDebugLogEnabled := common.RelayDebugLogEnabled
	originalRelayDebugLogTextLimitMB := common.RelayDebugLogTextLimitMB
	originalErrorLogEnabled := constant.ErrorLogEnabled
	originalStreamingTimeout := constant.StreamingTimeout
	originalRetryRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	originalModelRatios := ratio_setting.ModelRatio2JSONString()
	originalFreeModelPreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	common.RetryTimes = 10
	common.LogConsumeEnabled = true
	common.RelayDebugLogEnabled = true
	common.RelayDebugLogTextLimitMB = 1
	constant.ErrorLogEnabled = true
	constant.StreamingTimeout = 30
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(fmt.Sprintf(`{"%s":0}`, modelName)))
	t.Cleanup(func() {
		common.RetryTimes = originalRetryTimes
		common.LogConsumeEnabled = originalLogConsumeEnabled
		common.RelayDebugLogEnabled = originalRelayDebugLogEnabled
		common.RelayDebugLogTextLimitMB = originalRelayDebugLogTextLimitMB
		constant.ErrorLogEnabled = originalErrorLogEnabled
		constant.StreamingTimeout = originalStreamingTimeout
		operation_setting.AutomaticRetryStatusCodeRanges = originalRetryRanges
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = originalFreeModelPreConsume
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalModelRatios))
	})
	service.InitHttpClient()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set(common.RequestIdKey, "relay-debug-final-failure")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, &channels[0], modelName))

	Relay(ctx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	gotAttempts := append([]string(nil), attempts...)
	attemptsMu.Unlock()
	require.Len(t, gotAttempts, 12)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "busy", "candidate exhaustion must preserve the last upstream error")
	require.Len(t, ctx.GetStringSlice("use_channel"), 12)

	runs := make([][]string, 0, 3)
	for _, channelKey := range gotAttempts {
		if len(runs) == 0 || runs[len(runs)-1][0] != channelKey {
			runs = append(runs, []string{channelKey})
			continue
		}
		runs[len(runs)-1] = append(runs[len(runs)-1], channelKey)
	}
	require.Len(t, runs, 3, "system retry budget must not restart an exhausted channel selection")
	for interChannelRound, run := range runs {
		assert.Lenf(t, run, 4, "channel round %d must contain one request plus three channel-internal retries", interChannelRound)
	}
	assert.Equal(t, []string{"channel-1", "channel-2", "channel-3"}, []string{runs[0][0], runs[1][0], runs[2][0]})

	var errorLogCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeError).Count(&errorLogCount).Error)
	assert.Equal(t, int64(1), errorLogCount, "all failed upstream attempts must produce one final request error log")
	var failedLog model.Log
	require.NoError(t, db.Where("request_id = ? AND type = ?", "relay-debug-final-failure", model.LogTypeError).First(&failedLog).Error)
	var failedOther struct {
		AdminInfo struct {
			RelayRetry *service.RelayRetrySummary `json:"relay_retry"`
		} `json:"admin_info"`
	}
	require.NoError(t, common.UnmarshalJsonStr(failedLog.Other, &failedOther))
	require.NotNil(t, failedOther.AdminInfo.RelayRetry)
	assert.Equal(t, "failed", failedOther.AdminInfo.RelayRetry.Outcome)
	assert.Equal(t, 12, failedOther.AdminInfo.RelayRetry.AttemptCount)
	assert.Equal(t, 12, failedOther.AdminInfo.RelayRetry.FailureCount)
	assert.Equal(t, 1, failedOther.AdminInfo.RelayRetry.UniqueErrorCount)
	assert.True(t, failedOther.AdminInfo.RelayRetry.TraceAvailable)
	require.Len(t, failedOther.AdminInfo.RelayRetry.Errors, 1)
	assert.Len(t, failedOther.AdminInfo.RelayRetry.Errors[0].Occurrences, 12)
	failedTracePayload, err := model.LoadRelayDebugPayload(ctx.Request.Context(), "relay-debug-final-failure")
	require.NoError(t, err)
	var failedTrace service.RelayDebugTrace
	require.NoError(t, common.Unmarshal(failedTracePayload, &failedTrace))
	assert.Equal(t, "failed", failedTrace.Outcome)
	assert.Len(t, failedTrace.Attempts, 12)
	assert.NotContains(t, string(failedTracePayload), "Bearer channel-1")

	attemptsMu.Lock()
	attempts = nil
	succeedOnAttempt = 3
	attemptsMu.Unlock()

	successRecorder := httptest.NewRecorder()
	successCtx, _ := gin.CreateTestContext(successRecorder)
	successCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	successCtx.Request.Header.Set("Content-Type", "application/json")
	successCtx.Set(common.RequestIdKey, "relay-debug-recovered")
	common.SetContextKey(successCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(successCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(successCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(successCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(successCtx, &channels[0], modelName))

	Relay(successCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	successAttempts := append([]string(nil), attempts...)
	attemptsMu.Unlock()
	assert.Equal(t, http.StatusOK, successRecorder.Code)
	require.Len(t, successAttempts, 3)
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeError).Count(&errorLogCount).Error)
	assert.Equal(t, int64(1), errorLogCount, "a successful retry must not persist intermediate upstream errors")
	var recoveredLog model.Log
	require.NoError(t, db.Where("request_id = ? AND type = ?", "relay-debug-recovered", model.LogTypeConsume).First(&recoveredLog).Error)
	var recoveredOther struct {
		AdminInfo struct {
			RelayRetry *service.RelayRetrySummary `json:"relay_retry"`
		} `json:"admin_info"`
	}
	require.NoError(t, common.UnmarshalJsonStr(recoveredLog.Other, &recoveredOther))
	require.NotNil(t, recoveredOther.AdminInfo.RelayRetry)
	assert.Equal(t, "recovered", recoveredOther.AdminInfo.RelayRetry.Outcome)
	assert.Equal(t, 3, recoveredOther.AdminInfo.RelayRetry.AttemptCount)
	assert.Equal(t, 2, recoveredOther.AdminInfo.RelayRetry.FailureCount)
	assert.Equal(t, 1, recoveredOther.AdminInfo.RelayRetry.UniqueErrorCount)
	assert.True(t, recoveredOther.AdminInfo.RelayRetry.TraceAvailable)
	recoveredTracePayload, err := model.LoadRelayDebugPayload(successCtx.Request.Context(), "relay-debug-recovered")
	require.NoError(t, err)
	var recoveredTrace service.RelayDebugTrace
	require.NoError(t, common.Unmarshal(recoveredTracePayload, &recoveredTrace))
	require.Len(t, recoveredTrace.Attempts, 3)
	assert.True(t, recoveredTrace.Attempts[2].Succeeded)
	require.NotEmpty(t, recoveredTrace.Attempts[2].Exchanges)
	assert.NotNil(t, recoveredTrace.Attempts[2].Exchanges[0].Request.Body)

	attemptsMu.Lock()
	attempts = nil
	succeedOnAttempt = 0
	streamFallback = true
	attemptsMu.Unlock()

	streamRecorder := httptest.NewRecorder()
	streamCtx, _ := gin.CreateTestContext(streamRecorder)
	streamCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}],"stream":true}`, modelName)),
	)
	streamCtx.Request.Header.Set("Content-Type", "application/json")
	streamCtx.Set(common.RequestIdKey, "relay-debug-stream-recovered")
	common.SetContextKey(streamCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(streamCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(streamCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(streamCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(streamCtx, &channels[0], modelName))

	Relay(streamCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	streamAttempts := append([]string(nil), attempts...)
	streamFallback = false
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1", "channel-2"}, streamAttempts)
	assert.Equal(t, []string{"1", "2"}, streamCtx.GetStringSlice("use_channel"))
	assert.Contains(t, streamRecorder.Body.String(), "stream fallback ok")
	assert.NotContains(t, streamRecorder.Body.String(), "first stream failed")
	var streamConsumeLogCount int64
	require.NoError(t, db.Model(&model.Log{}).
		Where("request_id = ? AND type = ?", "relay-debug-stream-recovered", model.LogTypeConsume).
		Count(&streamConsumeLogCount).Error)
	assert.Equal(t, int64(1), streamConsumeLogCount)
	var firstChannelStatus int
	require.NoError(t, db.Model(&model.Channel{}).Select("status").Where("id = ?", channels[0].Id).Scan(&firstChannelStatus).Error)
	assert.Equal(t, common.ChannelStatusEnabled, firstChannelStatus)

	channels[0].OtherSettings = `{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"ranges":[{"min_tokens":1,"max_tokens":500000}]}}`
	channels[1].OtherSettings = `{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"ranges":[{"min_tokens":500001,"max_tokens":0}]}}`
	channels[2].OtherSettings = channels[0].OtherSettings
	for i := range channels {
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[i].Id).Update("settings", channels[i].OtherSettings).Error)
	}
	common.RetryTimes = 0
	attemptsMu.Lock()
	attempts = nil
	succeedOnAttempt = 0
	routeOnBoundedHTTP400 = true
	attemptsMu.Unlock()

	routingRecorder := httptest.NewRecorder()
	routingCtx, _ := gin.CreateTestContext(routingRecorder)
	largeRoutingRequestBody := fmt.Sprintf(
		`{"model":"%s","messages":[{"role":"user","content":"%s"}]}`,
		modelName,
		strings.Repeat("路", 560000),
	)
	routingCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(largeRoutingRequestBody),
	)
	routingCtx.Request.Header.Set("Content-Type", "application/json")
	routingCtx.Set(common.RequestIdKey, "relay-debug-routing-recovered")
	common.SetContextKey(routingCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(routingCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(routingCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(routingCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(routingCtx, &channels[0], modelName))

	Relay(routingCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	routingAttempts := append([]string(nil), attempts...)
	attemptsMu.Unlock()
	assert.Equal(t, http.StatusOK, routingRecorder.Code)
	assert.Equal(t, []string{"channel-1", "channel-2"}, routingAttempts)
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeError).Count(&errorLogCount).Error)
	assert.Equal(t, int64(1), errorLogCount, "a successful token-range upgrade must not persist its intermediate upstream 400")

	channels[1].OtherSettings = channels[0].OtherSettings
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[1].Id).Update("settings", channels[1].OtherSettings).Error)
	attemptsMu.Lock()
	attempts = nil
	attemptsMu.Unlock()

	noHigherRecorder := httptest.NewRecorder()
	noHigherCtx, _ := gin.CreateTestContext(noHigherRecorder)
	noHigherCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(largeRoutingRequestBody),
	)
	noHigherCtx.Request.Header.Set("Content-Type", "application/json")
	noHigherCtx.Set(common.RequestIdKey, "relay-debug-routing-failed")
	common.SetContextKey(noHigherCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(noHigherCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(noHigherCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(noHigherCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(noHigherCtx, &channels[0], modelName))

	Relay(noHigherCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	noHigherAttempts := append([]string(nil), attempts...)
	attemptsMu.Unlock()
	assert.Equal(t, http.StatusBadRequest, noHigherRecorder.Code)
	assert.Equal(t, []string{"channel-1"}, noHigherAttempts)
	assert.Contains(t, noHigherRecorder.Body.String(), "input exceeds this route capacity")
}
