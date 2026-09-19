package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	hostdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGroupSchedulingDatabaseRouting(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("configure " + dialect.env)
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("cache=%t", cached), func(t *testing.T) {
					common.MemoryCacheEnabled = cached
					channels := make([]*model.Channel, 0, 3)
					ids := []int{}
					for i := range 3 {
						channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: fmt.Sprintf("scheduling-%d", i), Key: "test", Models: "scheduling-model,other-model", Group: "default,vip", Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(30 - i*10)), Weight: common.GetPointer(uint(1))}
						channel.SetOtherSettings(dto.ChannelOtherSettings{RetryZeroOutput: true, GroupScheduling: &dto.GroupSchedulingSettings{Enabled: true, Groups: []string{"default"}, CostFactor: common.GetPointer(float64(3 - i)), ProbeModels: []string{"scheduling-model"}, IntervalSeconds: 30, TimeoutSeconds: 30}})
						require.NoError(t, channel.ValidateSettings())
						require.NoError(t, db.Create(channel).Error)
						for _, group := range []string{"default", "vip"} {
							for _, name := range []string{"scheduling-model", "other-model"} {
								require.NoError(t, db.Create(&model.Ability{Group: group, Model: name, ChannelId: channel.Id, Enabled: true, Priority: channel.Priority, Weight: 1}).Error)
							}
						}
						model.CompleteGroupProbe(channel, "scheduling-model", "", time.Duration(2000+i*4000)*time.Millisecond, true, time.Now())
						channels = append(channels, channel)
						ids = append(ids, channel.Id)
					}
					t.Cleanup(func() {
						require.NoError(t, db.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
						require.NoError(t, db.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
					})
					model.InitChannelCache()
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					param := &service.ChannelSelectParam{Ctx: ctx, TokenGroup: "default", ModelName: "scheduling-model"}
					selected, group, err := service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, "default", group)
					assert.Equal(t, channels[1].Id, selected.Id, "cost wins only within fastest + 5 seconds")
					param.ExcludeAttemptedChannel(selected)
					require.NotNil(t, param.MaxPriority)
					assert.EqualValues(t, 30, *param.MaxPriority)
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, channels[0].Id, selected.Id, "retry must retain the faster higher-priority peer")
					param.ExcludedChannelIDs = nil
					param.MaxPriority = nil
					require.NoError(t, model.UpdateOption(operation_setting.GroupSchedulingToleranceKey, `{"default":4000}`))
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, channels[1].Id, selected.Id, "threshold is inclusive")
					require.NoError(t, model.UpdateOption(operation_setting.GroupSchedulingToleranceKey, `{"default":3999}`))
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, channels[0].Id, selected.Id)
					require.NoError(t, model.UpdateOption(operation_setting.GroupSchedulingToleranceKey, `{}`))
					for _, mode := range []types.RequestMode{types.RequestModeStream, types.RequestModeNonStream} {
						param.ClientRequestMode = mode
						selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
						require.NoError(t, err)
						require.NotNil(t, selected)
						assert.Equal(t, channels[1].Id, selected.Id)
					}
					param.TokenGroup = "vip"
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, channels[0].Id, selected.Id, "unselected groups preserve priority")
					param.TokenGroup = "default"
					param.ModelName = "other-model"
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, channels[0].Id, selected.Id)
					param.ModelName = "scheduling-model"
					model.CompleteGroupProbe(channels[1], param.ModelName, "", 0, false, time.Now())
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, channels[0].Id, selected.Id)
					for _, channel := range channels {
						model.CompleteGroupProbe(channel, param.ModelName, "", 0, false, time.Now())
					}
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					assert.Nil(t, selected, "failed probes never fall back to unhealthy participants")
					assert.True(t, model.GroupSchedulingAvailable(channels[0], "vip", param.ModelName))
					assert.True(t, model.GroupSchedulingAvailable(channels[0], "default", "other-model"))
					assert.False(t, model.GroupSchedulingAvailable(channels[0], "default", param.ModelName))
					var persisted model.Channel
					require.NoError(t, db.First(&persisted, channels[0].Id).Error)
					assert.Equal(t, common.ChannelStatusEnabled, persisted.Status)
					assert.True(t, persisted.GetOtherSettings().RetryZeroOutput)
					assert.True(t, persisted.GetOtherSettings().GroupScheduling.Enabled)
					model.CompleteGroupProbe(channels[1], param.ModelName, "", time.Second, true, time.Now())
					selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, channels[1].Id, selected.Id)
				})
			}
		})
	}
}

