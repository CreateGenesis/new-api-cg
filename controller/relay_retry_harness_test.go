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
		attemptsMu              sync.Mutex
		attempts                []string
		succeedOnAttempt        int
		succeedOnKey            string
		routeOnBoundedHTTP400   bool
		streamFallback          bool
		zeroInputFallback       bool
		zeroOutputFallback      bool
		zeroOutputStream        bool
		sseToJSONFallback       bool
		responseContentFallback bool
		responseContentStream   bool
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsMu.Lock()
		channelKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		attempts = append(attempts, channelKey)
		attemptNumber := len(attempts)
		shouldSucceed := succeedOnAttempt > 0 && attemptNumber == succeedOnAttempt
		if succeedOnKey != "" && channelKey == succeedOnKey {
			shouldSucceed = true
		}
		if routeOnBoundedHTTP400 && channelKey != "channel-1" {
			shouldSucceed = true
		}
		boundedHTTP400 := routeOnBoundedHTTP400 && channelKey == "channel-1"
		useStreamFallback := streamFallback
		useZeroInputFallback := zeroInputFallback
		useZeroOutputFallback := zeroOutputFallback
		useZeroOutputStream := zeroOutputStream
		useSSEToJSONFallback := sseToJSONFallback
		useResponseContentFallback := responseContentFallback
		useResponseContentStream := responseContentStream
		attemptsMu.Unlock()
		if useSSEToJSONFallback {
			if channelKey == "channel-1" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"id\":\"sse-first-attempt\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"retry-harness-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0,\"total_tokens\":1}}\n\ndata: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"json-second-attempt","object":"chat.completion","created":1,"model":"retry-harness-model","choices":[{"index":0,"message":{"role":"assistant","content":"sse to json fallback ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		if useResponseContentFallback {
			if useResponseContentStream {
				w.Header().Set("Content-Type", "text/event-stream")
				if channelKey == "channel-1" {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"[内容\"}}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"已过滤]\"}}]}\n\n"))
					return
				}
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"response content stream fallback ok\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if channelKey == "channel-1" {
				_, _ = w.Write([]byte(`{"id":"matched-channel-1","choices":[{"message":{"role":"assistant","content":" [内容已过滤]"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"response-content-fallback","choices":[{"message":{"role":"assistant","content":"response content fallback ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		if useZeroInputFallback {
			w.Header().Set("Content-Type", "application/json")
			if channelKey == "channel-1" {
				_, _ = w.Write([]byte(`{"id":"zero-input-channel-1","object":"chat.completion","created":1,"model":"retry-harness-model","choices":[{"index":0,"message":{"role":"assistant","content":"zero input channel 1"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":1,"total_tokens":1}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"chatcmpl-zero-input-fallback","object":"chat.completion","created":1,"model":"retry-harness-model","choices":[{"index":0,"message":{"role":"assistant","content":"zero input fallback ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		if useZeroOutputFallback {
			if useZeroOutputStream {
				w.Header().Set("Content-Type", "text/event-stream")
				if channelKey == "channel-1" {
					_, _ = w.Write([]byte(": zero-output-channel-1\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0,\"total_tokens\":1}}\n\ndata: [DONE]\n\n"))
					return
				}
				_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-zero-output-stream-fallback\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"retry-harness-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"zero output stream fallback ok\"},\"finish_reason\":null}]}\n\n"))
				_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if channelKey == "channel-1" {
				_, _ = w.Write([]byte(`{"id":"zero-output-channel-1","object":"chat.completion","created":1,"model":"retry-harness-model","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"chatcmpl-zero-output-fallback","object":"chat.completion","created":1,"model":"retry-harness-model","choices":[{"index":0,"message":{"role":"assistant","content":"zero output fallback ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		if useStreamFallback {
			w.Header().Set("Content-Type", "text/event-stream")
			if channelKey == "channel-1" {
				_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"first stream failed\",\"type\":\"upstream_error\",\"code\":\"stream_failed\"}}\n\n"))
				return
			}
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream-retry\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"retry-harness-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream fallback ok\"},\"finish_reason\":null}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream-retry\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"retry-harness-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n"))
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
	originalResponseContentPolicy := operation_setting.ResponseContentRetryPolicy2JSONString()
	common.RetryTimes = 10
	common.LogConsumeEnabled = true
	common.RelayDebugLogEnabled = true
	common.RelayDebugLogTextLimitMB = 1
	constant.ErrorLogEnabled = true
	constant.StreamingTimeout = 30
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	require.NoError(t, operation_setting.UpdateResponseContentRetryPolicyByJSONString(`{"enabled":true,"rules":[{"mode":"prefix","content":"[内容已过滤]"}]}`))
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
		require.NoError(t, operation_setting.UpdateResponseContentRetryPolicyByJSONString(originalResponseContentPolicy))
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

	originalFirstChannelKey := channels[0].Key
	originalFirstChannelInfo := channels[0].ChannelInfo
	originalFirstChannelSettings := channels[0].OtherSettings
	noOverrideCases := []struct {
		name        string
		requestID   string
		key         string
		channelInfo model.ChannelInfo
	}{
		{
			name:      "single key",
			requestID: "relay-no-channel-override-single-key",
			key:       "channel-1",
		},
		{
			name:      "multiple keys",
			requestID: "relay-no-channel-override-multiple-keys",
			key:       "channel-1-key-a\nchannel-1-key-b\nchannel-1-key-c",
			channelInfo: model.ChannelInfo{
				IsMultiKey:   true,
				MultiKeyMode: constant.MultiKeyModeRandom,
			},
		},
	}
	for _, testCase := range noOverrideCases {
		t.Run(testCase.name+" does not retry the same channel", func(t *testing.T) {
			channels[0].Key = testCase.key
			channels[0].ChannelInfo = testCase.channelInfo
			channels[0].OtherSettings = "{}"
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Updates(map[string]interface{}{
				"key":          channels[0].Key,
				"channel_info": channels[0].ChannelInfo,
				"settings":     channels[0].OtherSettings,
			}).Error)
			attemptsMu.Lock()
			attempts = nil
			succeedOnAttempt = 0
			succeedOnKey = "channel-2"
			attemptsMu.Unlock()

			retryRecorder := httptest.NewRecorder()
			retryCtx, _ := gin.CreateTestContext(retryRecorder)
			retryCtx.Request = httptest.NewRequest(
				http.MethodPost,
				"/v1/chat/completions",
				strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
			)
			retryCtx.Request.Header.Set("Content-Type", "application/json")
			retryCtx.Set(common.RequestIdKey, testCase.requestID)
			common.SetContextKey(retryCtx, constant.ContextKeyTokenGroup, "default")
			common.SetContextKey(retryCtx, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(retryCtx, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(retryCtx, constant.ContextKeyRequestStartTime, time.Now())
			require.Nil(t, middleware.SetupContextForSelectedChannel(retryCtx, &channels[0], modelName))

			Relay(retryCtx, types.RelayFormatOpenAI)

			attemptsMu.Lock()
			noOverrideAttempts := append([]string(nil), attempts...)
			attemptsMu.Unlock()
			require.Len(t, noOverrideAttempts, 2)
			assert.Contains(t, strings.Split(testCase.key, "\n"), noOverrideAttempts[0])
			assert.Equal(t, "channel-2", noOverrideAttempts[1])
			assert.Equal(t, []string{"1", "2"}, retryCtx.GetStringSlice("use_channel"))
			assert.Equal(t, http.StatusOK, retryRecorder.Code)
		})
	}
	channels[0].Key = originalFirstChannelKey
	channels[0].ChannelInfo = originalFirstChannelInfo
	channels[0].OtherSettings = originalFirstChannelSettings
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Updates(map[string]interface{}{
		"key":          channels[0].Key,
		"channel_info": channels[0].ChannelInfo,
		"settings":     channels[0].OtherSettings,
	}).Error)

	attemptsMu.Lock()
	attempts = nil
	succeedOnAttempt = 0
	succeedOnKey = ""
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

	common.RetryTimes = 2
	attemptsMu.Lock()
	attempts = nil
	responseContentFallback = true
	responseContentStream = false
	attemptsMu.Unlock()
	contentRecorder := httptest.NewRecorder()
	contentCtx, _ := gin.CreateTestContext(contentRecorder)
	contentCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	contentCtx.Request.Header.Set("Content-Type", "application/json")
	contentCtx.Set(common.RequestIdKey, "relay-response-content-recovered")
	common.SetContextKey(contentCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(contentCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(contentCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(contentCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(contentCtx, &channels[0], modelName))

	Relay(contentCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	contentAttempts := append([]string(nil), attempts...)
	attempts = nil
	responseContentStream = true
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1", "channel-2"}, contentAttempts)
	assert.Equal(t, http.StatusOK, contentRecorder.Code)
	assert.Contains(t, contentRecorder.Body.String(), "response content fallback ok")
	assert.NotContains(t, contentRecorder.Body.String(), "内容已过滤")

	contentStreamRecorder := httptest.NewRecorder()
	contentStreamCtx, _ := gin.CreateTestContext(contentStreamRecorder)
	contentStreamCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}],"stream":true}`, modelName)),
	)
	contentStreamCtx.Request.Header.Set("Content-Type", "application/json")
	contentStreamCtx.Set(common.RequestIdKey, "relay-response-content-stream-recovered")
	common.SetContextKey(contentStreamCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(contentStreamCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(contentStreamCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(contentStreamCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(contentStreamCtx, &channels[0], modelName))

	Relay(contentStreamCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	contentStreamAttempts := append([]string(nil), attempts...)
	attempts = nil
	responseContentFallback = false
	responseContentStream = false
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1", "channel-2"}, contentStreamAttempts)
	assert.Equal(t, http.StatusOK, contentStreamRecorder.Code)
	assert.Contains(t, contentStreamRecorder.Body.String(), "response content stream fallback ok")
	assert.NotContains(t, contentStreamRecorder.Body.String(), "内容已过滤")

	channels[0].OtherSettings = `{"retry_zero_output":true}`
	channels[1].OtherSettings = `{}`
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[i].Id).Update("settings", channels[i].OtherSettings).Error)
	}
	common.RetryTimes = 2
	attemptsMu.Lock()
	attempts = nil
	zeroOutputFallback = true
	zeroOutputStream = false
	attemptsMu.Unlock()

	zeroOutputRecorder := httptest.NewRecorder()
	zeroOutputCtx, _ := gin.CreateTestContext(zeroOutputRecorder)
	zeroOutputCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	zeroOutputCtx.Request.Header.Set("Content-Type", "application/json")
	zeroOutputCtx.Set(common.RequestIdKey, "relay-zero-output-recovered")
	common.SetContextKey(zeroOutputCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(zeroOutputCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(zeroOutputCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(zeroOutputCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(zeroOutputCtx, &channels[0], modelName))

	Relay(zeroOutputCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	zeroOutputAttempts := append([]string(nil), attempts...)
	zeroOutputStream = true
	attempts = nil
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1", "channel-2"}, zeroOutputAttempts)
	assert.Equal(t, http.StatusOK, zeroOutputRecorder.Code)
	assert.Contains(t, zeroOutputRecorder.Body.String(), "zero output fallback ok")
	assert.NotContains(t, zeroOutputRecorder.Body.String(), "zero-output-channel-1")

	zeroOutputStreamRecorder := httptest.NewRecorder()
	zeroOutputStreamCtx, _ := gin.CreateTestContext(zeroOutputStreamRecorder)
	zeroOutputStreamCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}],"stream":true,"stream_options":{"include_usage":true}}`, modelName)),
	)
	zeroOutputStreamCtx.Request.Header.Set("Content-Type", "application/json")
	zeroOutputStreamCtx.Set(common.RequestIdKey, "relay-zero-output-stream-recovered")
	common.SetContextKey(zeroOutputStreamCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(zeroOutputStreamCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(zeroOutputStreamCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(zeroOutputStreamCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(zeroOutputStreamCtx, &channels[0], modelName))

	Relay(zeroOutputStreamCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	zeroOutputStreamAttempts := append([]string(nil), attempts...)
	zeroOutputFallback = false
	zeroOutputStream = false
	attempts = nil
	succeedOnKey = "channel-2"
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1", "channel-2"}, zeroOutputStreamAttempts)
	assert.Equal(t, http.StatusOK, zeroOutputStreamRecorder.Code)
	assert.Contains(t, zeroOutputStreamRecorder.Body.String(), "zero output stream fallback ok")
	assert.NotContains(t, zeroOutputStreamRecorder.Body.String(), "zero-output-channel-1")

	attemptsMu.Lock()
	attempts = nil
	zeroInputFallback = true
	attemptsMu.Unlock()
	zeroInputRecorder := httptest.NewRecorder()
	zeroInputCtx, _ := gin.CreateTestContext(zeroInputRecorder)
	zeroInputCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	zeroInputCtx.Request.Header.Set("Content-Type", "application/json")
	zeroInputCtx.Set(common.RequestIdKey, "relay-zero-input-recovered")
	common.SetContextKey(zeroInputCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(zeroInputCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(zeroInputCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(zeroInputCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(zeroInputCtx, &channels[0], modelName))

	Relay(zeroInputCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	zeroInputAttempts := append([]string(nil), attempts...)
	zeroInputFallback = false
	attempts = nil
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1", "channel-2"}, zeroInputAttempts)
	assert.Equal(t, http.StatusOK, zeroInputRecorder.Code)
	assert.Contains(t, zeroInputRecorder.Body.String(), "zero input fallback ok")
	assert.NotContains(t, zeroInputRecorder.Body.String(), "zero input channel 1")

	attemptsMu.Lock()
	attempts = nil
	sseToJSONFallback = true
	attemptsMu.Unlock()
	sseToJSONRecorder := httptest.NewRecorder()
	sseToJSONCtx, _ := gin.CreateTestContext(sseToJSONRecorder)
	sseToJSONCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	sseToJSONCtx.Request.Header.Set("Content-Type", "application/json")
	sseToJSONCtx.Set(common.RequestIdKey, "relay-sse-to-json-recovered")
	common.SetContextKey(sseToJSONCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(sseToJSONCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(sseToJSONCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(sseToJSONCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(sseToJSONCtx, &channels[0], modelName))

	Relay(sseToJSONCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	sseToJSONAttempts := append([]string(nil), attempts...)
	sseToJSONFallback = false
	attempts = nil
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1", "channel-2"}, sseToJSONAttempts)
	assert.Equal(t, http.StatusOK, sseToJSONRecorder.Code)
	assert.Contains(t, sseToJSONRecorder.Body.String(), "sse to json fallback ok")
	assert.NotContains(t, sseToJSONRecorder.Body.String(), "sse-first-attempt")

	channels[0].OtherSettings = `{"disable_non_stream":true}`
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Update("settings", channels[0].OtherSettings).Error)
	disableNonStreamRecorder := httptest.NewRecorder()
	disableNonStreamCtx, _ := gin.CreateTestContext(disableNonStreamRecorder)
	disableNonStreamCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	disableNonStreamCtx.Request.Header.Set("Content-Type", "application/json")
	disableNonStreamCtx.Set(common.RequestIdKey, "relay-disable-non-stream-rerouted")
	common.SetContextKey(disableNonStreamCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(disableNonStreamCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(disableNonStreamCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(disableNonStreamCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(disableNonStreamCtx, &channels[0], modelName))

	Relay(disableNonStreamCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	disableNonStreamAttempts := append([]string(nil), attempts...)
	succeedOnKey = ""
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-2"}, disableNonStreamAttempts)
	assert.Equal(t, []string{"2"}, disableNonStreamCtx.GetStringSlice("use_channel"))
	assert.Equal(t, http.StatusOK, disableNonStreamRecorder.Code)

	channels[0].OtherSettings = `{"disable_stream":true}`
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Update("settings", channels[0].OtherSettings).Error)
	attemptsMu.Lock()
	attempts = nil
	streamFallback = true
	attemptsMu.Unlock()
	disableStreamRecorder := httptest.NewRecorder()
	disableStreamCtx, _ := gin.CreateTestContext(disableStreamRecorder)
	disableStreamCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}],"stream":true}`, modelName)),
	)
	disableStreamCtx.Request.Header.Set("Content-Type", "application/json")
	disableStreamCtx.Set(common.RequestIdKey, "relay-disable-stream-rerouted")
	common.SetContextKey(disableStreamCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(disableStreamCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(disableStreamCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(disableStreamCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(disableStreamCtx, &channels[0], modelName))

	Relay(disableStreamCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	disableStreamAttempts := append([]string(nil), attempts...)
	attempts = nil
	streamFallback = false
	succeedOnKey = "channel-1"
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-2"}, disableStreamAttempts)
	assert.Equal(t, []string{"2"}, disableStreamCtx.GetStringSlice("use_channel"))
	assert.Equal(t, http.StatusOK, disableStreamRecorder.Code)
	assert.Contains(t, disableStreamRecorder.Body.String(), "stream fallback ok")

	streamDisabledNonStreamRecorder := httptest.NewRecorder()
	streamDisabledNonStreamCtx, _ := gin.CreateTestContext(streamDisabledNonStreamRecorder)
	streamDisabledNonStreamCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}]}`, modelName)),
	)
	streamDisabledNonStreamCtx.Request.Header.Set("Content-Type", "application/json")
	streamDisabledNonStreamCtx.Set(common.RequestIdKey, "relay-disable-stream-allows-non-stream")
	common.SetContextKey(streamDisabledNonStreamCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(streamDisabledNonStreamCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(streamDisabledNonStreamCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(streamDisabledNonStreamCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(streamDisabledNonStreamCtx, &channels[0], modelName))

	Relay(streamDisabledNonStreamCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	streamDisabledNonStreamAttempts := append([]string(nil), attempts...)
	attempts = nil
	succeedOnKey = ""
	attemptsMu.Unlock()
	assert.Equal(t, []string{"channel-1"}, streamDisabledNonStreamAttempts)
	assert.Equal(t, http.StatusOK, streamDisabledNonStreamRecorder.Code)

	pinnedStreamRecorder := httptest.NewRecorder()
	pinnedStreamCtx, _ := gin.CreateTestContext(pinnedStreamRecorder)
	pinnedStreamCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}],"stream":true}`, modelName)),
	)
	pinnedStreamCtx.Request.Header.Set("Content-Type", "application/json")
	pinnedStreamCtx.Set(common.RequestIdKey, "relay-disable-stream-pinned")
	pinnedStreamCtx.Set("specific_channel_id", channels[0].Id)
	common.SetContextKey(pinnedStreamCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(pinnedStreamCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(pinnedStreamCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(pinnedStreamCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(pinnedStreamCtx, &channels[0], modelName))

	Relay(pinnedStreamCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	pinnedStreamAttempts := append([]string(nil), attempts...)
	attemptsMu.Unlock()
	assert.Empty(t, pinnedStreamAttempts)
	assert.Equal(t, http.StatusServiceUnavailable, pinnedStreamRecorder.Code)
	assert.Contains(t, pinnedStreamRecorder.Body.String(), string(types.ErrorCodeChannelStreamDisabled))

	for i := range channels {
		channels[i].OtherSettings = `{"disable_stream":true}`
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[i].Id).Update("settings", channels[i].OtherSettings).Error)
	}
	attemptsMu.Lock()
	attempts = nil
	attemptsMu.Unlock()
	allStreamDisabledRecorder := httptest.NewRecorder()
	allStreamDisabledCtx, _ := gin.CreateTestContext(allStreamDisabledRecorder)
	allStreamDisabledCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"test"}],"stream":true}`, modelName)),
	)
	allStreamDisabledCtx.Request.Header.Set("Content-Type", "application/json")
	allStreamDisabledCtx.Set(common.RequestIdKey, "relay-disable-stream-no-eligible-channel")
	common.SetContextKey(allStreamDisabledCtx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(allStreamDisabledCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(allStreamDisabledCtx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(allStreamDisabledCtx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(allStreamDisabledCtx, &channels[0], modelName))

	Relay(allStreamDisabledCtx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	allStreamDisabledAttempts := append([]string(nil), attempts...)
	attemptsMu.Unlock()
	assert.Empty(t, allStreamDisabledAttempts)
	assert.Equal(t, http.StatusServiceUnavailable, allStreamDisabledRecorder.Code)
	assert.Contains(t, allStreamDisabledRecorder.Body.String(), string(types.ErrorCodeChannelStreamDisabled))
	require.NoError(t, db.Where("request_id IN ?", []string{
		"relay-disable-stream-pinned",
		"relay-disable-stream-no-eligible-channel",
	}).Delete(&model.Log{}).Error)

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

func TestRelayRetryClearsKimiK3CompatibilityWhenSwitchingChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type upstreamAttempt struct {
		Path          string
		Model         string
		Authorization string
	}
	var (
		attemptsMu sync.Mutex
		attempts   []upstreamAttempt
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &request))
		attemptsMu.Lock()
		attempts = append(attempts, upstreamAttempt{Path: r.URL.Path, Model: request.Model, Authorization: r.Header.Get("Authorization")})
		attemptsMu.Unlock()

		if r.URL.Path == "/v1/messages" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html><body><h1>403 Forbidden</h1>Request forbidden by administrative rules.</body></html>"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-k3-fallback","object":"chat.completion","created":1,"model":"k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
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
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
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

	modelName := "kimi-retry-alias"
	compatibleMapping := fmt.Sprintf(`{"%s":"kimi-k3"}`, modelName)
	ordinaryMapping := fmt.Sprintf(`{"%s":"k3"}`, modelName)
	compatibleOverride := `{"operations":[{"mode":"set_header","path":"Authorization","value":"Bearer leaked-first-channel"}]}`
	weight := uint(1)
	autoBan := 0
	firstPriority := int64(10)
	secondPriority := int64(5)
	baseURL := upstream.URL
	channels := []model.Channel{
		{
			Id:            48,
			Type:          constant.ChannelTypeAnthropic,
			Key:           "compatible-channel",
			Status:        common.ChannelStatusEnabled,
			Name:          "kimi-compatible",
			Weight:        &weight,
			BaseURL:       &baseURL,
			Models:        modelName,
			Group:         "default",
			ModelMapping:  &compatibleMapping,
			ParamOverride: &compatibleOverride,
			Priority:      &firstPriority,
			AutoBan:       &autoBan,
			OtherSettings: `{"kimi_k3_official_compatibility":true}`,
		},
		{
			Id:            35,
			Type:          constant.ChannelTypeOpenAI,
			Key:           "ordinary-channel",
			Status:        common.ChannelStatusEnabled,
			Name:          "ordinary-openai",
			Weight:        &weight,
			BaseURL:       &baseURL,
			Models:        modelName,
			Group:         "default",
			ModelMapping:  &ordinaryMapping,
			Priority:      &secondPriority,
			AutoBan:       &autoBan,
			OtherSettings: `{}`,
		},
	}
	abilities := []model.Ability{
		{Group: "default", Model: modelName, ChannelId: channels[0].Id, Enabled: true, Priority: &firstPriority, Weight: weight},
		{Group: "default", Model: modelName, ChannelId: channels[1].Id, Enabled: true, Priority: &secondPriority, Weight: weight},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&abilities).Error)

	originalRetryTimes := common.RetryTimes
	originalRetryRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	originalModelRatios := ratio_setting.ModelRatio2JSONString()
	originalFreeModelPreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	common.RetryTimes = 1
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 403, End: 403}}
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(fmt.Sprintf(`{"%s":0}`, modelName)))
	t.Cleanup(func() {
		common.RetryTimes = originalRetryTimes
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
	ctx.Set(common.RequestIdKey, "kimi-compatibility-channel-switch")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, &channels[0], modelName))

	Relay(ctx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	gotAttempts := append([]upstreamAttempt(nil), attempts...)
	attemptsMu.Unlock()
	require.Len(t, gotAttempts, 2)
	assert.Equal(t, upstreamAttempt{Path: "/v1/messages", Model: "kimi-k3", Authorization: "Bearer leaked-first-channel"}, gotAttempts[0])
	assert.Equal(t, upstreamAttempt{Path: "/v1/chat/completions", Model: "k3", Authorization: "Bearer ordinary-channel"}, gotAttempts[1])
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"content":"ok"`)
	assert.NotContains(t, recorder.Body.String(), "invalid_request")
}
