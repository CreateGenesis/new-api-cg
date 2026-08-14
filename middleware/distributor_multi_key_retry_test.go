package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimulatedModelCacheRequestFormat(t *testing.T) {
	tests := map[string]types.RelayFormat{
		"/v1/chat/completions":                  types.RelayFormatOpenAI,
		"/v1/responses":                         types.RelayFormatOpenAIResponses,
		"/v1/responses/compact":                 types.RelayFormatOpenAIResponsesCompaction,
		"/v1/messages":                          types.RelayFormatClaude,
		"/v1beta/models/gemini:generateContent": types.RelayFormatGemini,
	}
	for path, expected := range tests {
		assert.Equal(t, expected, simulatedModelCacheRequestFormat(path))
	}
	assert.Empty(t, simulatedModelCacheRequestFormat("/v1/images/generations"))
}

func TestSetupContextForSelectedChannelClearsPreviousOptionalChannelState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name         string
		firstType    int
		firstOther   string
		organization string
		contextKey   string
	}{
		{name: "azure api version", firstType: constant.ChannelTypeAzure, firstOther: "2025-01-01", contextKey: "api_version"},
		{name: "vertex region", firstType: constant.ChannelTypeVertexAi, firstOther: "us-central1", contextKey: "region"},
		{name: "ali plugin", firstType: constant.ChannelTypeAli, firstOther: "plugin-a", contextKey: "plugin"},
		{name: "coze bot", firstType: constant.ChannelTypeCoze, firstOther: "bot-a", contextKey: "bot_id"},
		{name: "organization", firstType: constant.ChannelTypeOpenAI, organization: "org-a", contextKey: "channel_organization"},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			first := &model.Channel{Id: 960000 + index, Type: test.firstType, Key: "first-key", Other: test.firstOther}
			if test.organization != "" {
				first.OpenAIOrganization = &test.organization
			}
			require.Nil(t, SetupContextForSelectedChannel(ctx, first, "gpt-test"))
			require.NotEmpty(t, ctx.GetString(test.contextKey))

			second := &model.Channel{Id: 970000 + index, Type: constant.ChannelTypeOpenAI, Key: "second-key"}
			require.Nil(t, SetupContextForSelectedChannel(ctx, second, "gpt-test"))

			require.Empty(t, ctx.GetString("api_version"))
			require.Empty(t, ctx.GetString("region"))
			require.Empty(t, ctx.GetString("plugin"))
			require.Empty(t, ctx.GetString("bot_id"))
			require.Empty(t, common.GetContextKeyString(ctx, constant.ContextKeyChannelOrganization))
		})
	}
}

