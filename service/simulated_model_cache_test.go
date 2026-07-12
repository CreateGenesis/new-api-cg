package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"

	"github.com/alicebob/miniredis/v2"
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
	assert.Equal(t, 1.0, SimulatedModelCacheMatchRatio("same", "same"))
	assert.Equal(t, 0.0, SimulatedModelCacheMatchRatio("anything", "different"))
	assert.Equal(t, 0.0, SimulatedModelCacheMatchRatio("anything", ""))
}

func TestSimulatedModelCacheMatchRatioMatchesShortPromptTrigrams(t *testing.T) {
	assert.InDelta(t, 6.0/7.0, SimulatedModelCacheMatchRatio("hello AA", "hello B"), 0.000001)
	assert.InDelta(t, 6.0/8.0, SimulatedModelCacheMatchRatio("hello B", "hello AA"), 0.000001)
	assert.InDelta(t, 4.0/5.0, SimulatedModelCacheMatchRatio("你好世界甲", "你好世界乙"), 0.000001)
	assert.InDelta(t, 6.0/9.0, SimulatedModelCacheMatchRatio("QabcabcR", "abcabcXYZ"), 0.000001)
}

func TestSimulatedModelCacheMatchRatioKeepsVeryShortPromptsExactOnly(t *testing.T) {
	assert.Equal(t, 1.0, SimulatedModelCacheMatchRatio("你好", "你好"))
	assert.Equal(t, 0.0, SimulatedModelCacheMatchRatio("你们", "你好"))
}

func TestSimulatedModelCacheFingerprintMatcherUsesCurrentPromptRunes(t *testing.T) {
	current := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 256,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 3, RuneLength: 64},
			{HashHigh: 4, RuneLength: 64},
		},
	}
	candidate := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 192,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 9, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 3, RuneLength: 64},
		},
	}

	ratio := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)

	assert.Equal(t, 0.5, ratio)
}

func TestBuildSimulatedModelCachePromptFingerprintHandlesUnicodeAndBoundaries(t *testing.T) {
	prompt := strings.Repeat("你好🌍", 400)
	fingerprint, err := buildSimulatedModelCachePromptFingerprint(context.Background(), prompt)

	require.NoError(t, err)
	assert.Equal(t, 1200, fingerprint.TotalRunes)
	require.NotEmpty(t, fingerprint.Chunks)
	for index, chunk := range fingerprint.Chunks {
		if index < len(fingerprint.Chunks)-1 {
			assert.GreaterOrEqual(t, int(chunk.RuneLength), simulatedModelCacheFingerprintMinRunes)
		}
		assert.LessOrEqual(t, int(chunk.RuneLength), simulatedModelCacheFingerprintMaxRunes)
	}
	assert.Equal(t, 1.0, SimulatedModelCacheMatchRatio(prompt, prompt))
}

func TestBuildSimulatedModelCachePromptFingerprintStoresFineHashesOnlyThroughLimit(t *testing.T) {
	shortPrompt := strings.Repeat("你", simulatedModelCacheFineFingerprintMaxRunes)
	shortFingerprint, err := buildSimulatedModelCachePromptFingerprint(context.Background(), shortPrompt)
	require.NoError(t, err)
	assert.Len(
		t,
		shortFingerprint.FineHashes,
		(simulatedModelCacheFineFingerprintMaxRunes-simulatedModelCacheFineFingerprintWindowRunes+1)*simulatedModelCacheFineFingerprintHashBytes,
	)
	assert.True(t, shortFingerprint.hasValidFineHashes())
	raw, err := common.Marshal(shortFingerprint)
	require.NoError(t, err)
	assert.Less(t, len(raw), simulatedModelCacheMaxFingerprintEncodedBytes)
	assert.NotContains(t, string(raw), shortPrompt)

	longPrompt := strings.Repeat("你", simulatedModelCacheFineFingerprintMaxRunes+1)
	longFingerprint, err := buildSimulatedModelCachePromptFingerprint(context.Background(), longPrompt)
	require.NoError(t, err)
	assert.Empty(t, longFingerprint.FineHashes)
	assert.True(t, longFingerprint.hasValidFineHashes())
}

