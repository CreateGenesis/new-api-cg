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
