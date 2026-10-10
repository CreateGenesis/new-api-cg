package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/videocompat"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
								if path == "/pg/chat/completions" {
									assert.False(t, gjson.GetBytes(body, "group").Exists(), "playground routing fields must stay in the gateway")
								} else if !tc.textOnly {
									assert.Equal(t, "default", gjson.GetBytes(body, "group").String(), "ordinary API unknown parameters must survive")
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

// Real FFmpeg plus ordinary relay handlers verify native/A/B retries and JSON/SSE.
func TestVideoCompatibilityRelayProtocolsAndVariants(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("real ffmpeg unavailable")
	}
	fixture := t.TempDir() + "/fixture.mp4"
	output, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-t", "2", "-an", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-y", fixture).CombinedOutput()
	require.NoError(t, err, string(output))
	media, err := os.ReadFile(fixture)
	require.NoError(t, err)
	uri := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(media)
	oldDB, oldLog := model.DB, model.LOG_DB
	oldTypes, oldLogTypes := common.MainDatabaseType(), common.LogDatabaseType()
	oldPath, oldMaster, oldCache, oldRedis := common.SQLitePath, common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled
	oldRatio := ratio_setting.ModelRatio2JSONString()
	oldRetry, oldConsume, oldErrors, oldCount := common.RetryTimes, common.LogConsumeEnabled, constant.ErrorLogEnabled, constant.CountToken
	oldRetryRanges := operation_setting.AutomaticRetryStatusCodeRanges
	oldConfig := videocompat.GetConfig()
	oldStreamTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		constant.StreamingTimeout = oldStreamTimeout
		common.SetDatabaseTypes(oldTypes, oldLogTypes)
		common.SQLitePath, common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = oldPath, oldMaster, oldCache, oldRedis
		common.RetryTimes, common.LogConsumeEnabled, constant.ErrorLogEnabled, constant.CountToken = oldRetry, oldConsume, oldErrors, oldCount
		operation_setting.AutomaticRetryStatusCodeRanges = oldRetryRanges
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio))
		require.NoError(t, videocompat.SetConfig(oldConfig))
	})
	t.Setenv("SQL_DSN", "")
	common.SQLitePath = "file:video-compat-relay?mode=memory&cache=shared"
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}, &model.Log{}, &model.RelayDebugPayload{}))
	user := model.User{Username: "video-compat-relay", Quota: 1000000}
	require.NoError(t, db.Create(&user).Error)
	common.RetryTimes, common.LogConsumeEnabled, constant.ErrorLogEnabled, constant.CountToken = 2, true, false, true
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 503, End: 503}}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"video-compat-relay":0}`))
	require.NoError(t, videocompat.SetConfig(videocompat.DefaultConfig()))
	service.InitHttpClient()
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				path, format, channelType := "/v1/chat/completions", types.RelayFormatOpenAI, constant.ChannelTypeOpenAI
				contentField := "messages"
				content := fmt.Sprintf(`[{"role":"user","custom_message":false,"content":[{"type":"text","text":"before","custom_block":0,"cache_control":{"type":"ephemeral"}},{"type":"video_url","video_url":"%s"},{"type":"text","text":"after","custom_block":false}]}]`, uri)
				controls := `"tools":[{"type":"function","custom_tool":0,"cache_control":{"type":"ephemeral"},"function":{"name":"lookup","custom_function":false,"parameters":{"type":"object"}}}],"thinking":{"type":"disabled","custom_reasoning":false,"custom_zero":0}`
				switch protocol {
				case "responses":
					path, format, contentField = "/v1/responses", types.RelayFormatOpenAIResponses, "input"
					content = fmt.Sprintf(`[{"role":"user","custom_message":false,"content":[{"type":"input_text","text":"before","custom_block":0,"cache_control":{"type":"ephemeral"}},{"type":"input_video","video_url":"%s"},{"type":"input_text","text":"after","custom_block":false}]}]`, uri)
					controls = `"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"custom_tool":0}],"reasoning":{"effort":"low","custom_reasoning":false,"custom_zero":0}`
				case "messages":
					path, format, channelType = "/v1/messages", types.RelayFormatClaude, constant.ChannelTypeAnthropic
					content = fmt.Sprintf(`[{"role":"user","custom_message":false,"content":[{"type":"text","text":"before","custom_block":0,"cache_control":{"type":"ephemeral"}},{"type":"video","source":{"type":"base64","media_type":"video/mp4","data":"%s"},"cache_control":{"type":"ephemeral"}},{"type":"text","text":"after","custom_block":false}]}]`, base64.StdEncoding.EncodeToString(media))
					controls = `"tools":[{"name":"lookup","input_schema":{"type":"object"},"custom_tool":0,"cache_control":{"type":"ephemeral"}}],"thinking":{"type":"disabled","custom_reasoning":false,"custom_zero":0},"response_format":{"type":"text"}`
				}
				requestBody := fmt.Sprintf(`{"model":"video-compat-relay","stream":%t,"max_tokens":16,"custom_parameter":{"zero":0,"flag":false},"custom.parameter":false,%s,"%s":%s}`, stream, controls, contentField, content)
				var mu sync.Mutex
				var attempts []string
				beforeStats := videocompat.Stats()
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					if protocol == "messages" {
						key = r.Header.Get("X-Api-Key")
					}
					mu.Lock()
					attempts = append(attempts, key)
					mu.Unlock()
					assert.Equal(t, `{"zero":0,"flag":false}`, gjson.GetBytes(body, "custom_parameter").Raw)
					assert.Equal(t, "false", gjson.GetBytes(body, `custom\.parameter`).Raw)
					assert.Equal(t, "false", gjson.GetBytes(body, contentField+".0.custom_message").Raw)
					assert.Equal(t, "0", gjson.GetBytes(body, "tools.0.custom_tool").Raw)
					thinkingField := "thinking"
					if protocol == "responses" {
						thinkingField = "reasoning"
					} else {
						assert.Equal(t, "ephemeral", gjson.GetBytes(body, "tools.0.cache_control.type").String())
					}
					assert.Equal(t, "false", gjson.GetBytes(body, thinkingField+".custom_reasoning").Raw)
					assert.Equal(t, "0", gjson.GetBytes(body, thinkingField+".custom_zero").Raw)
					if protocol == "chat" {
						assert.Equal(t, "false", gjson.GetBytes(body, "tools.0.function.custom_function").Raw)
					}
					if protocol == "messages" {
						assert.False(t, gjson.GetBytes(body, "response_format").Exists(), "known fields removed by the adaptor must stay removed")
					}
					parts := gjson.GetBytes(body, contentField+".0.content").Array()
					assert.Equal(t, "before", parts[0].Get("text").String())
					assert.Equal(t, "0", parts[0].Get("custom_block").Raw)
					assert.Equal(t, "ephemeral", parts[0].Get("cache_control.type").String())
					assert.Equal(t, "after", parts[len(parts)-1].Get("text").String())
					assert.Equal(t, "false", parts[len(parts)-1].Get("custom_block").Raw)
					if key == "native" {
						assert.Len(t, parts, 3)
					} else {
						expected, width, height := 4, 256, 144
						if key == "smaller" {
							expected, width, height = 2, 128, 72
						}
						images := 0
						for _, part := range parts {
							kind := part.Get("type").String()
							if kind != "image_url" && kind != "input_image" && kind != "image" {
								continue
							}
							images++
							if protocol == "messages" {
								assert.Equal(t, images == expected, part.Get("cache_control").Exists(), "the video cache boundary belongs only to the final frame")
							}
							data := part.Get("image_url.url").String()
							if protocol == "responses" {
								data = part.Get("image_url").String()
							}
							if protocol == "messages" {
								data = part.Get("source.data").String()
							}
							if strings.HasPrefix(data, "data:") {
								_, data, _ = strings.Cut(data, ",")
							}
							decoded, err := base64.StdEncoding.DecodeString(data)
							require.NoError(t, err)
							size, err := jpeg.DecodeConfig(bytes.NewReader(decoded))
							require.NoError(t, err)
							assert.Equal(t, width, size.Width)
							assert.Equal(t, height, size.Height)
						}
						assert.Equal(t, expected, images)
					}
					if key != "smaller" {
						w.WriteHeader(503)
						_, _ = io.WriteString(w, `{"error":{"message":"busy","type":"upstream_error"}}`)
						return
					}
					if !stream {
						w.Header().Set("Content-Type", "application/json")
						switch protocol {
						case "chat":
							_, _ = io.WriteString(w, `{"id":"video-result","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":21,"completion_tokens":3,"total_tokens":24}}`)
						case "responses":
							_, _ = io.WriteString(w, `{"id":"resp_video","object":"response","status":"completed","model":"video-compat-relay","output":[{"id":"msg_video","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":21,"output_tokens":3,"total_tokens":24}}`)
						case "messages":
							_, _ = io.WriteString(w, `{"id":"msg_video","type":"message","role":"assistant","model":"video-compat-relay","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":21,"output_tokens":3}}`)
						}
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch protocol {
					case "chat":
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":21,\"completion_tokens\":3,\"total_tokens\":24}}\n\ndata: [DONE]\n\n")
					case "responses":
						_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\",\"output_index\":0,\"content_index\":0}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_video\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":21,\"output_tokens\":3,\"total_tokens\":24}}}\n\n")
					case "messages":
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_video\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":21,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					}
				}))
				defer upstream.Close()
				var ids []int
				for index, key := range []string{"native", "images", "smaller"} {
					priority, weight, baseURL, autoBan := int64(30-index*10), uint(1), upstream.URL, 0
					channel := model.Channel{Type: channelType, Key: key, Name: key, Models: "video-compat-relay", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight, BaseURL: &baseURL, AutoBan: &autoBan}
					settings := kitdto.DefaultVideoCompatibilitySettings()
					settings.Enabled = index > 0
					settings.FPS = 2
					settings.MaxFrames = 4
					settings.MaxSide = 256
					if index == 2 {
						settings.FPS = 1
						settings.MaxFrames = 2
						settings.PixelPercent = 25
					}
					channel.SetOtherSettings(kitdto.ChannelOtherSettings{VideoCompatibility: &settings})
					require.NoError(t, db.Create(&channel).Error)
					ids = append(ids, channel.Id)
					require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "video-compat-relay", ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: weight}).Error)
				}
				defer func() {
					require.NoError(t, db.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
					require.NoError(t, db.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
				}()
				engine := gin.New()
				engine.POST(path, func(c *gin.Context) {
					defer common.CleanupBodyStorage(c)
					common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
					common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
					common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
					common.SetContextKey(c, constant.ContextKeyUserSetting, kitdto.UserSetting{BillingPreference: "wallet_only"})
					common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
					common.SetContextKey(c, constant.ContextKeyUserQuota, user.Quota)
					common.SetContextKey(c, common.RequestIdKey, t.Name())
					c.Next()
				}, middleware.Distribute(), func(c *gin.Context) { Relay(c, format) })
				request := httptest.NewRequest("POST", path, strings.NewReader(requestBody))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, request)
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
				assert.Contains(t, recorder.Body.String(), "ok")
				mu.Lock()
				assert.Equal(t, []string{"native", "images", "smaller"}, attempts)
				mu.Unlock()
				afterStats := videocompat.Stats()
				assert.Equal(t, beforeStats.BufferBytes, afterStats.BufferBytes)
				assert.Equal(t, beforeStats.DiskBytes, afterStats.DiskBytes)
				assert.Equal(t, 0, afterStats.Running)
				var logs []model.Log
				require.NoError(t, db.Where("request_id = ? AND type = ?", t.Name(), model.LogTypeConsume).Find(&logs).Error)
				require.Len(t, logs, 1)
				assert.Equal(t, 21, logs[0].PromptTokens)
			})
		}
	}
}

