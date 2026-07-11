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

func TestRelayRetryHarnessUsesChannelAndSystemBudgetsAcrossSelectionSweeps(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var (
		attemptsMu sync.Mutex
		attempts   []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptsMu.Lock()
		attempts = append(attempts, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		attemptsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"busy","type":"api_error","code":"busy"}}`))
	}))
	t.Cleanup(upstream.Close)

	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalSQLitePath := common.SQLitePath
	originalIsMasterNode := common.IsMasterNode
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	t.Setenv("SQL_DSN", "")
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	common.IsMasterNode = false
	require.NoError(t, model.InitDB())
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	common.MemoryCacheEnabled = false
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.SQLitePath = originalSQLitePath
		common.IsMasterNode = originalIsMasterNode
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
	})

	modelName := "retry-harness-model"
	priority := int64(10)
	weight := uint(1)
	autoBan := 0
	retryTimes := 3
	retryInterval := 0
	channels := make([]model.Channel, 0, 3)
	abilities := make([]model.Ability, 0, 3)
	for id := 1; id <= 3; id++ {
		baseURL := upstream.URL
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
	originalErrorLogEnabled := constant.ErrorLogEnabled
	originalRetryRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	originalModelRatios := ratio_setting.ModelRatio2JSONString()
	originalFreeModelPreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	common.RetryTimes = 10
	constant.ErrorLogEnabled = false
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(fmt.Sprintf(`{"%s":0}`, modelName)))
	t.Cleanup(func() {
		common.RetryTimes = originalRetryTimes
		constant.ErrorLogEnabled = originalErrorLogEnabled
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
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now())
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, &channels[0], modelName))

	Relay(ctx, types.RelayFormatOpenAI)

	attemptsMu.Lock()
	gotAttempts := append([]string(nil), attempts...)
	attemptsMu.Unlock()
	require.Len(t, gotAttempts, 44)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Len(t, ctx.GetStringSlice("use_channel"), 44)

	runs := make([][]string, 0, 11)
	for _, channelKey := range gotAttempts {
		if len(runs) == 0 || runs[len(runs)-1][0] != channelKey {
			runs = append(runs, []string{channelKey})
			continue
		}
		runs[len(runs)-1] = append(runs[len(runs)-1], channelKey)
	}
	require.Len(t, runs, 11, "system retry=10 must produce the initial channel round plus ten inter-channel retries")
	for interChannelRound, run := range runs {
		assert.Lenf(t, run, 4, "channel round %d must contain one request plus three channel-internal retries", interChannelRound)
	}

	for sweepStart := 0; sweepStart+3 <= len(runs); sweepStart += 3 {
		seen := make(map[string]struct{}, 3)
		for _, run := range runs[sweepStart : sweepStart+3] {
			seen[run[0]] = struct{}{}
		}
		assert.Lenf(t, seen, 3, "selection sweep starting at round %d must exhaust all three peers before reuse", sweepStart)
	}
}
