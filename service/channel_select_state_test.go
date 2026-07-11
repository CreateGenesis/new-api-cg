package service

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInterAndSameChannelRetryStatesHaveIndependentStorage(t *testing.T) {
	interChannelState := &InterChannelRetryState{}
	sameChannelState := &SameChannelRetryState{}
	sameChannelState.Increase()
	sameChannelState.Increase()
	sameChannelState.Increase()

	assert.Equal(t, 0, interChannelState.Count())
	assert.Equal(t, 3, sameChannelState.Count())

	interChannelState.Increase()

	assert.Equal(t, 1, interChannelState.Count())
	assert.Equal(t, 3, sameChannelState.Count())

	nextChannelState := &SameChannelRetryState{}
	assert.Equal(t, 1, interChannelState.Count())
	assert.Equal(t, 0, nextChannelState.Count())
}

func TestSelectionSweepStateCannotRewriteRetryCounters(t *testing.T) {
	interChannelState := &InterChannelRetryState{}
	sameChannelState := &SameChannelRetryState{}
	for range 7 {
		interChannelState.Increase()
	}
	for range 2 {
		sameChannelState.Increase()
	}
	selectParam := &ChannelSelectParam{}
	selectParam.ExcludeAttemptedChannel(3)
	selectParam.ExcludeAttemptedChannel(10)

	assert.True(t, selectParam.BeginNextSelectionSweep())
	assert.Equal(t, map[int]struct{}{10: {}}, selectParam.ExcludedChannelIDs)
	assert.Equal(t, 7, interChannelState.Count())
	assert.Equal(t, 2, sameChannelState.Count())

	selectParam.AllowLastAttemptedChannel()
	assert.Empty(t, selectParam.ExcludedChannelIDs)
	assert.Equal(t, 7, interChannelState.Count())
	assert.Equal(t, 2, sameChannelState.Count())
}

func TestAutoGroupSelectionKeepsCrossGroupPolicySeparateFromRetryCounters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["group-a","group-b"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","group-a":"A","group-b":"B"}`))
	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		if originalDB != nil {
			model.InitChannelCache()
		}
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	priority := int64(10)
	weight := uint(1)
	channels := []model.Channel{
		{Id: 1, Name: "group-a", Status: common.ChannelStatusEnabled, Group: "group-a", Models: "retry-model", Priority: &priority, Weight: &weight},
		{Id: 2, Name: "group-b", Status: common.ChannelStatusEnabled, Group: "group-b", Models: "retry-model", Priority: &priority, Weight: &weight},
	}
	abilities := []model.Ability{
		{Group: "group-a", Model: "retry-model", ChannelId: 1, Enabled: true, Priority: &priority, Weight: weight},
		{Group: "group-b", Model: "retry-model", ChannelId: 2, Enabled: true, Priority: &priority, Weight: weight},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&abilities).Error)
	model.InitChannelCache()

	noCrossContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(noCrossContext, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(noCrossContext, constant.ContextKeyTokenCrossGroupRetry, false)
	noCrossParam := &ChannelSelectParam{Ctx: noCrossContext, TokenGroup: "auto", ModelName: "retry-model"}
	channel, group, err := CacheGetRandomSatisfiedChannel(noCrossParam)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 1, channel.Id)
	assert.Equal(t, "group-a", group)

	noCrossParam.ExcludeAttemptedChannel(channel.Id)
	channel, _, err = CacheGetRandomSatisfiedChannel(noCrossParam)
	require.NoError(t, err)
	assert.Nil(t, channel, "disabled cross-group retry must not move to group-b")
	require.True(t, noCrossParam.BeginNextSelectionSweep())
	channel, _, err = CacheGetRandomSatisfiedChannel(noCrossParam)
	require.NoError(t, err)
	assert.Nil(t, channel, "the last channel is deferred for the first pick of a new sweep")
	noCrossParam.AllowLastAttemptedChannel()
	channel, group, err = CacheGetRandomSatisfiedChannel(noCrossParam)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 1, channel.Id)
	assert.Equal(t, "group-a", group)

	crossContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(crossContext, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(crossContext, constant.ContextKeyTokenCrossGroupRetry, true)
	crossParam := &ChannelSelectParam{Ctx: crossContext, TokenGroup: "auto", ModelName: "retry-model"}
	interChannelState := &InterChannelRetryState{}
	sameChannelState := &SameChannelRetryState{}
	for range 4 {
		interChannelState.Increase()
	}
	for range 2 {
		sameChannelState.Increase()
	}
	channel, group, err = CacheGetRandomSatisfiedChannel(crossParam)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 1, channel.Id)
	assert.Equal(t, "group-a", group)
	crossParam.ExcludeAttemptedChannel(channel.Id)

	channel, group, err = CacheGetRandomSatisfiedChannel(crossParam)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 2, channel.Id)
	assert.Equal(t, "group-b", group)
	assert.Equal(t, 4, interChannelState.Count())
	assert.Equal(t, 2, sameChannelState.Count())
	crossParam.ExcludeAttemptedChannel(channel.Id)

	channel, _, err = CacheGetRandomSatisfiedChannel(crossParam)
	require.NoError(t, err)
	assert.Nil(t, channel)
	channel, _, err = CacheGetRandomSatisfiedChannel(crossParam)
	require.NoError(t, err)
	assert.Nil(t, channel, "an exhausted auto-group index must not implicitly reset")
	require.True(t, crossParam.BeginNextSelectionSweep())
	channel, group, err = CacheGetRandomSatisfiedChannel(crossParam)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 1, channel.Id)
	assert.Equal(t, "group-a", group)
	assert.Equal(t, 4, interChannelState.Count())
	assert.Equal(t, 2, sameChannelState.Count())
}