func TestSetupContextCacheAwareStrategyPrefersBestMatchingKeyWhenFieldsHidden(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	originalMinimumInputTokens := common.GetSimulatedModelCacheMinInputTokens()
	common.RedisEnabled = true
	common.RDB = client
	common.SetSimulatedModelCacheMinInputTokens(0)
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
		common.SetSimulatedModelCacheMinInputTokens(originalMinimumInputTokens)
	})

	const channelID = 950100
	const userID = 42
	const modelName = "gpt-test"
	prompt := "the shared prompt that should prefer key b"
	keyA := "key-a"
	keyB := "key-b"
	require.NoError(t, service.StoreSimulatedModelCachePromptFingerprint(context.Background(), service.SimulatedModelCachePartialMatchRequest{
		ChannelID:  channelID,
		UserID:     userID,
		Model:      modelName,
		PromptText: prompt,
		TTLSeconds: 60,
		KeyDigest:  model.MultiKeyKeyDigest(keyB),
	}))
	threshold := 35
	channel := &model.Channel{
		Id:            channelID,
		Key:           keyA + "\n" + keyB,
		OtherSettings: `{"simulated_model_cache":{"enabled":false,"ttl_seconds":60,"min_match_ratio":0.5}}`,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:                            true,
			MultiKeyMode:                          constant.MultiKeyModeCacheAffinityLeastRequests,
			MultiKeyLeastRequestsWindowSeconds:    60,
			MultiKeyCacheAffinityThresholdPercent: &threshold,
		},
	}
	body := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"the shared prompt that should prefer key b"}]}`)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyUserId, userID)

	newAPIError := SetupContextForSelectedChannel(ctx, channel, modelName)

	require.Nil(t, newAPIError)
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	assert.Equal(t, keyB, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
}

func TestSetupContextCacheAwareStrategyRoutesMediaOnlyPrompt(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	originalMinimumInputTokens := common.GetSimulatedModelCacheMinInputTokens()
	common.RedisEnabled = true
	common.RDB = client
	common.SetSimulatedModelCacheMinInputTokens(500)
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
		common.SetSimulatedModelCacheMinInputTokens(originalMinimumInputTokens)
	})

	imageRate := 520.0
	videoRate := 820.0
	audioRate := 25.0
	fileRate := 4096.0
	imageFallback := 1000
	videoFallback := 8192
	audioFallback := 256
	fileFallback := 4096
	settings := dto.SimulatedModelCacheSettings{
		Multimodal: &dto.SimulatedModelCacheMultimodalSettings{
			Enabled:                       true,
			ImageTokensPerMegapixel:       &imageRate,
			VideoTokensPerSecondMegapixel: &videoRate,
			AudioTokensPerSecond:          &audioRate,
			FileTokensPerMiB:              &fileRate,
			ImageFallbackTokens:           &imageFallback,
			VideoFallbackTokens:           &videoFallback,
			AudioFallbackTokens:           &audioFallback,
			FileFallbackTokens:            &fileFallback,
		},
	}
	const channelID = 950102
	const userID = 43
	const modelName = "gpt-test"
	const keyA = "key-a"
	const keyB = "key-b"
	body := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`)
	prompt := service.ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, modelName, body, settings)
	require.NoError(t, service.StoreSimulatedModelCachePromptFingerprint(context.Background(), service.SimulatedModelCachePartialMatchRequest{
		ChannelID: channelID, UserID: userID, Model: modelName, Prompt: prompt, TTLSeconds: 60, KeyDigest: model.MultiKeyKeyDigest(keyB),
	}))
	threshold := 35
	channel := &model.Channel{
		Id:            channelID,
		Key:           keyA + "\n" + keyB,
		OtherSettings: `{"simulated_model_cache":{"enabled":false,"ttl_seconds":60,"min_match_ratio":0.5,"multimodal":{"enabled":true,"image_tokens_per_megapixel":520,"video_tokens_per_second_megapixel":820,"audio_tokens_per_second":25,"file_tokens_per_mib":4096,"image_fallback_tokens":1000,"video_fallback_tokens":8192,"audio_fallback_tokens":256,"file_fallback_tokens":4096}}}`,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:                            true,
			MultiKeyMode:                          constant.MultiKeyModeCacheAffinityLeastRequests,
			MultiKeyLeastRequestsWindowSeconds:    60,
			MultiKeyCacheAffinityThresholdPercent: &threshold,
		},
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyUserId, userID)

	newAPIError := SetupContextForSelectedChannel(ctx, channel, modelName)

	require.Nil(t, newAPIError)
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	assert.Equal(t, keyB, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
}

func TestPrepareSimulatedModelCacheRoutingRespectsMinimumInputTokens(t *testing.T) {
	originalMinimumInputTokens := common.GetSimulatedModelCacheMinInputTokens()
	common.SetSimulatedModelCacheMinInputTokens(1000000)
	t.Cleanup(func() { common.SetSimulatedModelCacheMinInputTokens(originalMinimumInputTokens) })
	channel := &model.Channel{
		Id:          950101,
		Key:         "key-a\nkey-b",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeCacheAffinityLeastRequests},
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"short"}]}`)))
	ctx.Request.Header.Set("Content-Type", "application/json")

	preparation := prepareSimulatedModelCacheRouting(ctx, channel, "gpt-test", nil)

	assert.Nil(t, preparation)
}

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