func TestGroupSchedulingProbeBackoffAndRedisLease(t *testing.T) {
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled, common.RDB = true, client
	t.Cleanup(func() { common.RedisEnabled, common.RDB = oldEnabled, oldRDB; require.NoError(t, client.Close()) })
	channel := &model.Channel{Id: 89001, Key: "fixture", OtherSettings: `{"group_scheduling":{"enabled":true,"groups":["default"],"probe_models":["m"]}}`}
	now := time.Now()
	expected := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 6400 * time.Millisecond, 12800 * time.Millisecond, 25600 * time.Millisecond, 30 * time.Second, 30 * time.Second}
	for i, delay := range expected {
		owner, claimed := model.TryGroupProbe(channel, "m", now)
		require.True(t, claimed)
		_, duplicate := model.TryGroupProbe(channel, "m", now)
		assert.False(t, duplicate)
		model.CompleteGroupProbe(channel, "m", owner, 0, false, now)
		state := model.GroupProbeStates([]*model.Channel{channel}, "m")[channel.Id]
		assert.False(t, state.Healthy)
		assert.Equal(t, i+1, state.Failures)
		assert.Equal(t, now.Add(delay).UnixMilli(), state.NextAt)
		_, tooEarly := model.TryGroupProbe(channel, "m", now)
		assert.False(t, tooEarly)
		now = now.Add(delay)
	}
	owner, claimed := model.TryGroupProbe(channel, "m", now)
	require.True(t, claimed)
	model.CompleteGroupProbe(channel, "m", owner, 1500*time.Millisecond, true, now)
	state := model.GroupProbeStates([]*model.Channel{channel}, "m")[channel.Id]
	assert.True(t, model.GroupProbeFresh(state, now))
	assert.Zero(t, state.Failures)
	assert.False(t, model.GroupProbeFresh(state, now.Add(91*time.Second)))
	assert.EqualValues(t, 1500, state.TTFTMilliseconds)
	assert.Equal(t, 5*time.Second, model.GroupProbeBackoff(8, 5*time.Second))
	other := &model.Channel{Id: 89004, Key: "fixture", OtherSettings: channel.OtherSettings}
	key := model.GroupProbeKey(other, "m")
	server.Set(key+":lease", "another-instance")
	_, claimedElsewhere := model.TryGroupProbe(other, "m", now)
	assert.False(t, claimedElsewhere, "Redis lease blocks a second instance")
	server.Del(key + ":lease")
	owner, claimed = model.TryGroupProbe(other, "m", now)
	require.True(t, claimed)
	model.CompleteGroupProbe(other, "m", "stale-owner", time.Second, true, now)
	assert.Empty(t, model.GroupProbeStates([]*model.Channel{other}, "m"), "stale leases cannot publish health")
	model.CompleteGroupProbe(other, "m", owner, time.Second, true, now)
	require.True(t, model.GroupProbeFresh(model.GroupProbeStates([]*model.Channel{other}, "m")[other.Id], now))
	channel.Key = "changed-fixture"
	assert.Empty(t, model.GroupProbeStates([]*model.Channel{channel}, "m"), "credential changes invalidate old samples")
}

