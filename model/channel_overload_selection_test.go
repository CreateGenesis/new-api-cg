package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
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
}