func TestSimulatedModelCacheFingerprintFallsBackToCoarseAcrossFineLimit(t *testing.T) {
	current, err := buildSimulatedModelCachePromptFingerprint(
		context.Background(),
		strings.Repeat("abcd", simulatedModelCacheFineFingerprintMaxRunes/4),
	)
	require.NoError(t, err)
	candidate, err := buildSimulatedModelCachePromptFingerprint(
		context.Background(),
		"Z"+strings.Repeat("abcd", simulatedModelCacheFineFingerprintMaxRunes/4),
	)
	require.NoError(t, err)
	require.NotEmpty(t, current.FineHashes)
	require.Empty(t, candidate.FineHashes)

	got := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)
	current.FineHashes = nil
	want := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)

	assert.Equal(t, want, got)
}

func TestSimulatedModelCacheFingerprintKeepsMatchesAroundLocalizedChanges(t *testing.T) {
	base := strings.Repeat("abcdefghij", 600)
	changed := base[:2500] + strings.Repeat("Z", 80) + base[2580:]

	ratio := SimulatedModelCacheMatchRatio(base, changed)

	assert.Greater(t, ratio, 0.5)
	assert.Less(t, ratio, 1.0)
}

func TestSimulatedModelCacheFingerprintResynchronizesAfterUnicodeInsertions(t *testing.T) {
	current := strings.Repeat("你好世界🌍", 400)
	cached := strings.Repeat("前", 100) + current + strings.Repeat("后", 100)

	ratio := SimulatedModelCacheMatchRatio(cached, current)

	assert.Greater(t, ratio, 0.7)
}

func TestSimulatedModelCacheFingerprintFindsLongestRepeatedBlockSequence(t *testing.T) {
	current := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 384,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 3, RuneLength: 64},
			{HashHigh: 4, RuneLength: 64},
		},
	}
	candidate := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 384,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 9, RuneLength: 64},
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 8, RuneLength: 64},
		},
	}

	ratio := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)

	assert.InDelta(t, 2.0/3.0, ratio, 0.000001)
}

func TestBuildSimulatedModelCachePromptFingerprintRejectsOversizedPrompt(t *testing.T) {
	_, err := buildSimulatedModelCachePromptFingerprint(context.Background(), strings.Repeat("a", simulatedModelCacheMaxFingerprintRunes+1))

	assert.ErrorIs(t, err, errSimulatedModelCachePromptTooLarge)
}

func TestBuildSimulatedModelCachePromptFingerprintStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := buildSimulatedModelCachePromptFingerprint(ctx, strings.Repeat("a", 1024))

	assert.ErrorIs(t, err, context.Canceled)
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
		Mode:       "partial_fingerprint",
		MatchRatio: 0.25,
	})

	require.NotNil(t, marker)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Equal(t, 25, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.TotalTokens-usage.CompletionTokens)
	assert.Equal(t, 100, marker.OriginalPromptTokens)
	assert.Equal(t, 75, marker.SimulatedPromptTokens)
	assert.Equal(t, 25, marker.SimulatedCachedTokens)
}

func TestApplySimulatedModelCacheUsageRewriteUsesAnthropicUsageSemantics(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 7,
		TotalTokens:      107,
		UsageSemantic:    "anthropic",
	}

	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_fingerprint",
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