func TestGroupSchedulingProbeUsesMappingUniquePromptAndNoBilling(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	service.InitHttpClient()
	user := model.User{Username: "probe-user", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", Quota: 12345}
	require.NoError(t, db.Create(&user).Error)
	prompts := make(chan string, 2)
	var interrupted atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			w.WriteHeader(500)
			return
		}
		assert.Equal(t, "upstream-probe", gjson.GetBytes(body, "model").String())
		assert.True(t, gjson.GetBytes(body, "stream").Bool())
		prompts <- gjson.GetBytes(body, "messages.0.content").String()
		w.Header().Set("Content-Type", "text/event-stream")
		if interrupted.Load() {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	channel := &model.Channel{Id: 89002, Type: constant.ChannelTypeOpenAI, Key: "test", Models: "probe-alias", Group: "default", BaseURL: common.GetPointer(upstream.URL), ModelMapping: common.GetPointer(`{"probe-alias":"upstream-probe"}`)}
	for range 2 {
		result := runChannelTest(context.Background(), channel, user.Id, "probe-alias", "", true, nil, true)
		require.NoError(t, result.localErr)
		require.Nil(t, result.newAPIError)
		assert.Positive(t, result.ttft)
	}
	first, second := <-prompts, <-prompts
	assert.True(t, strings.HasPrefix(first, "Reply only with p"))
	assert.NotEqual(t, first, second)
	var saved model.User
	require.NoError(t, db.First(&saved, user.Id).Error)
	assert.Equal(t, user.Quota, saved.Quota)
	var count int64
	require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := runChannelTest(ctx, channel, user.Id, "probe-alias", "", true, nil, true)
	assert.Error(t, result.localErr)
	assert.Error(t, prepareGroupProbeRequest(&dto.EmbeddingRequest{}))
	interrupted.Store(true)
	result = runChannelTest(context.Background(), channel, user.Id, "probe-alias", "", true, nil, true)
	assert.Error(t, result.localErr, "a stream that ends before its protocol terminator is not a successful probe")
}

func TestGroupSchedulingDistributionAffinityAndPins(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	oldAffinity := *operation_setting.GetChannelAffinitySetting()
	t.Cleanup(func() { *operation_setting.GetChannelAffinitySetting() = oldAffinity })
	*operation_setting.GetChannelAffinitySetting() = operation_setting.ChannelAffinitySetting{Enabled: true, DefaultTTLSeconds: 60, MaxEntries: 100, Rules: []operation_setting.ChannelAffinityRule{{Name: "scheduling-test", ModelRegex: []string{"^scheduling-model$"}, KeySources: []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Scheduling-Affinity"}}, IncludeRuleName: true}}}
	channels := make([]model.Channel, 3)
	for i := range channels {
		channels[i] = model.Channel{Type: constant.ChannelTypeOpenAI, Key: "fixture", Status: common.ChannelStatusEnabled, Group: "default", Models: "scheduling-model", Priority: common.GetPointer(int64(30 - i*10)), Weight: common.GetPointer(uint(1))}
		channels[i].SetOtherSettings(dto.ChannelOtherSettings{GroupScheduling: &dto.GroupSchedulingSettings{Enabled: i < 2, Groups: []string{"default"}, ProbeModels: []string{"scheduling-model"}, CostFactor: common.GetPointer(float64(3 - i))}})
		require.NoError(t, db.Create(&channels[i]).Error)
		require.NoError(t, db.Create(&model.Ability{ChannelId: channels[i].Id, Group: "default", Model: "scheduling-model", Enabled: true, Priority: channels[i].Priority, Weight: 1}).Error)
		model.CompleteGroupProbe(&channels[i], "scheduling-model", "", time.Duration(i+1)*time.Second, true, time.Now())
	}
	t.Run("revalidation preserves the previously selected scheduling pool", func(t *testing.T) {
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[2].Id).Update("priority", 100).Error)
		require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", channels[2].Id).Update("priority", 100).Error)
		t.Cleanup(func() {
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[2].Id).Update("priority", 10).Error)
			require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", channels[2].Id).Update("priority", 10).Error)
		})
		anchor := channels[1]
		anchor.SchedulingPriority = common.GetPointer(int64(30))
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		param := &service.ChannelSelectParam{Ctx: ctx, TokenGroup: "default", ModelName: "scheduling-model", SchedulingAnchor: &anchor, SchedulingAnchorGroup: "default"}
		selected, _, err := service.CacheGetRandomSatisfiedChannel(param)
		require.NoError(t, err)
		require.NotNil(t, selected)
		assert.Equal(t, channels[1].Id, selected.Id, "do not draw again against channels outside the pool")
		param.ExcludeAttemptedChannel(selected)
		require.NotNil(t, param.MaxPriority)
		assert.EqualValues(t, 30, *param.MaxPriority)
	})
	for _, tc := range []struct {
		name          string
		affinity, pin int
		plainPriority bool
		failed        bool
		want          int
		status        int
	}{
		{name: "scheduled affinity is overridden", affinity: 0, pin: -1, want: 1, status: 200},
		{name: "nonparticipant affinity is retained", affinity: 2, pin: -1, want: 2, status: 200},
		{name: "nonparticipant priority is retained", affinity: -1, pin: -1, plainPriority: true, want: 2, status: 200},
		{name: "explicit pin is retained", affinity: -1, pin: 0, want: 0, status: 200},
		{name: "unhealthy pinned model is rejected", affinity: -1, pin: 0, failed: true, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.plainPriority {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[2].Id).Update("priority", 100).Error)
				require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", channels[2].Id).Update("priority", 100).Error)
				t.Cleanup(func() {
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[2].Id).Update("priority", 10).Error)
					require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", channels[2].Id).Update("priority", 10).Error)
				})
			}
			if tc.failed {
				model.CompleteGroupProbe(&channels[0], "scheduling-model", "", 0, false, time.Now())
			}
			if tc.affinity >= 0 {
				seed, _ := gin.CreateTestContext(httptest.NewRecorder())
				seed.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
				seed.Request.Header.Set("X-Scheduling-Affinity", t.Name())
				service.GetPreferredChannelByAffinity(seed, "scheduling-model", "default")
				service.RecordChannelAffinity(seed, channels[tc.affinity].Id)
				t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(seed) })
			}
			engine := gin.New()
			engine.POST("/v1/chat/completions", func(c *gin.Context) {
				defer common.CleanupBodyStorage(c)
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				if tc.pin >= 0 {
					service.GetChannelConstraints(c).AddPin(hostdto.ChannelPin{ChannelId: channels[tc.pin].Id, Source: hostdto.PinSourceToken})
				}
				c.Next()
			}, middleware.Distribute(), func(c *gin.Context) {
				c.JSON(200, gin.H{"channel": common.GetContextKeyInt(c, constant.ContextKeyChannelId)})
			})
			request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"scheduling-model","messages":[{"role":"user","content":"hi"}]}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Scheduling-Affinity", t.Name())
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			if tc.status == 200 {
				assert.EqualValues(t, channels[tc.want].Id, gjson.Get(recorder.Body.String(), "channel").Int())
			}
		})
	}
}

