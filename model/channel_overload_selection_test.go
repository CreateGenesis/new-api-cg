package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRandomSatisfiedChannelExcludingPrefersSamePriorityThenFallsBack(t *testing.T) {
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCacheEnabled })

	priorityHigh := int64(10)
	priorityLow := int64(5)
	weight := uint(1)
	channels := map[int]*Channel{
		1: {Id: 1, Priority: &priorityHigh, Weight: &weight},
		2: {Id: 2, Priority: &priorityHigh, Weight: &weight},
		3: {Id: 3, Priority: &priorityLow, Weight: &weight},
	}

	channelSyncLock.Lock()
	originalGroups := group2model2channels
	originalChannels := channelsIDM
	originalAdvanced := channel2advancedCustomConfig
	group2model2channels = map[string]map[string][]int{"default": {"test-model": {1, 2, 3}}}
	channelsIDM = channels
	channel2advancedCustomConfig = make(map[int]*dto.AdvancedCustomConfig)
	channelSyncLock.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		group2model2channels = originalGroups
		channelsIDM = originalChannels
		channel2advancedCustomConfig = originalAdvanced
		channelSyncLock.Unlock()
	})

	selected, err := GetRandomSatisfiedChannelExcluding("default", "test-model", 0, "", nil, map[int]struct{}{1: {}})
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 2, selected.Id)

	selected, err = GetRandomSatisfiedChannelExcluding("default", "test-model", 1, "", nil, map[int]struct{}{1: {}, 2: {}})
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 3, selected.Id)

	selected, err = GetRandomSatisfiedChannelExcludingPriority("default", "test-model", 0, "", nil, nil, &priorityLow)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 3, selected.Id, "a retry must not climb above its current priority ceiling")
}

func TestGetRandomSatisfiedChannelExcludingRetryIndexDoesNotSkipSamePriority(t *testing.T) {
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCacheEnabled })

	priorityHigh := int64(10)
	priorityLow := int64(5)
	weight := uint(1)
	channels := map[int]*Channel{
		3: {
			Id:            3,
			Priority:      &priorityHigh,
			Weight:        &weight,
			OtherSettings: `{"input_token_routing":{"enabled":true,"min_tokens":800,"max_tokens":500000}}`,
		},
		10: {Id: 10, Priority: &priorityHigh, Weight: &weight, OtherSettings: `{}`},
		11: {
			Id:            11,
			Priority:      &priorityLow,
			Weight:        &weight,
			OtherSettings: `{"input_token_routing":{"enabled":true,"min_tokens":800,"max_tokens":500000}}`,
		},
	}

	channelSyncLock.Lock()
	originalGroups := group2model2channels
	originalChannels := channelsIDM
	originalAdvanced := channel2advancedCustomConfig
	group2model2channels = map[string]map[string][]int{"default": {"test-model": {3, 10, 11}}}
	channelsIDM = channels
	channel2advancedCustomConfig = make(map[int]*dto.AdvancedCustomConfig)
	channelSyncLock.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		group2model2channels = originalGroups
		channelsIDM = originalChannels
		channel2advancedCustomConfig = originalAdvanced
		channelSyncLock.Unlock()
	})

	estimatedTokens := 1200
	selected, err := GetRandomSatisfiedChannelExcluding(
		"default",
		"test-model",
		1,
		"",
		&estimatedTokens,
		map[int]struct{}{3: {}},
	)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 10, selected.Id)

	selected, err = GetRandomSatisfiedChannelExcluding(
		"default",
		"test-model",
		2,
		"",
		&estimatedTokens,
		map[int]struct{}{3: {}, 10: {}},
	)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 11, selected.Id)
}

