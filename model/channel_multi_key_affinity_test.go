package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiKeyAffinityReturnsSameKeyForSameAffinityValue(t *testing.T) {
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	channel := &Channel{
		Id:  900001,
		Key: "upstream-a\nupstream-b\nupstream-c",
		ChannelInfo: ChannelInfo{
			IsMultiKey:                 true,
			MultiKeyMode:               constant.MultiKeyModeAffinity,
			MultiKeyAffinityTTLSeconds: 60,
			MultiKeyStatusList:         map[int]int{},
			MultiKeyDisabledReason:     map[int]string{},
			MultiKeyDisabledTime:       map[int]int64{},
		},
	}

	firstKey, firstIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-a")
	require.Nil(t, err)
	secondKey, secondIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-a")
	require.Nil(t, err)

	assert.Equal(t, firstKey, secondKey)
	assert.Equal(t, firstIndex, secondIndex)
}

func TestMultiKeyAffinitySkipsDisabledCachedKey(t *testing.T) {
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	channel := &Channel{
		Id:  900002,
		Key: "upstream-a\nupstream-b\nupstream-c",
		ChannelInfo: ChannelInfo{
			IsMultiKey:                 true,
			MultiKeyMode:               constant.MultiKeyModeAffinity,
			MultiKeyAffinityTTLSeconds: 60,
			MultiKeyStatusList:         map[int]int{},
		},
	}

	_, cachedIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-b")
	require.Nil(t, err)

	channel.ChannelInfo.MultiKeyStatusList[cachedIndex] = common.ChannelStatusManuallyDisabled

	nextKey, nextIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-b")
	require.Nil(t, err)

	assert.NotEqual(t, cachedIndex, nextIndex)
	assert.NotEqual(t, fmt.Sprintf("upstream-%c", 'a'+rune(cachedIndex)), nextKey)
	assert.NotEqual(t, common.ChannelStatusManuallyDisabled, channel.ChannelInfo.MultiKeyStatusList[nextIndex])
}

func TestMultiKeyAffinityOverloadFallbackKeepsCachedBinding(t *testing.T) {
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	channel := &Channel{
		Id:  900003,
		Key: "upstream-a\nupstream-b\nupstream-c",
		ChannelInfo: ChannelInfo{
			IsMultiKey:                 true,
			MultiKeyMode:               constant.MultiKeyModeAffinity,
			MultiKeyAffinityTTLSeconds: 60,
			MultiKeyStatusList:         map[int]int{},
		},
	}

	_, cachedIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-c")
	require.Nil(t, err)
	_, fallbackIndex, newAPIError := channel.GetNextEnabledKeyWithSelection(
		"sk-client-c", map[int]struct{}{cachedIndex: {}}, false,
	)
	require.Nil(t, newAPIError)
	assert.NotEqual(t, cachedIndex, fallbackIndex)

	_, restoredIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-c")
	require.Nil(t, err)
	assert.Equal(t, cachedIndex, restoredIndex)
}

func TestMultiKeyAffinityNormalRetryUpdatesCachedBinding(t *testing.T) {
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	channel := &Channel{
		Id:  900004,
		Key: "upstream-a\nupstream-b\nupstream-c",
		ChannelInfo: ChannelInfo{
			IsMultiKey:                 true,
			MultiKeyMode:               constant.MultiKeyModeAffinity,
			MultiKeyAffinityTTLSeconds: 60,
			MultiKeyStatusList:         map[int]int{},
		},
	}

	_, cachedIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-d")
	require.Nil(t, err)
	_, retryIndex, newAPIError := channel.GetNextEnabledKeyWithSelection(
		"sk-client-d", map[int]struct{}{cachedIndex: {}}, true,
	)
	require.Nil(t, newAPIError)
	assert.NotEqual(t, cachedIndex, retryIndex)

	_, restoredIndex, err := channel.GetNextEnabledKeyWithAffinity("sk-client-d")
	require.Nil(t, err)
	assert.Equal(t, retryIndex, restoredIndex)
}