func TestGroupSchedulingSettingsValidationAndSampleIdentity(t *testing.T) {
	channel := &model.Channel{Id: 89003, Key: "fixture", Group: "default", Models: "m"}
	settings := dto.ChannelOtherSettings{GroupScheduling: &dto.GroupSchedulingSettings{Enabled: true, Groups: []string{"default"}, ProbeModels: []string{"m"}, CostFactor: common.GetPointer(float64(0))}}
	channel.SetOtherSettings(settings)
	require.NoError(t, channel.ValidateSettings())
	assert.Zero(t, channel.GetOtherSettings().GroupScheduling.Cost())
	identity := model.GroupProbeKey(channel, "m")
	settings.GroupScheduling.CostFactor = common.GetPointer(float64(2))
	channel.SetOtherSettings(settings)
	assert.Equal(t, identity, model.GroupProbeKey(channel, "m"), "cost changes must not pause traffic for a new probe")
	settings.GroupScheduling.Groups = []string{"vip"}
	channel.SetOtherSettings(settings)
	assert.Error(t, channel.ValidateSettings())
	assert.Equal(t, identity, model.GroupProbeKey(channel, "m"))
	settings.GroupScheduling.Groups = []string{"default"}
	settings.GroupScheduling.ProbeModels = []string{"other"}
	channel.SetOtherSettings(settings)
	assert.Error(t, channel.ValidateSettings())
	settings.GroupScheduling.ProbeModels = []string{"m"}
	settings.GroupScheduling.IntervalSeconds = -1
	channel.SetOtherSettings(settings)
	assert.Error(t, channel.ValidateSettings())
	assert.Error(t, operation_setting.ValidateGroupSchedulingTolerance(`{"default":-1}`))
	assert.Error(t, operation_setting.ValidateGroupSchedulingTolerance(`null`))
	require.NoError(t, operation_setting.ValidateGroupSchedulingTolerance(`{"default":0}`))
	assert.False(t, model.GroupSchedulingApplies(&model.Channel{}, "default", "m"))
}