func TestSimulatedModelCacheFingerprintIndexKeepsRecentUniquePromptsPerUserAndModel(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	originalMaxEntries := common.GetSimulatedModelCacheEntriesPerScope()
	common.SetSimulatedModelCacheEntriesPerScope(3)
	t.Cleanup(func() {
		common.SetSimulatedModelCacheEntriesPerScope(originalMaxEntries)
	})
	const userID = 4242
	const otherUserID = 4243
	const model = "gpt-test"
	const otherModel = "other-test"

	for i := 0; i < 4; i++ {
		prompt := fmt.Sprintf("prompt %03d %s", i, strings.Repeat("content ", 20))
		err := StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
			UserID:     userID,
			Model:      model,
			PromptText: prompt,
			TTLSeconds: 86400,
		})
		require.NoError(t, err)
	}

	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     otherUserID,
		Model:      model,
		PromptText: strings.Repeat("other user ", 20),
		TTLSeconds: 86400,
	}))
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      otherModel,
		PromptText: strings.Repeat("other model ", 20),
		TTLSeconds: 86400,
	}))

	indexKey := simulatedModelCacheScopeIndexKey(userID, model)
	promptIDs, err := common.RDB.ZRange(ctx, indexKey, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, promptIDs, 3)
	assert.NotContains(t, promptIDs, sha256Hex([]byte(fmt.Sprintf("prompt %03d %s", 0, strings.Repeat("content ", 20)))))
	assert.Contains(t, promptIDs, sha256Hex([]byte(fmt.Sprintf("prompt %03d %s", 3, strings.Repeat("content ", 20)))))

	otherUserCount, err := common.RDB.ZCard(ctx, simulatedModelCacheScopeIndexKey(otherUserID, model)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), otherUserCount)
	otherModelCount, err := common.RDB.ZCard(ctx, simulatedModelCacheScopeIndexKey(userID, otherModel)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), otherModelCount)
}

func TestSimulatedModelCacheV3StoresOneFingerprintPerPromptAndKeepsOlderVersionsUntouched(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const prompt = "shared prompt "
	promptText := strings.Repeat(prompt, 20)
	v1Key := "simulated_model_cache:v1:legacy"
	v2Key := "simulated_model_cache:v2:legacy"
	require.NoError(t, common.RDB.Set(ctx, v1Key, "legacy", 0).Err())
	require.NoError(t, common.RDB.Set(ctx, v2Key, "legacy", 0).Err())

	for i := 0; i < 2; i++ {
		require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
			UserID:     1,
			Model:      "gpt-test",
			PromptText: promptText,
			TTLSeconds: 60,
		}))
	}

	indexKey := simulatedModelCacheScopeIndexKey(1, "gpt-test")
	indexCount, err := common.RDB.ZCard(ctx, indexKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), indexCount)
	fingerprintKey := simulatedModelCacheFingerprintKey(sha256Hex([]byte(promptText)))
	raw, err := common.RDB.Get(ctx, fingerprintKey).Result()
	require.NoError(t, err)
	assert.NotContains(t, raw, promptText)
	legacy, err := common.RDB.Get(ctx, v1Key).Result()
	require.NoError(t, err)
	assert.Equal(t, "legacy", legacy)
	legacy, err = common.RDB.Get(ctx, v2Key).Result()
	require.NoError(t, err)
	assert.Equal(t, "legacy", legacy)
}

func TestSimulatedModelCachePartialFingerprintMatchesShortPrompts(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "short-forward",
		PromptText: "hello AA",
		TTLSeconds: 60,
	}))

	forward, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:        10,
		Model:         "short-forward",
		PromptText:    "hello B",
		MinMatchRatio: 0.8,
	})
	require.NoError(t, err)
	assert.True(t, forward.Found)
	assert.InDelta(t, 6.0/7.0, forward.MatchRatio, 0.000001)
	assert.Equal(t, SimulatedModelCacheFingerprintVersion, forward.FingerprintVersion)

	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "short-reverse",
		PromptText: "hello B",
		TTLSeconds: 60,
	}))
	reverse, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:        10,
		Model:         "short-reverse",
		PromptText:    "hello AA",
		MinMatchRatio: 0.7,
	})
	require.NoError(t, err)
	assert.True(t, reverse.Found)
	assert.InDelta(t, 6.0/8.0, reverse.MatchRatio, 0.000001)
}

