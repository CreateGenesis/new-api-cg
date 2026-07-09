package service

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractSimulatedModelCachePromptText(t *testing.T) {
	tests := []struct {
		name   string
		format types.RelayFormat
		body   string
		want   string
	}{
		{
			name:   "openai chat messages",
			format: types.RelayFormatOpenAI,
			body: `{
				"model":"gpt-test",
				"messages":[
					{"role":"system","content":"Be terse"},
					{"role":"user","content":[
						{"type":"text","text":"Explain cache"},
						{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
					]}
				]
			}`,
			want: "Be terse\nExplain cache",
		},
		{
			name:   "openai responses input and instructions",
			format: types.RelayFormatOpenAIResponses,
			body: `{
				"model":"gpt-test",
				"instructions":"Follow policy",
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"Summarize this"}]}
				]
			}`,
			want: "Follow policy\nSummarize this",
		},
		{
			name:   "claude system and messages",
			format: types.RelayFormatClaude,
			body: `{
				"model":"claude-test",
				"system":[{"type":"text","text":"Be safe"}],
				"messages":[{"role":"user","content":[{"type":"text","text":"Draft reply"}]}]
			}`,
			want: "Be safe\nDraft reply",
		},
		{
			name:   "gemini system instruction and contents",
			format: types.RelayFormatGemini,
			body: `{
				"contents":[{"role":"user","parts":[{"text":"Describe image"}]}],
				"systemInstruction":{"parts":[{"text":"Use bullet points"}]}
			}`,
			want: "Use bullet points\nDescribe image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSimulatedModelCachePromptText(tt.format, []byte(tt.body))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSimulatedModelCacheMatchRatioUsesCurrentPromptLength(t *testing.T) {
	assert.Equal(t, 0.6, SimulatedModelCacheMatchRatio("abc123xyz", "00123"))
	assert.Equal(t, 1.0, SimulatedModelCacheMatchRatio("same", "same"))
	assert.Equal(t, 0.0, SimulatedModelCacheMatchRatio("anything", ""))
}

func TestSimulatedModelCachePartialMatchRatioHandlesLargePrompts(t *testing.T) {
	ratio, ok := simulatedModelCachePartialMatchRatio("abc123xyz", "00123")
	require.True(t, ok)
	assert.Equal(t, 0.6, ratio)

	commonPrefix := strings.Repeat("中", 50000)
	cachedPrompt := "old-" + commonPrefix + "-cached"
	currentPrompt := commonPrefix + "-current"

	ratio, ok = simulatedModelCachePartialMatchRatio(cachedPrompt, currentPrompt)
	require.True(t, ok)
	assert.Greater(t, ratio, 0.99)

	oversizedPrompt := strings.Repeat("a", simulatedModelCacheMaxPartialMatchRunes+1)
	ratio, ok = simulatedModelCachePartialMatchRatio(oversizedPrompt, "abc")
	assert.False(t, ok)
	assert.Equal(t, 0.0, ratio)
}

func TestApplySimulatedModelCacheUsageRewritePreservesPromptTotal(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 7,
		TotalTokens:      107,
		InputTokens:      100,
		OutputTokens:     7,
	}

	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:        "partial_rewrite",
		MatchRatio:  0.25,
		ReplayCount: 0,
	})

	require.NotNil(t, marker)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Equal(t, 25, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.TotalTokens-usage.CompletionTokens)
	assert.Equal(t, 100, marker.OriginalPromptTokens)
	assert.Equal(t, 75, marker.SimulatedPromptTokens)
	assert.Equal(t, 25, marker.SimulatedCachedTokens)
}

func TestApplySimulatedModelCacheUsageRewriteMarksExactReplayAsFullyCached(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     40,
		CompletionTokens: 9,
		TotalTokens:      49,
	}

	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:        "exact_replay",
		MatchRatio:  1,
		ReplayCount: 2,
	})

	require.NotNil(t, marker)
	assert.Equal(t, 40, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 0, marker.SimulatedPromptTokens)
	assert.Equal(t, 40, marker.SimulatedCachedTokens)
	assert.Equal(t, 2, marker.ReplayCount)
}