func TestGetChannelExcludingRetryIndexDoesNotSkipSamePriority(t *testing.T) {
	truncateTables(t)
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCacheEnabled })

	priorityHigh := int64(10)
	priorityLow := int64(5)
	channels := []Channel{
		{
			Id:            3,
			Name:          "limited-high",
			Key:           "sk-3",
			OtherSettings: `{"input_token_routing":{"enabled":true,"min_tokens":800,"max_tokens":500000}}`,
		},
		{Id: 10, Name: "unlimited-high", Key: "sk-10", OtherSettings: `{}`},
		{
			Id:            11,
			Name:          "limited-low",
			Key:           "sk-11",
			OtherSettings: `{"input_token_routing":{"enabled":true,"min_tokens":800,"max_tokens":500000}}`,
		},
	}
	require.NoError(t, DB.Create(&channels).Error)

	abilities := []Ability{
		{Group: "default", Model: "test-model-db", ChannelId: 3, Enabled: true, Priority: &priorityHigh, Weight: 1},
		{Group: "default", Model: "test-model-db", ChannelId: 10, Enabled: true, Priority: &priorityHigh, Weight: 1},
		{Group: "default", Model: "test-model-db", ChannelId: 11, Enabled: true, Priority: &priorityLow, Weight: 1},
	}
	require.NoError(t, DB.Create(&abilities).Error)

	estimatedTokens := 1200
	selected, err := GetChannelExcluding(
		"default",
		"test-model-db",
		1,
		"",
		&estimatedTokens,
		map[int]struct{}{3: {}},
	)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 10, selected.Id)

	selected, err = GetChannelExcluding(
		"default",
		"test-model-db",
		2,
		"",
		&estimatedTokens,
		map[int]struct{}{3: {}, 10: {}},
	)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 11, selected.Id)

	selected, err = GetChannelExcludingPriority("default", "test-model-db", 0, "", &estimatedTokens, nil, &priorityLow)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 11, selected.Id, "the database selector must enforce the same priority ceiling")
}

func TestGetChannelFiltersBeforeChoosingHighestPriority(t *testing.T) {
	truncateTables(t)
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCacheEnabled })

	priorityHigh := int64(10)
	priorityLow := int64(5)
	channels := []Channel{
		{Id: 21, Name: "high-token-mismatch", Key: "sk-21", OtherSettings: `{"input_token_routing":{"enabled":true,"min_tokens":5000,"max_tokens":9000}}`},
		{Id: 22, Name: "low-token-match", Key: "sk-22", OtherSettings: `{"input_token_routing":{"enabled":true,"min_tokens":100,"max_tokens":4999}}`},
	}
	require.NoError(t, DB.Create(&channels).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "filtered-priority", ChannelId: 21, Enabled: true, Priority: &priorityHigh, Weight: 1},
		{Group: "default", Model: "filtered-priority", ChannelId: 22, Enabled: true, Priority: &priorityLow, Weight: 1},
	}).Error)

	estimatedTokens := 1000
	selected, err := GetChannelExcluding("default", "filtered-priority", 0, "", &estimatedTokens, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 22, selected.Id)
}

func TestCachedChannelSelectionFiltersBeforeChoosingHighestPriority(t *testing.T) {
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCacheEnabled })

	priorityHigh := int64(10)
	priorityLow := int64(5)
	weight := uint(1)
	channels := map[int]*Channel{
		31: {Id: 31, Type: constant.ChannelTypeAdvancedCustom, Priority: &priorityHigh, Weight: &weight},
		32: {Id: 32, Type: constant.ChannelTypeAdvancedCustom, Priority: &priorityLow, Weight: &weight},
	}

	channelSyncLock.Lock()
	originalGroups := group2model2channels
	originalChannels := channelsIDM
	originalAdvanced := channel2advancedCustomConfig
	group2model2channels = map[string]map[string][]int{"default": {"path-priority": {31, 32}}}
	channelsIDM = channels
	channel2advancedCustomConfig = map[int]*dto.AdvancedCustomConfig{
		31: {Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/messages"}}},
		32: {Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses"}}},
	}
	channelSyncLock.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		group2model2channels = originalGroups
		channelsIDM = originalChannels
		channel2advancedCustomConfig = originalAdvanced
		channelSyncLock.Unlock()
	})

	selected, err := GetRandomSatisfiedChannelExcluding("default", "path-priority", 0, "/v1/responses", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 32, selected.Id)
}

func TestDatabaseChannelSelectionFiltersPathBeforeChoosingHighestPriority(t *testing.T) {
	truncateTables(t)
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCacheEnabled })

	priorityHigh := int64(10)
	priorityLow := int64(5)
	channels := []Channel{
		{Id: 41, Type: constant.ChannelTypeAdvancedCustom, Name: "high-path-mismatch", Key: "sk-41", OtherSettings: `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/messages"}]}}`},
		{Id: 42, Type: constant.ChannelTypeAdvancedCustom, Name: "low-path-match", Key: "sk-42", OtherSettings: `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/responses"}]}}`},
	}
	require.NoError(t, DB.Create(&channels).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "path-priority-db", ChannelId: 41, Enabled: true, Priority: &priorityHigh, Weight: 1},
		{Group: "default", Model: "path-priority-db", ChannelId: 42, Enabled: true, Priority: &priorityLow, Weight: 1},
	}).Error)

	selected, err := GetChannelExcluding("default", "path-priority-db", 0, "/v1/responses", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 42, selected.Id)
}