func TestSimulatedModelCachePartialFingerprintMatchIsScopedByUserAndModel(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	prompt := strings.Repeat("scope content ", 100)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: prompt,
		TTLSeconds: 60,
	}))

	matched, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:        10,
		Model:         "gpt-test",
		PromptText:    prompt,
		MinMatchRatio: 1,
	})
	require.NoError(t, err)
	assert.True(t, matched.Found)
	assert.Equal(t, 1.0, matched.MatchRatio)
	assert.Equal(t, 1, matched.CandidateCount)

	for _, req := range []SimulatedModelCachePartialMatchRequest{
		{UserID: 11, Model: "gpt-test", PromptText: prompt},
		{UserID: 10, Model: "other-model", PromptText: prompt},
	} {
		result, findErr := FindSimulatedModelCachePartialMatch(ctx, req)
		require.NoError(t, findErr)
		assert.False(t, result.Found)
		assert.Zero(t, result.CandidateCount)
	}
}

func TestSimulatedModelCacheSharedScopeTTLIsNotShortenedByAnotherChannelSetting(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const userID = 10
	const model = "gpt-test"

	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      model,
		PromptText: strings.Repeat("long ttl prompt ", 20),
		TTLSeconds: 3600,
	}))
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      model,
		PromptText: strings.Repeat("short ttl prompt ", 20),
		TTLSeconds: 60,
	}))

	ttl, err := common.RDB.TTL(ctx, simulatedModelCacheScopeIndexKey(userID, model)).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, 30*time.Minute)
}

func TestSimulatedModelCachePartialMatchPrunesMissingFingerprintMembers(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	prompt := strings.Repeat("expired fingerprint ", 20)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: prompt,
		TTLSeconds: 60,
	}))

	promptID := sha256Hex([]byte(prompt))
	require.NoError(t, common.RDB.Del(ctx, simulatedModelCacheFingerprintKey(promptID)).Err())
	result, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: strings.Repeat("current prompt ", 20),
	})
	require.NoError(t, err)
	assert.False(t, result.Found)

	count, err := common.RDB.ZCard(ctx, simulatedModelCacheScopeIndexKey(10, "gpt-test")).Result()
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSimulatedModelCachePartialMatchPrunesInvalidFineFingerprint(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const prompt = "hello AA"
	fingerprint, err := buildSimulatedModelCachePromptFingerprint(ctx, prompt)
	require.NoError(t, err)
	require.NotEmpty(t, fingerprint.FineHashes)
	fingerprint.FineHashes = fingerprint.FineHashes[:len(fingerprint.FineHashes)-1]
	raw, err := common.Marshal(fingerprint)
	require.NoError(t, err)
	promptID := sha256Hex([]byte(prompt))
	require.NoError(t, common.RDB.Set(ctx, simulatedModelCacheFingerprintKey(promptID), raw, time.Minute).Err())
	indexKey := simulatedModelCacheScopeIndexKey(10, "gpt-test")
	require.NoError(t, common.RDB.ZAdd(ctx, indexKey, &redis.Z{Score: 1, Member: promptID}).Err())

	result, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: "hello B",
	})
	require.NoError(t, err)
	assert.False(t, result.Found)

	count, err := common.RDB.ZCard(ctx, indexKey).Result()
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSimulatedModelCacheWorkerReturnsPreparedFingerprintWithoutStoring(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	prompt := strings.Repeat("worker query only prompt ", 20)
	handle, bypassReason := SubmitSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: prompt,
	})
	require.Empty(t, bypassReason)
	require.NotNil(t, handle)

	result := <-handle.result
	require.NoError(t, result.Err)
	require.NotNil(t, result.Prepared)
	exists, err := common.RDB.Exists(ctx, simulatedModelCacheFingerprintKey(sha256Hex([]byte(prompt)))).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)

	reservation := ReserveSimulatedModelCacheMemory(common.GetSimulatedModelCacheMemoryBudgetBytes())
	require.NotNil(t, reservation)
	defer reservation.Release()
	require.NoError(t, result.Prepared.Store(ctx), "prepared storage must not reserve or rebuild the fingerprint")
	exists, err = common.RDB.Exists(ctx, simulatedModelCacheFingerprintKey(sha256Hex([]byte(prompt)))).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
}