func TestApplySimulatedModelCacheUsageRewriteUsesAnthropicUsageSemantics(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 7,
		TotalTokens:      107,
		UsageSemantic:    "anthropic",
	}

	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_rewrite",
		MatchRatio: 0.25,
	})

	require.NotNil(t, marker)
	assert.Equal(t, 75, usage.PromptTokens)
	assert.Equal(t, 25, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 82, usage.TotalTokens)
	assert.Equal(t, 100, marker.OriginalPromptTokens)
	assert.Equal(t, 75, marker.SimulatedPromptTokens)
	assert.Equal(t, 25, marker.SimulatedCachedTokens)
}

func TestAppendSimulatedModelCacheResponseKeepsMostRecentTwo(t *testing.T) {
	entry := simulatedModelCacheExactEntry{}

	entry.appendResponse(simulatedModelCacheResponse{Body: []byte("first")})
	entry.appendResponse(simulatedModelCacheResponse{Body: []byte("second")})
	entry.appendResponse(simulatedModelCacheResponse{Body: []byte("third")})

	require.Len(t, entry.Responses, 2)
	assert.Equal(t, []byte("second"), entry.Responses[0].Body)
	assert.Equal(t, []byte("third"), entry.Responses[1].Body)
}

func TestPickSimulatedModelCacheResponseUsesProvidedRand(t *testing.T) {
	entry := simulatedModelCacheExactEntry{
		Responses: []simulatedModelCacheResponse{
			{Body: []byte("a")},
			{Body: []byte("b")},
		},
	}
	rng := rand.New(rand.NewSource(1))

	first, ok := entry.pickResponse(rng)
	require.True(t, ok)
	second, ok := entry.pickResponse(rng)
	require.True(t, ok)

	assert.Equal(t, []byte("b"), first.Body)
	assert.Equal(t, []byte("b"), second.Body)
}

func TestSimulatedModelCacheStoreResetsReplayCount(t *testing.T) {
	entry := simulatedModelCacheExactEntry{
		ReplayCount: 3,
	}

	entry.storeFreshResponse(simulatedModelCacheResponse{Body: []byte("fresh")})

	assert.Equal(t, 0, entry.ReplayCount)
	require.Len(t, entry.Responses, 1)
	assert.Equal(t, []byte("fresh"), entry.Responses[0].Body)
}

func TestGetSimulatedModelCacheReplaySkipsWhenRedisDisabled(t *testing.T) {
	oldRedisEnabled := common.RedisEnabled
	oldRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRDB
	})

	replay, err := GetSimulatedModelCacheReplay(context.Background(), SimulatedModelCacheLookupRequest{
		ChannelID:          1,
		UpstreamModel:      "gpt-test",
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        []byte(`{"model":"gpt-test","messages":[]}`),
		ReuseLimit:         3,
	})

	require.NoError(t, err)
	assert.False(t, replay.Found)
}

func TestSimulatedModelCacheResponseBodyStoredCompressedOnDisk(t *testing.T) {
	body := []byte(strings.Repeat("cached response body ", 20))
	restore := useSimulatedModelCacheTestDiskPath(t)
	defer restore()

	response := SimulatedModelCacheResponse{Body: append([]byte(nil), body...)}

	require.NoError(t, storeSimulatedModelCacheResponseDiskBody(&response))
	assert.Empty(t, response.Body)
	require.NotNil(t, response.BodyStorage)
	assert.Equal(t, "gzip", response.BodyStorage.Compression)

	raw, err := common.Marshal(response)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), string(body))

	loaded := response
	require.NoError(t, loadSimulatedModelCacheResponseBody(&loaded))
	assert.Equal(t, body, loaded.Body)
	deleteSimulatedModelCacheResponseDiskBody(response)
}