// Variant reservations retain one frozen contract and never release funds mid-retry.
type videoBillingReservation struct {
	quota   int
	targets []int
}

func (b *videoBillingReservation) Reserve(target int) error {
	b.targets = append(b.targets, target)
	b.quota = max(b.quota, target)
	return nil
}
func (b *videoBillingReservation) GetPreConsumedQuota() int { return b.quota }
func (*videoBillingReservation) Settle(int) error           { return nil }
func (*videoBillingReservation) Refund(*gin.Context)        {}
func (*videoBillingReservation) NeedsRefund() bool          { return true }

func TestVideoVariantBillingPreservesFrozenExpressionAndInput(t *testing.T) {
	const expression = `param("factor") == 2 ? tier("frozen", p * 2) : tier("other", p * 20)`
	b := &videoBillingReservation{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"factor":99}`))
	info := &relaycommon.RelayInfo{Billing: b, UsingGroup: "default", UserGroup: "default", StrictPreConsume: true,
		BillingRequestInput:   &billingexpr.RequestInput{Body: []byte(`{"factor":2}`)},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), PreConsumeMultiplier: 1, QuotaPerUnit: common.QuotaPerUnit}}
	for _, tokens := range []int{100, 400, 50, 400} {
		require.Nil(t, relay.RefreshVariantBilling(c, info, tokens))
		assert.Equal(t, expression, info.TieredBillingSnapshot.ExprString)
		assert.Equal(t, `{"factor":2}`, string(info.BillingRequestInput.Body))
		assert.Equal(t, tokens, info.TieredBillingSnapshot.EstimatedPromptTokens)
		assert.Equal(t, "frozen", info.TieredBillingSnapshot.EstimatedTier)
	}
	assert.Equal(t, b.targets[1], b.GetPreConsumedQuota())
	assert.Less(t, b.targets[2], b.GetPreConsumedQuota())
	assert.Same(t, b, info.Billing)
}

func TestVideoCompatibilityFallbackPreparationErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldPath, oldMaster := common.SQLitePath, common.IsMasterNode
	oldCache, oldRedis := common.MemoryCacheEnabled, common.RedisEnabled
	t.Setenv("SQL_DSN", "")
	common.SQLitePath = t.TempDir() + "/video-preparation-errors.db"
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMainType, oldLogType)
		common.SQLitePath, common.IsMasterNode = oldPath, oldMaster
		common.MemoryCacheEnabled, common.RedisEnabled = oldCache, oldRedis
	})
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	priority, weight := int64(10), uint(1)
	fallback := model.Channel{Type: constant.ChannelTypeOpenAI, Key: "fallback-key", Status: common.ChannelStatusEnabled,
		Name: "video-preparation-fallback", Models: "video-preparation-test", Group: "default", Priority: &priority, Weight: &weight}
	require.NoError(t, db.Create(&fallback).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: fallback.Models, ChannelId: fallback.Id, Enabled: true, Priority: &priority, Weight: weight}).Error)

	for _, path := range []string{"initial selection", "overload fallback", "request mode fallback"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%s/status=%d", path, status), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				info := &relaycommon.RelayInfo{OriginModelName: fallback.Models, UsingGroup: "default", UserGroup: "default"}
				preparations := 0
				param := &service.ChannelSelectParam{Ctx: c, TokenGroup: "default", ModelName: fallback.Models,
					RequestPath: c.Request.URL.Path, ClientRequestMode: types.RequestModeStream,
					PrepareCandidate: func(channel *model.Channel) (*kitdto.InputTokenEstimates, error) {
						assert.Equal(t, fallback.Id, channel.Id)
						preparations++
						return nil, &videocompat.Error{StatusCode: status, Code: "media_test_failure", Message: "local media preparation failed"}
					}}
				var apiErr *types.NewAPIError
				if path == "initial selection" {
					_, apiErr = selectChannelByInputTokenRouting(c, info, param)
				} else {
					native := &model.Channel{Id: 940018 + status, Type: constant.ChannelTypeOpenAI, Key: "native-key", Name: "native",
						Priority: &priority, ChannelInfo: model.ChannelInfo{ChannelOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 1}}}
					if path == "request mode fallback" {
						native.SetOtherSettings(kitdto.ChannelOtherSettings{DisableStream: true})
					} else {
						held, scope, leaseErr := service.AcquireChannelOverloadLease(c.Request.Context(), native, 0)
						require.NoError(t, leaseErr)
						require.NotNil(t, held)
						require.Empty(t, scope)
						t.Cleanup(func() {
							held.Release(context.Background())
							service.ResetChannelOverloadState(context.Background(), native.Id, 1)
						})
					}
					require.Nil(t, middleware.SetupContextForSelectedChannel(c, native, fallback.Models))
					selected, lease, admissionErr := acquireRelayOverloadLease(c, info, param, native, false)
					assert.Same(t, native, selected)
					assert.Nil(t, lease)
					apiErr = admissionErr
				}
				require.NotNil(t, apiErr)
				assert.Equal(t, status, apiErr.StatusCode)
				assert.Equal(t, types.ErrorCode("video_processing_failed"), apiErr.GetErrorCode())
				assert.True(t, types.IsSkipRetryError(apiErr))
				policy := relayRetryPolicy{retryTimes: 2, statusCodeRanges: []operation_setting.StatusCodeRange{{Start: 400, End: 599}}}
				assert.False(t, shouldRetryWithPolicy(c, apiErr, policy, 0))
				assert.Equal(t, 1, preparations)
				assert.Empty(t, c.GetStringSlice("use_channel"), "local preparation must fail before an upstream attempt")
				var unchanged model.Channel
				require.NoError(t, db.First(&unchanged, fallback.Id).Error)
				assert.Equal(t, common.ChannelStatusEnabled, unchanged.Status)
			})
		}
	}

	t.Run("database failure remains channel selection error", func(t *testing.T) {
		require.NoError(t, db.Migrator().DropTable(&model.Ability{}))
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		info := &relaycommon.RelayInfo{OriginModelName: fallback.Models, UsingGroup: "default", UserGroup: "default"}
		param := &service.ChannelSelectParam{Ctx: c, TokenGroup: "default", ModelName: fallback.Models,
			PrepareCandidate: func(*model.Channel) (*kitdto.InputTokenEstimates, error) {
				t.Error("candidate preparation must not run after a database selection failure")
				return nil, nil
			}}
		_, apiErr := selectChannelByInputTokenRouting(c, info, param)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeGetChannelFailed, apiErr.GetErrorCode())
		assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	})
}