func withSimulatedModelCacheTestRedis(t *testing.T) context.Context {
	t.Helper()

	redisURL := os.Getenv("NEW_API_TEST_REDIS_CONN_STRING")
	var opt *redis.Options
	if redisURL != "" {
		parsed, err := redis.ParseURL(redisURL)
		require.NoError(t, err)
		opt = parsed
	} else {
		server := miniredis.RunT(t)
		opt = &redis.Options{Addr: server.Addr()}
	}

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

func TestPatchSimulatedModelCacheResponseBodyUpdatesOpenAIUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		TotalTokens:      108,
	}
	ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_fingerprint",
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

func TestPatchSimulatedModelCacheResponseBodyUpdatesOpenAIModelInSSE(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2,
		CompletionTokens: 3,
		TotalTokens:      5,
	}
	body := []byte(strings.Join([]string{
		`data: {"id":"chatcmpl_test","model":"xopglm52","choices":[{"delta":{"content":"ok"},"index":0}]}`,
		`data: {"id":"chatcmpl_test","model":"xopglm52","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n"))

	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatOpenAI, "text/event-stream", body, usage, "glm-5.2")

	got := string(patched)
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
	require.Contains(t, got, `data: [DONE]`)
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesResponsesModelInSSE(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2,
		CompletionTokens: 3,
		TotalTokens:      5,
	}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_test","model":"xopglm52"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_test","model":"xopglm52","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n"))

	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatOpenAIResponses, "text/event-stream", body, usage, "glm-5.2")

	got := string(patched)
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
	require.Contains(t, got, `data: [DONE]`)
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesClaudeUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		TotalTokens:      108,
		UsageSemantic:    "anthropic",
	}
	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_fingerprint",
		MatchRatio: 0.25,
	})
	require.NotNil(t, marker)
	responseUsage := *usage
	responseUsage.PromptTokens = marker.OriginalPromptTokens
	responseUsage.InputTokens = marker.OriginalPromptTokens
	responseUsage.TotalTokens = marker.OriginalPromptTokens + usage.CompletionTokens

	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":8}}`)
	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatClaude, "application/json", body, &responseUsage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(patched, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usageMap["input_tokens"])
	assert.Equal(t, float64(25), usageMap["cache_read_input_tokens"])
	assert.Equal(t, float64(8), usageMap["output_tokens"])
	assert.Equal(t, float64(0), usageMap["cache_creation_input_tokens"])
	assert.Equal(t, float64(0), usageMap["claude_cache_creation_5_m_tokens"])
	assert.Equal(t, float64(0), usageMap["claude_cache_creation_1_h_tokens"])
	assert.Equal(t, 75, usage.PromptTokens, "response formatting must not restore billing prompt tokens")
	assert.Equal(t, 25, usage.PromptTokensDetails.CachedTokens)
}

func TestPatchSimulatedModelCacheResponseBodyPreservesClaudeCacheCreationFields(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		UsageSemantic:    "anthropic",
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 25,
		},
	}
	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":8,"cache_creation_input_tokens":11,"claude_cache_creation_5_m_tokens":4,"claude_cache_creation_1_h_tokens":7}}`)

	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatClaude, "application/json", body, usage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(patched, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(11), usageMap["cache_creation_input_tokens"])
	assert.Equal(t, float64(4), usageMap["claude_cache_creation_5_m_tokens"])
	assert.Equal(t, float64(7), usageMap["claude_cache_creation_1_h_tokens"])
}