func TestSimulatedModelCacheStoreEvictsOldestAfterOneHundredEntriesPerUserChannelModel(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const userID = 4242
	const channelID = 998877
	const otherChannelID = 998878
	const upstreamModel = "gpt-test"
	const otherModel = "other-test"

	var bodies [][]byte
	for i := 0; i < 100; i++ {
		body := []byte(fmt.Sprintf(`{"model":"gpt-test","messages":[{"role":"user","content":"prompt %02d"}]}`, i))
		bodies = append(bodies, body)
		err := StoreSimulatedModelCacheResponse(ctx, SimulatedModelCacheStoreRequest{
			UserID:             userID,
			ChannelID:          channelID,
			UpstreamModel:      upstreamModel,
			FinalRequestFormat: types.RelayFormatOpenAI,
			RequestBody:        body,
			PromptText:         fmt.Sprintf("prompt %02d", i),
			Response: SimulatedModelCacheResponse{
				StatusCode: 200,
				Body:       []byte(fmt.Sprintf(`{"id":"response-%02d"}`, i)),
				Usage: dto.Usage{
					PromptTokens:     10,
					CompletionTokens: 1,
					TotalTokens:      11,
				},
			},
			TTLSeconds: 86400,
		})
		require.NoError(t, err)
	}

	otherChannelBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"other channel"}]}`)
	require.NoError(t, StoreSimulatedModelCacheResponse(ctx, SimulatedModelCacheStoreRequest{
		UserID:             userID,
		ChannelID:          otherChannelID,
		UpstreamModel:      upstreamModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        otherChannelBody,
		PromptText:         "other channel",
		Response:           SimulatedModelCacheResponse{StatusCode: 200, Body: []byte(`{"id":"other-channel"}`)},
		TTLSeconds:         86400,
	}))
	otherModelBody := []byte(`{"model":"other-test","messages":[{"role":"user","content":"other model"}]}`)
	require.NoError(t, StoreSimulatedModelCacheResponse(ctx, SimulatedModelCacheStoreRequest{
		UserID:             userID,
		ChannelID:          channelID,
		UpstreamModel:      otherModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        otherModelBody,
		PromptText:         "other model",
		Response:           SimulatedModelCacheResponse{StatusCode: 200, Body: []byte(`{"id":"other-model"}`)},
		TTLSeconds:         86400,
	}))

	firstBeforeOverflow, err := GetSimulatedModelCacheReplay(ctx, SimulatedModelCacheLookupRequest{
		UserID:             userID,
		ChannelID:          channelID,
		UpstreamModel:      upstreamModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        bodies[0],
		ReuseLimit:         3,
		TTLSeconds:         86400,
	})
	require.NoError(t, err)
	require.True(t, firstBeforeOverflow.Found)

	overflowBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"prompt 100"}]}`)
	require.NoError(t, StoreSimulatedModelCacheResponse(ctx, SimulatedModelCacheStoreRequest{
		UserID:             userID,
		ChannelID:          channelID,
		UpstreamModel:      upstreamModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        overflowBody,
		PromptText:         "prompt 100",
		Response:           SimulatedModelCacheResponse{StatusCode: 200, Body: []byte(`{"id":"response-100"}`)},
		TTLSeconds:         86400,
	}))

	firstAfterOverflow, err := GetSimulatedModelCacheReplay(ctx, SimulatedModelCacheLookupRequest{
		UserID:             userID,
		ChannelID:          channelID,
		UpstreamModel:      upstreamModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        bodies[0],
		ReuseLimit:         3,
		TTLSeconds:         86400,
	})
	require.NoError(t, err)
	assert.False(t, firstAfterOverflow.Found)

	overflowReplay, err := GetSimulatedModelCacheReplay(ctx, SimulatedModelCacheLookupRequest{
		UserID:             userID,
		ChannelID:          channelID,
		UpstreamModel:      upstreamModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        overflowBody,
		ReuseLimit:         3,
		TTLSeconds:         86400,
	})
	require.NoError(t, err)
	require.True(t, overflowReplay.Found)
	assert.Equal(t, []byte(`{"id":"response-100"}`), overflowReplay.Response.Body)

	otherChannelReplay, err := GetSimulatedModelCacheReplay(ctx, SimulatedModelCacheLookupRequest{
		UserID:             userID,
		ChannelID:          otherChannelID,
		UpstreamModel:      upstreamModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        otherChannelBody,
		ReuseLimit:         3,
		TTLSeconds:         86400,
	})
	require.NoError(t, err)
	require.True(t, otherChannelReplay.Found)
	assert.Equal(t, []byte(`{"id":"other-channel"}`), otherChannelReplay.Response.Body)

	otherModelReplay, err := GetSimulatedModelCacheReplay(ctx, SimulatedModelCacheLookupRequest{
		UserID:             userID,
		ChannelID:          channelID,
		UpstreamModel:      otherModel,
		FinalRequestFormat: types.RelayFormatOpenAI,
		RequestBody:        otherModelBody,
		ReuseLimit:         3,
		TTLSeconds:         86400,
	})
	require.NoError(t, err)
	require.True(t, otherModelReplay.Found)
	assert.Equal(t, []byte(`{"id":"other-model"}`), otherModelReplay.Response.Body)
}

