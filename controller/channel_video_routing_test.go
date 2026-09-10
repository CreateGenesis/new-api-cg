package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	hostdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// VIDEO_ROUTING_TEST_DSN optionally runs the same persistence and routing
// contracts against a dedicated MySQL or PostgreSQL database.
func TestVideoUnderstandingChannelRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldCache, oldRedis := common.MemoryCacheEnabled, common.RedisEnabled
	oldSQLitePath, oldMaster := common.SQLitePath, common.IsMasterNode
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	t.Setenv("SQL_DSN", os.Getenv("VIDEO_ROUTING_TEST_DSN"))
	common.SQLitePath = t.TempDir() + "/video-routing.db"
	common.IsMasterNode, common.RedisEnabled, common.MemoryCacheEnabled = false, false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.MemoryCacheEnabled, common.RedisEnabled = oldCache, oldRedis
		common.SQLitePath, common.IsMasterNode = oldSQLitePath, oldMaster
		common.SetDatabaseTypes(oldMainType, oldLogType)
		if oldDB != nil {
			model.InitChannelCache()
		}
	})
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}, &model.User{}, &model.RelayDebugPayload{}))
	var version string
	versionQuery := "SELECT version()"
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		versionQuery = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database=%s version=%s", common.MainDatabaseType(), version)

	oldRetry, oldConsume, oldError := common.RetryTimes, common.LogConsumeEnabled, constant.ErrorLogEnabled
	oldDebug := common.RelayDebugLogEnabled
	oldStreamTimeout := constant.StreamingTimeout
	oldRanges := operation_setting.AutomaticRetryStatusCodeRanges
	oldRatios := ratio_setting.ModelRatio2JSONString()
	oldFree := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	affinity := operation_setting.GetChannelAffinitySetting()
	oldAffinity := *affinity
	t.Cleanup(func() {
		common.RetryTimes, common.LogConsumeEnabled, constant.ErrorLogEnabled = oldRetry, oldConsume, oldError
		common.RelayDebugLogEnabled = oldDebug
		constant.StreamingTimeout = oldStreamTimeout
		operation_setting.AutomaticRetryStatusCodeRanges = oldRanges
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = oldFree
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios))
		*affinity = oldAffinity
	})
	common.RetryTimes, common.LogConsumeEnabled, constant.ErrorLogEnabled = 1, false, false
	common.RelayDebugLogEnabled = false
	constant.StreamingTimeout = 30
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"video-routing-test":0}`))
	*affinity = operation_setting.ChannelAffinitySetting{
		Enabled: true, DefaultTTLSeconds: 60, MaxEntries: 100,
		Rules: []operation_setting.ChannelAffinityRule{{Name: "video-routing-test", ModelRegex: []string{"^video-routing-test$"}, KeySources: []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Test-Affinity"}}, IncludeRuleName: true}},
	}
	service.InitHttpClient()

	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cache=%t", cached), func(t *testing.T) {
			common.MemoryCacheEnabled = cached
			for _, tc := range []struct {
				name         string
				disabled     []bool
				retry        bool
				pin          int
				affinity     bool
				textOnly     bool
				wantStatus   int
				wantAttempts []string
			}{
				{name: "skip before priority", disabled: []bool{true, false, true, false}, wantStatus: 200, wantAttempts: []string{"route-2"}},
				{name: "cross-channel retry skips disabled", disabled: []bool{true, false, true, false}, retry: true, wantStatus: 200, wantAttempts: []string{"route-2", "route-4"}},
				{name: "affinity cannot select disabled", disabled: []bool{true, false, true, false}, affinity: true, wantStatus: 200, wantAttempts: []string{"route-2"}},
				{name: "all disabled stops before relay", disabled: []bool{true, true, true, true}, wantStatus: 503},
				{name: "pinned disabled rejects", disabled: []bool{true, false, true, false}, pin: 1, wantStatus: 400},
				{name: "pinned allowed works", disabled: []bool{true, false, true, false}, pin: 4, wantStatus: 200, wantAttempts: []string{"route-4"}},
				{name: "default off permits video", disabled: []bool{false, false, false, false}, wantStatus: 200, wantAttempts: []string{"route-1"}},
				{name: "text unaffected", disabled: []bool{true, true, true, true}, textOnly: true, wantStatus: 200, wantAttempts: []string{"route-1"}},
			} {
				for _, path := range []string{"/v1/chat/completions", "/pg/chat/completions"} {
					for _, stream := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/stream=%t", tc.name, path, stream), func(t *testing.T) {
							var mu sync.Mutex
							var attempts, videoValues []string
							upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								body, readErr := io.ReadAll(r.Body)
								if !assert.NoError(t, readErr) {
									w.WriteHeader(500)
									return
								}
								key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
								mu.Lock()
								attempts = append(attempts, key)
								videoValues = append(videoValues, gjson.GetBytes(body, "messages.0.content.0.video_url").Raw)
								mu.Unlock()
								if tc.retry && key == "route-2" {
									w.Header().Set("Content-Type", "application/json")
									w.WriteHeader(503)
									_, _ = io.WriteString(w, `{"error":{"message":"busy","type":"upstream_error"}}`)
									return
								}
								if stream {
									w.Header().Set("Content-Type", "text/event-stream")
									_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
									return
								}
								w.Header().Set("Content-Type", "application/json")
								_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
							}))
							t.Cleanup(upstream.Close)
							channels := make([]model.Channel, 4)
							for i := range channels {
								priority, weight, autoBan, baseURL := int64(100-i*10), uint(1), 0, upstream.URL
								channels[i] = model.Channel{Type: constant.ChannelTypeOpenAI, Key: fmt.Sprintf("route-%d", i+1), Status: common.ChannelStatusEnabled, Name: fmt.Sprintf("video-route-%d", i), Models: "video-routing-test", Group: "default", BaseURL: &baseURL, Priority: &priority, Weight: &weight, AutoBan: &autoBan}
								channels[i].SetOtherSettings(kitdto.ChannelOtherSettings{DisableVideoUnderstanding: tc.disabled[i], DisableStream: false, RetryZeroOutput: true})
								require.NoError(t, db.Create(&channels[i]).Error)
								ability := model.Ability{Group: "default", Model: "video-routing-test", ChannelId: channels[i].Id, Enabled: true, Priority: &priority, Weight: weight}
								require.NoError(t, db.Create(&ability).Error)
								var saved model.Channel
								require.NoError(t, db.First(&saved, channels[i].Id).Error)
								assert.Equal(t, tc.disabled[i], saved.GetOtherSettings().DisableVideoUnderstanding)
								assert.True(t, saved.GetOtherSettings().RetryZeroOutput, "unrelated settings survive persistence")
							}
							t.Cleanup(func() {
								ids := []int{channels[0].Id, channels[1].Id, channels[2].Id, channels[3].Id}
								require.NoError(t, db.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
								require.NoError(t, db.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
								model.InitChannelCache()
							})
							model.InitChannelCache()
							messages := `[{"role":"user","content":[{"type":"video_url","video_url":"data:video/mp4;base64,AAAA"}]},{"role":"user","content":"describe"}]`
							if tc.textOnly {
								messages = `[{"role":"user","content":"explain video_url"}]`
							}
							requestBody := fmt.Sprintf(`{"model":"video-routing-test","group":"default","stream":%t,"messages":%s}`, stream, messages)
							affinityKey := t.Name()
							if tc.affinity {
								seed, _ := gin.CreateTestContext(httptest.NewRecorder())
								seed.Request = httptest.NewRequest(http.MethodPost, path, nil)
								seed.Request.Header.Set("X-Test-Affinity", affinityKey)
								_, found := service.GetPreferredChannelByAffinity(seed, "video-routing-test", "default")
								require.False(t, found)
								service.RecordChannelAffinity(seed, channels[0].Id)
								got, found := service.GetPreferredChannelByAffinity(seed, "video-routing-test", "default")
								require.True(t, found)
								require.Equal(t, channels[0].Id, got)
								t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(seed) })
							}
							relayCalls := 0
							var relayCtx *gin.Context
							engine := gin.New()
							engine.POST(path, func(c *gin.Context) {
								defer common.CleanupBodyStorage(c)
								common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
								common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
								common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
								if tc.pin > 0 {
									service.GetChannelConstraints(c).AddPin(hostdto.ChannelPin{ChannelId: channels[tc.pin-1].Id, Source: hostdto.PinSourceToken, Rank: hostdto.PinRankToken, RetryMode: hostdto.PinRetrySingleAttempt})
								}
								c.Next()
							}, middleware.Distribute(), func(c *gin.Context) {
								relayCalls++
								relayCtx = c
								body, readErr := io.ReadAll(c.Request.Body)
								require.NoError(t, readErr)
								assert.Equal(t, requestBody, string(body), "distribution must not rewrite the body")
								c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
								Relay(c, types.RelayFormatOpenAI)
							})
							req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(requestBody))
							req.Header.Set("Content-Type", "application/json")
							if tc.affinity {
								req.Header.Set("X-Test-Affinity", affinityKey)
							}
							recorder := httptest.NewRecorder()
							engine.ServeHTTP(recorder, req)
							assert.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
							mu.Lock()
							gotAttempts := append([]string(nil), attempts...)
							gotVideos := append([]string(nil), videoValues...)
							mu.Unlock()
							assert.Equal(t, tc.wantAttempts, gotAttempts)
							if tc.wantStatus != 200 {
								assert.Zero(t, relayCalls, "no relay or pre-consumption for rejected requests")
								if tc.pin > 0 {
									assert.Equal(t, "video_understanding", gjson.Get(recorder.Body.String(), "error.code").String())
								}
								return
							}
							require.Equal(t, 1, relayCalls)
							assert.Len(t, relayCtx.GetStringSlice("use_channel"), len(tc.wantAttempts), "skipped channels consume no retry attempts")
							if !tc.textOnly {
								for _, video := range gotVideos {
									assert.Equal(t, `"data:video/mp4;base64,AAAA"`, video)
								}
							}
							assert.Contains(t, recorder.Body.String(), "ok")
						})
					}
				}
			}
		})
	}
}
