package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupContextForSelectedChannelExcludesTriedKeysUntilNextRound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	channel := &model.Channel{
		Id:      950001,
		Type:    constant.ChannelTypeOpenAI,
		Name:    "multi-key",
		Key:     "key-a\nkey-b\nkey-c",
		AutoBan: common.GetPointer(1),
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}

	firstRound := map[int]bool{}
	for range 3 {
		require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "gpt-4o"))
		index := common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)
		assert.False(t, firstRound[index])
		firstRound[index] = true
	}
	assert.Len(t, firstRound, 3)

	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "gpt-4o"))
	nextRoundIndex := common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex)
	assert.Contains(t, []int{0, 1, 2}, nextRoundIndex)

	tried := getTriedMultiKeyIndexes(ctx, channel.Id)
	assert.Len(t, tried, 1)
	_, exists := tried[nextRoundIndex]
	assert.True(t, exists)
}

func TestSetupContextForChannelTestKeyUsesExactIndexWithoutMutatingSelection(t *testing.T) {
	testCases := []struct {
		name     string
		keyIndex int
		status   int
		wantKey  string
	}{
		{name: "first key", keyIndex: 0, status: common.ChannelStatusEnabled, wantKey: "key-a"},
		{name: "manually disabled key", keyIndex: 1, status: common.ChannelStatusManuallyDisabled, wantKey: "key-b"},
		{name: "automatically disabled key", keyIndex: 2, status: common.ChannelStatusAutoDisabled, wantKey: "key-c"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			statusList := map[int]int{testCase.keyIndex: testCase.status}
			channel := &model.Channel{
				Id:   950010 + testCase.keyIndex,
				Type: constant.ChannelTypeOpenAI,
				Name: "multi-key-test",
				Key:  "key-a\nkey-b\nkey-c",
				ChannelInfo: model.ChannelInfo{
					IsMultiKey:           true,
					MultiKeyMode:         constant.MultiKeyModePolling,
					MultiKeyPollingIndex: 2,
					MultiKeyStatusList:   statusList,
				},
			}

			require.Nil(t, SetupContextForChannelTestKey(ctx, channel, "gpt-4o", testCase.keyIndex))

			assert.Equal(t, testCase.wantKey, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
			assert.Equal(t, testCase.keyIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
			assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
			assert.Equal(t, 2, channel.ChannelInfo.MultiKeyPollingIndex)
			assert.Equal(t, map[int]int{testCase.keyIndex: testCase.status}, channel.ChannelInfo.MultiKeyStatusList)
			assert.Empty(t, getTriedMultiKeyIndexes(ctx, channel.Id))
		})
	}
}

func TestSetupContextForChannelTestKeyDoesNotChangeAffinityBinding(t *testing.T) {
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	channel := &model.Channel{
		Id:   950020,
		Type: constant.ChannelTypeOpenAI,
		Name: "affinity-test",
		Key:  "key-a\nkey-b\nkey-c",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:                 true,
			MultiKeyMode:               constant.MultiKeyModeAffinity,
			MultiKeyAffinityTTLSeconds: 60,
		},
	}
	const affinityValue = "channel-test-affinity"
	_, boundIndex, newAPIError := channel.GetNextEnabledKeyWithAffinity(affinityValue)
	require.Nil(t, newAPIError)
	fixedIndex := (boundIndex + 1) % 3

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.Nil(t, SetupContextForChannelTestKey(ctx, channel, "gpt-4o", fixedIndex))

	_, restoredIndex, newAPIError := channel.GetNextEnabledKeyWithAffinity(affinityValue)
	require.Nil(t, newAPIError)
	assert.Equal(t, boundIndex, restoredIndex)
}

func TestSetupContextForSelectedChannelStillUsesEnabledKeySelection(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	channel := &model.Channel{
		Id:   950030,
		Type: constant.ChannelTypeOpenAI,
		Name: "ordinary-test",
		Key:  "key-a\nkey-b\nkey-c",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusManuallyDisabled,
				2: common.ChannelStatusAutoDisabled,
			},
		},
	}

	require.Nil(t, SetupContextForSelectedChannel(ctx, channel, "gpt-4o"))

	assert.Equal(t, "key-b", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	assert.Len(t, getTriedMultiKeyIndexes(ctx, channel.Id), 1)
}