func TestSimulatedModelCacheExactKeyNormalizesJSONRequestBody(t *testing.T) {
	base := SimulatedModelCacheLookupRequest{
		ChannelID:          1,
		UpstreamModel:      "gpt-test",
		FinalRequestFormat: types.RelayFormatOpenAI,
	}
	first := base
	first.RequestBody = []byte(`{"b":2,"a":1}`)
	second := base
	second.RequestBody = []byte("{\n  \"a\": 1,\n  \"b\": 2\n}")

	assert.Equal(t, simulatedModelCacheExactKey(first), simulatedModelCacheExactKey(second))
}

func withSimulatedModelCacheTestRedis(t *testing.T) context.Context {
	t.Helper()

	redisURL := os.Getenv("NEW_API_TEST_REDIS_CONN_STRING")
	if redisURL == "" {
		t.Skip("NEW_API_TEST_REDIS_CONN_STRING is not set")
	}
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	oldRedisEnabled := common.RedisEnabled
	oldRDB := common.RDB
	client := redis.NewClient(opt)
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	require.NoError(t, client.FlushDB(ctx).Err())

	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		_ = client.FlushDB(ctx).Err()
		_ = client.Close()
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRDB
	})
	return ctx
}

func useSimulatedModelCacheTestDiskPath(t *testing.T) func() {
	t.Helper()
	oldConfig := common.GetDiskCacheConfig()
	common.SetDiskCacheConfig(common.DiskCacheConfig{
		Enabled:     oldConfig.Enabled,
		ThresholdMB: oldConfig.ThresholdMB,
		MaxSizeMB:   oldConfig.MaxSizeMB,
		Path:        t.TempDir(),
	})
	return func() {
		common.SetDiskCacheConfig(oldConfig)
	}
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesOpenAIUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		TotalTokens:      108,
	}
	ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_rewrite",
		MatchRatio: 0.25,
	})

	body := []byte(`{"id":"chatcmpl_test","usage":{"prompt_tokens":100,"completion_tokens":8,"total_tokens":108}}`)
	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatOpenAI, "application/json", body, usage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(patched, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usageMap["prompt_tokens"])
	assert.Equal(t, float64(108), usageMap["total_tokens"])
	details, ok := usageMap["prompt_tokens_details"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(25), details["cached_tokens"])
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesClaudeUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		TotalTokens:      108,
		UsageSemantic:    "anthropic",
	}
	ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_rewrite",
		MatchRatio: 0.25,
	})

	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":8}}`)
	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatClaude, "application/json", body, usage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(patched, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(75), usageMap["input_tokens"])
	assert.Equal(t, float64(25), usageMap["cache_read_input_tokens"])
	assert.Equal(t, float64(8), usageMap["output_tokens"])
}
