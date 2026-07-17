package relay

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type simulatedModelCacheFailingWriter struct {
	header http.Header
	body   bytes.Buffer
	writes int
	failAt int
	status int
}

type simulatedModelCacheTestMatchTask struct {
	result   service.SimulatedModelCachePartialMatchResult
	ready    bool
	canceled bool
}

func (t *simulatedModelCacheTestMatchTask) TryResult() (service.SimulatedModelCachePartialMatchResult, bool) {
	return t.result, t.ready
}

func (t *simulatedModelCacheTestMatchTask) Cancel() {
	t.canceled = true
}

func (t *simulatedModelCacheTestMatchTask) StoreWhenReady(context.Context) {}

func (w *simulatedModelCacheFailingWriter) Header() http.Header {
	return w.header
}

func (w *simulatedModelCacheFailingWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *simulatedModelCacheFailingWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("client write failed")
	}
	return w.body.Write(data)
}

func (w *simulatedModelCacheFailingWriter) Flush() {}

func newSimulatedModelCacheStreamTest(t *testing.T, responseFormat types.RelayFormat, _ types.RelayFormat, includeUsage bool, match service.SimulatedModelCachePartialMatch) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo, *simulatedModelCacheAttempt, *simulatedModelCacheRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/stream", nil)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	attempt := &simulatedModelCacheAttempt{
		settings:       dto.SimulatedModelCacheSettings{Enabled: true},
		promptText:     "hello",
		cacheModelName: "requested-model",
		partialMatch: &simulatedModelCacheTestMatchTask{
			result: service.SimulatedModelCachePartialMatchResult{Match: match},
			ready:  true,
		},
	}
	info := &relaycommon.RelayInfo{
		RelayFormat:        responseFormat,
		IsStream:           true,
		ShouldIncludeUsage: includeUsage,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         7,
			UpstreamModelName: "upstream-model",
		},
	}
	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	require.NotNil(t, recorder)
	return c, w, info, attempt, recorder
}

func findSimulatedModelCacheStreamEvent(t *testing.T, body []byte, format types.RelayFormat, kind simulatedModelCacheStreamEventKind) []byte {
	t.Helper()
	for _, event := range splitSimulatedModelCacheSSEChunks(body) {
		if simulatedModelCacheStreamEventType(format, event) == kind {
			data, ok := simulatedModelCacheSSEData(event)
			require.True(t, ok)
			return data
		}
	}
	require.FailNow(t, "stream event not found", "kind=%d body=%s", kind, body)
	return nil
}

func TestSimulatedModelCacheModelNameSharesRequestedModelAcrossChannels(t *testing.T) {
	first := &relaycommon.RelayInfo{
		OriginModelName: "shared-model",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         1,
			UpstreamModelName: "provider-a-model",
		},
	}
	second := &relaycommon.RelayInfo{
		OriginModelName: "shared-model",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         2,
			UpstreamModelName: "provider-b-model",
		},
	}

	assert.Equal(t, "shared-model", simulatedModelCacheModelName(first))
	assert.Equal(t, simulatedModelCacheModelName(first), simulatedModelCacheModelName(second))
}

func TestSimulatedModelCacheModelNameFallsBackToUpstreamModel(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: " upstream-model "},
	}

	assert.Equal(t, "upstream-model", simulatedModelCacheModelName(info))
	assert.Empty(t, simulatedModelCacheModelName(nil))
}

func TestHiddenSimulatedModelCacheMatchDoesNotRewriteResponseOrLogInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	attempt := &simulatedModelCacheAttempt{
		settings:       dto.SimulatedModelCacheSettings{Enabled: false},
		visible:        false,
		promptText:     "hidden cache",
		cacheModelName: "gpt-test",
		precomputed:    &service.SimulatedModelCachePartialMatchResult{Match: service.SimulatedModelCachePartialMatch{Found: true, MatchRatio: 1}},
	}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1}}
	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	require.NotNil(t, recorder)
	original := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`)
	_, err := recorder.Write(original)
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105}

	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	assert.JSONEq(t, string(original), w.Body.String())
	assert.Nil(t, info.SimulatedModelCacheInfo)
	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
}

func TestSimulatedModelCacheModelNameKeepsRequestedCompactionModel(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "provider-compact-model",
		Request: &dto.OpenAIResponsesCompactionRequest{
			Model: "requested-compact-model",
		},
	}

	assert.Equal(t, "requested-compact-model", simulatedModelCacheModelName(info))
}

func TestSimulatedModelCacheLowBudgetDoesNotCancelMatchBeforeBuffering(t *testing.T) {
	originalBudget := common.GetSimulatedModelCacheMemoryBudgetMB()
	common.SetSimulatedModelCacheMemoryBudgetMB(1)
	t.Cleanup(func() {
		common.SetSimulatedModelCacheMemoryBudgetMB(originalBudget)
	})
	matchReservation := service.ReserveSimulatedModelCacheMemory(64 * 1024)
	require.NotNil(t, matchReservation)
	t.Cleanup(matchReservation.Release)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	task := &simulatedModelCacheTestMatchTask{}
	attempt := &simulatedModelCacheAttempt{
		settings:     dto.SimulatedModelCacheSettings{Enabled: true},
		partialMatch: task,
	}

	recorder := beginSimulatedModelCacheRecorder(c, &relaycommon.RelayInfo{}, attempt)
	require.NotNil(t, recorder)
	assert.False(t, task.canceled)
	assert.Equal(t, task, attempt.partialMatch)
}

func TestSimulatedModelCacheRecorderPassesThroughStreamWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	attempt := &simulatedModelCacheAttempt{
		promptText: "hello",
	}
	info := &relaycommon.RelayInfo{IsStream: true}

	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	require.NotNil(t, recorder)

	_, err := c.Writer.Write([]byte("data: one\n\n"))
	require.NoError(t, err)
	c.Writer.Flush()

	assert.Equal(t, "data: one\n\n", w.Body.String())
	assert.Empty(t, recorder.body.String(), "streaming responses must not accumulate the full body in memory")
}

func TestSimulatedModelCacheClaudeStreamRewritesFinalUsageForDownstreamParser(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatClaude, types.RelayFormatClaude, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.5,
	})
	content := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
	tail := "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":20},\"vendor\":\"kept\"}\n\n" +
		": final ping\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	_, err := c.Writer.Write([]byte(content + tail))
	require.NoError(t, err)
	assert.Equal(t, content, w.Body.String(), "content events must not wait for cache matching")

	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 20, TotalTokens: 532, UsageSemantic: "anthropic"}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	deltaData := findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatClaude, simulatedModelCacheStreamEventClaudeDelta)
	var delta dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(deltaData, &delta))
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 256, delta.Usage.InputTokens)
	assert.Equal(t, 256, delta.Usage.CacheReadInputTokens)
	assert.Equal(t, 20, delta.Usage.OutputTokens)
	var deltaPayload map[string]any
	require.NoError(t, common.Unmarshal(deltaData, &deltaPayload))
	deltaUsage, ok := deltaPayload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), deltaUsage["cache_creation_input_tokens"])
	assert.Equal(t, float64(0), deltaUsage["claude_cache_creation_5_m_tokens"])
	assert.Equal(t, float64(0), deltaUsage["claude_cache_creation_1_h_tokens"])
	assert.Contains(t, w.Body.String(), `"vendor":"kept"`)
	assert.True(t, bytes.HasSuffix(w.Body.Bytes(), []byte("data: {\"type\":\"message_stop\"}\n\n")))
	require.NotNil(t, info.SimulatedModelCacheInfo)
	assert.Equal(t, 512, info.SimulatedModelCacheInfo.OriginalPromptTokens)
	assert.Equal(t, 256, info.SimulatedModelCacheInfo.SimulatedCachedTokens)
	require.NotNil(t, info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.True(t, *info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.Equal(t, 256, usage.PromptTokens, "billing usage keeps only the uncached Anthropic input")
	assert.Equal(t, 256, usage.InputTokens)
	assert.Equal(t, 256, usage.PromptTokensDetails.CachedTokens)
}

func TestPatchSimulatedModelCacheClaudeStreamPreservesCacheCreationFields(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		UsageSemantic:    "anthropic",
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         25,
			CachedCreationTokens: 11,
		},
		ClaudeCacheCreation5mTokens: 4,
		ClaudeCacheCreation1hTokens: 7,
	}
	event := []byte("data: {\"type\":\"message_delta\",\"usage\":{\"cache_creation_input_tokens\":99,\"claude_cache_creation_5_m_tokens\":98,\"claude_cache_creation_1_h_tokens\":97}}\n\n")

	patched, ok := patchSimulatedModelCacheStreamUsageEvent(types.RelayFormatClaude, event, usage)
	require.True(t, ok)
	data, ok := simulatedModelCacheSSEData(patched)
	require.True(t, ok)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(data, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(11), usageMap["cache_creation_input_tokens"])
	assert.Equal(t, float64(4), usageMap["claude_cache_creation_5_m_tokens"])
	assert.Equal(t, float64(7), usageMap["claude_cache_creation_1_h_tokens"])
}

func TestSimulatedModelCacheClaudeNonStreamReturnsUncachedInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Writer.Header().Set("Content-Type", "application/json")
	attempt := &simulatedModelCacheAttempt{
		settings:   dto.SimulatedModelCacheSettings{Enabled: true},
		promptText: "hello",
		partialMatch: &simulatedModelCacheTestMatchTask{
			result: service.SimulatedModelCachePartialMatchResult{Match: service.SimulatedModelCachePartialMatch{
				Found:      true,
				MatchRatio: 0.5,
			}},
			ready: true,
		},
	}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	require.NotNil(t, recorder)
	_, err := c.Writer.Write([]byte(`{"usage":{"input_tokens":70,"output_tokens":20}}`))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 20, TotalTokens: 532, UsageSemantic: "anthropic"}

	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(256), usageMap["input_tokens"])
	assert.Equal(t, float64(256), usageMap["cache_read_input_tokens"])
	assert.Equal(t, float64(0), usageMap["cache_creation_input_tokens"])
	assert.Equal(t, float64(0), usageMap["claude_cache_creation_5_m_tokens"])
	assert.Equal(t, float64(0), usageMap["claude_cache_creation_1_h_tokens"])
	assert.Equal(t, 256, usage.PromptTokens)
	assert.Equal(t, 256, usage.PromptTokensDetails.CachedTokens)
}

func TestSimulatedModelCacheOpenAIStreamRewritesUsageBeforeDone(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatOpenAI, types.RelayFormatOpenAI, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.4,
	})
	content := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}],\"usage\":{\"prompt_tokens\":1}}\n\n"
	tail := "data: {\"id\":\"chatcmpl-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120,\"prompt_tokens_details\":{\"cached_tokens\":7}}}\n\n" +
		"data: [DONE]\n\n"

	_, err := c.Writer.Write([]byte(content + tail))
	require.NoError(t, err)
	assert.Equal(t, content, w.Body.String())

	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 20, TotalTokens: 532}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var final dto.ChatCompletionsStreamResponse
	require.NoError(t, common.Unmarshal(findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatOpenAI, simulatedModelCacheStreamEventOpenAIUsage), &final))
	require.NotNil(t, final.Usage)
	assert.Equal(t, 512, final.Usage.PromptTokens)
	assert.Equal(t, 20, final.Usage.CompletionTokens)
	assert.Equal(t, 532, final.Usage.TotalTokens)
	assert.Equal(t, 205, final.Usage.PromptTokensDetails.CachedTokens)
	assert.Less(t, bytes.Index(w.Body.Bytes(), []byte(`"cached_tokens":205`)), bytes.Index(w.Body.Bytes(), []byte("data: [DONE]")))
	assert.Equal(t, 205, info.SimulatedModelCacheInfo.SimulatedCachedTokens)
	assert.Equal(t, 205, usage.PromptTokensDetails.CachedTokens)
}

func TestSimulatedModelCacheOpenAIStreamInjectsMissingUsageBeforeDone(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatOpenAI, types.RelayFormatClaude, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.25,
	})
	original := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\ndata: [DONE]\r\n\r\n"

	_, err := c.Writer.Write([]byte(original[:37]))
	require.NoError(t, err)
	_, err = c.Writer.Write([]byte(original[37:]))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 10, TotalTokens: 522, UsageSemantic: "anthropic"}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var final dto.ChatCompletionsStreamResponse
	require.NoError(t, common.Unmarshal(findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatOpenAI, simulatedModelCacheStreamEventOpenAIUsage), &final))
	require.NotNil(t, final.Usage)
	assert.Equal(t, 512, final.Usage.PromptTokens, "OpenAI prompt tokens include cached input after Claude conversion")
	assert.Equal(t, 522, final.Usage.TotalTokens)
	assert.Equal(t, 128, final.Usage.PromptTokensDetails.CachedTokens)
	assert.Contains(t, w.Body.String(), "\r\n\r\ndata: [DONE]\r\n\r\n")
}

func TestSimulatedModelCacheStreamUsesDownstreamClaudeFormatAfterOpenAIConversion(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatClaude, types.RelayFormatOpenAI, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.5,
	})
	original := "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"

	_, err := c.Writer.Write([]byte(original))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 5, TotalTokens: 517}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var delta dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatClaude, simulatedModelCacheStreamEventClaudeDelta), &delta))
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 256, delta.Usage.InputTokens, "Claude response reports uncached input separately from cache")
	assert.Equal(t, 256, delta.Usage.CacheReadInputTokens)
	assert.Equal(t, 512, usage.PromptTokens)
	assert.Equal(t, 256, usage.PromptTokensDetails.CachedTokens)
	assert.NotContains(t, w.Body.String(), "data: [DONE]")
}

func TestSimulatedModelCacheOpenAIStreamExplicitUsageFalsePreservesResponse(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatOpenAI, types.RelayFormatOpenAI, false, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.5,
	})
	original := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"

	_, err := c.Writer.Write([]byte(original))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 4, TotalTokens: 516}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	assert.Equal(t, original, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"usage"`)
	require.NotNil(t, info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.False(t, *info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.Equal(t, 256, usage.PromptTokensDetails.CachedTokens, "local billing still uses the simulated cache match")
}

func TestSimulatedModelCacheConfiguredMinimumIgnoresMatchAndDoesNotStore(t *testing.T) {
	setSimulatedModelCacheMinInputTokensForTest(t, 513)
	ctx := withSimulatedModelCacheRelayTestRedis(t)
	const userID = 10
	const model = "gpt-test"
	require.NoError(t, service.StoreSimulatedModelCachePromptFingerprint(ctx, service.SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      model,
		PromptText: "hello AA",
		TTLSeconds: 60,
	}))

	match, err := service.FindSimulatedModelCachePartialMatch(ctx, service.SimulatedModelCachePartialMatchRequest{
		UserID:        userID,
		Model:         model,
		PromptText:    "hello B",
		MinMatchRatio: 0.8,
	})
	require.NoError(t, err)
	require.True(t, match.Found)

	for range 2 {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Writer.Header().Set("Content-Type", "application/json")
		attempt := &simulatedModelCacheAttempt{
			settings:       dto.SimulatedModelCacheSettings{Enabled: true, TTLSeconds: 60},
			promptText:     "hello B",
			cacheModelName: model,
			partialMatch: &simulatedModelCacheTestMatchTask{
				result: service.SimulatedModelCachePartialMatchResult{Match: match},
				ready:  true,
			},
		}
		info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, UserId: userID, ChannelMeta: &relaycommon.ChannelMeta{}}
		recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
		require.NotNil(t, recorder)
		_, err = c.Writer.Write([]byte(`{"usage":{"prompt_tokens":512,"completion_tokens":1,"total_tokens":513}}`))
		require.NoError(t, err)
		usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 1, TotalTokens: 513}

		finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

		assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
		require.NotNil(t, info.SimulatedModelCacheInfo)
		assert.Equal(t, service.SimulatedModelCacheBypassInputTokensLow, info.SimulatedModelCacheInfo.BypassReason)
	}

	result, err := service.FindSimulatedModelCachePartialMatch(ctx, service.SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      model,
		PromptText: "unrelated prompt",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.CandidateCount, "the request below the configured threshold must not add a fingerprint")
}

func TestSimulatedModelCacheConfiguredMinimumStoresEligibleRequests(t *testing.T) {
	tests := []struct {
		name        string
		threshold   int
		inputTokens int
	}{
		{name: "at default threshold", threshold: 128, inputTokens: 128},
		{name: "minimum threshold disabled", threshold: 0, inputTokens: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setSimulatedModelCacheMinInputTokensForTest(t, test.threshold)
			ctx := withSimulatedModelCacheRelayTestRedis(t)
			const userID = 10
			const model = "gpt-test"

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Writer.Header().Set("Content-Type", "application/json")
			attempt := &simulatedModelCacheAttempt{
				settings:       dto.SimulatedModelCacheSettings{Enabled: true, TTLSeconds: 60},
				promptText:     "hello AA",
				cacheModelName: model,
			}
			handle, bypassReason := service.SubmitSimulatedModelCachePartialMatch(ctx, service.SimulatedModelCachePartialMatchRequest{
				UserID:        userID,
				Model:         model,
				PromptText:    attempt.promptText,
				MinMatchRatio: 0.8,
				TTLSeconds:    attempt.settings.TTLSeconds,
			})
			require.Empty(t, bypassReason)
			require.NotNil(t, handle)
			attempt.partialMatch = handle
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, UserId: userID, ChannelMeta: &relaycommon.ChannelMeta{}}
			recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
			require.NotNil(t, recorder)
			_, err := c.Writer.Write([]byte(`{}`))
			require.NoError(t, err)
			usage := &dto.Usage{PromptTokens: test.inputTokens, CompletionTokens: 1, TotalTokens: test.inputTokens + 1}

			finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

			if info.SimulatedModelCacheInfo != nil {
				assert.Equal(t, service.SimulatedModelCacheBypassMatchNotReady, info.SimulatedModelCacheInfo.BypassReason)
			}
			require.Eventually(t, func() bool {
				match, findErr := service.FindSimulatedModelCachePartialMatch(ctx, service.SimulatedModelCachePartialMatchRequest{
					UserID:        userID,
					Model:         model,
					PromptText:    "hello B",
					MinMatchRatio: 0.8,
				})
				return findErr == nil && match.Found && match.CandidateCount == 1
			}, time.Second, 10*time.Millisecond)
		})
	}
}

func TestSimulatedModelCacheOriginalInputTokensUsesNormalizedFullInput(t *testing.T) {
	tests := []struct {
		name  string
		usage *dto.Usage
	}{
		{name: "OpenAI prompt tokens", usage: &dto.Usage{PromptTokens: 512}},
		{name: "Responses input token fallback", usage: &dto.Usage{InputTokens: 512}},
		{name: "Claude uncached read and creation tokens", usage: &dto.Usage{
			PromptTokens:  400,
			UsageSemantic: "anthropic",
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         100,
				CachedCreationTokens: 12,
			},
		}},
		{name: "Gemini normalized prompt tokens", usage: &dto.Usage{PromptTokens: 512}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, 512, simulatedModelCacheOriginalInputTokens(test.usage))
		})
	}
}

func TestSimulatedModelCacheMinimumUsesClaudeFullOriginalInput(t *testing.T) {
	setSimulatedModelCacheMinInputTokensForTest(t, 512)
	c, _, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatClaude, types.RelayFormatClaude, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.5,
	})
	_, err := c.Writer.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	require.NoError(t, err)
	usage := &dto.Usage{
		PromptTokens:     400,
		CompletionTokens: 1,
		TotalTokens:      401,
		UsageSemantic:    "anthropic",
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         100,
			CachedCreationTokens: 12,
		},
	}

	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	require.NotNil(t, info.SimulatedModelCacheInfo)
	assert.Empty(t, info.SimulatedModelCacheInfo.BypassReason)
	assert.Equal(t, "partial_fingerprint", info.SimulatedModelCacheInfo.Mode)
}

func TestSimulatedModelCacheStreamNoMatchPreservesRealUpstreamUsage(t *testing.T) {
	withSimulatedModelCacheRelayTestRedis(t)
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatOpenAI, types.RelayFormatOpenAI, true, service.SimulatedModelCachePartialMatch{})
	original := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":4,\"total_tokens\":44,\"prompt_tokens_details\":{\"cached_tokens\":9}}}\n\ndata: [DONE]\n\n"

	_, err := c.Writer.Write([]byte(original))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 4, TotalTokens: 516, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 9}}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	assert.Equal(t, original, w.Body.String())
	assert.Nil(t, info.SimulatedModelCacheInfo)
	assert.Equal(t, 9, usage.PromptTokensDetails.CachedTokens)
}

func TestSimulatedModelCacheMatchNotReadyPreservesRealUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	attempt := &simulatedModelCacheAttempt{
		settings:     dto.SimulatedModelCacheSettings{Enabled: true},
		promptText:   "hello",
		partialMatch: &simulatedModelCacheTestMatchTask{},
	}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	require.NotNil(t, recorder)
	_, err := c.Writer.Write([]byte(`{"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 2, TotalTokens: 514}

	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	assert.Equal(t, 0, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, info.SimulatedModelCacheInfo)
	assert.Equal(t, service.SimulatedModelCacheBypassMatchNotReady, info.SimulatedModelCacheInfo.BypassReason)
	assert.JSONEq(t, `{"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`, w.Body.String())
}

func TestSimulatedModelCacheOverloadBypassPreservesRealUsage(t *testing.T) {
	for _, bypassReason := range []string{
		service.SimulatedModelCacheBypassWorkerQueueFull,
		service.SimulatedModelCacheBypassMemoryBudget,
	} {
		t.Run(bypassReason, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			attempt := &simulatedModelCacheAttempt{
				settings:     dto.SimulatedModelCacheSettings{Enabled: true},
				promptText:   "hello",
				bypassReason: bypassReason,
			}
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{}}
			recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
			require.NotNil(t, recorder)
			_, err := c.Writer.Write([]byte(`{"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`))
			require.NoError(t, err)
			usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 2, TotalTokens: 514}

			finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

			assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
			require.NotNil(t, info.SimulatedModelCacheInfo)
			assert.Equal(t, bypassReason, info.SimulatedModelCacheInfo.BypassReason)
			assert.JSONEq(t, `{"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`, w.Body.String())
		})
	}
}

func TestSimulatedModelCacheNonStreamResponseOverLimitSwitchesToPassThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	attempt := &simulatedModelCacheAttempt{
		settings:   dto.SimulatedModelCacheSettings{Enabled: true},
		promptText: "hello",
		partialMatch: &simulatedModelCacheTestMatchTask{
			result: service.SimulatedModelCachePartialMatchResult{Match: service.SimulatedModelCachePartialMatch{Found: true, MatchRatio: 1}},
			ready:  true,
		},
	}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{}}
	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	require.NotNil(t, recorder)
	recorder.responseBufferLimit = 8
	body := []byte("0123456789abcdef")

	_, err := c.Writer.Write(body)
	require.NoError(t, err)
	assert.Equal(t, body, w.Body.Bytes())
	assert.True(t, recorder.passThrough)
	assert.Empty(t, recorder.body.Bytes())

	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 2, TotalTokens: 514}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)
	require.NotNil(t, info.SimulatedModelCacheInfo)
	assert.Equal(t, service.SimulatedModelCacheBypassResponseTooLarge, info.SimulatedModelCacheInfo.BypassReason)
	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
}

func TestSimulatedModelCacheStreamCancellationPreservesTail(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatClaude, types.RelayFormatClaude, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.5,
	})
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	original := "data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":40,\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n"

	_, err := c.Writer.Write([]byte(original))
	require.NoError(t, err)
	cancel()
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 4, TotalTokens: 516, UsageSemantic: "anthropic"}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	assert.Equal(t, original, w.Body.String())
	require.NotNil(t, info.SimulatedModelCacheInfo)
	assert.Equal(t, service.SimulatedModelCacheBypassRequestCanceled, info.SimulatedModelCacheInfo.BypassReason)
	assert.Zero(t, usage.PromptTokensDetails.CachedTokens)
}

func TestRestoreSimulatedModelCacheRecorderReleasesBufferedTailOnUpstreamError(t *testing.T) {
	c, w, _, _, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatClaude, types.RelayFormatClaude, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.5,
	})
	original := "data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":40}}\n\ndata: {\"type\":\"message_stop\"}\n\n"

	_, err := c.Writer.Write([]byte(original))
	require.NoError(t, err)
	assert.Empty(t, w.Body.String())
	restoreSimulatedModelCacheRecorder(c, recorder)

	assert.Equal(t, original, w.Body.String())
}

func TestSimulatedModelCacheStreamPreservesMalformedEventsAndComments(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatClaude, types.RelayFormatOpenAI, true, service.SimulatedModelCachePartialMatch{
		Found:      true,
		MatchRatio: 0.5,
	})
	original := ": ping\r\n\r\ndata: {not-json}\r\n\r\ndata: {\"type\":\"message_stop\",\"unknown\":true}\r\n\r\n"

	_, err := c.Writer.Write([]byte(original[:19]))
	require.NoError(t, err)
	_, err = c.Writer.Write([]byte(original[19:]))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 3, TotalTokens: 515, UsageSemantic: "anthropic"}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	assert.Equal(t, original, w.Body.String())
	require.NotNil(t, info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.False(t, *info.SimulatedModelCacheInfo.StreamUsageInjected)
}

func TestSimulatedModelCacheStreamMarksFailedTailWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	underlying := &simulatedModelCacheFailingWriter{header: make(http.Header), failAt: 2}
	c, _ := gin.CreateTestContext(underlying)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	attempt := &simulatedModelCacheAttempt{
		settings:   dto.SimulatedModelCacheSettings{Enabled: true},
		promptText: "hello",
		partialMatch: &simulatedModelCacheTestMatchTask{
			result: service.SimulatedModelCachePartialMatchResult{
				Match: service.SimulatedModelCachePartialMatch{Found: true, MatchRatio: 0.5},
			},
			ready: true,
		},
	}
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		IsStream:           true,
		ShouldIncludeUsage: true,
		ChannelMeta:        &relaycommon.ChannelMeta{},
	}
	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	_, err := c.Writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)

	usage := &dto.Usage{PromptTokens: 512, CompletionTokens: 2, TotalTokens: 514}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	require.NotNil(t, info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.False(t, *info.SimulatedModelCacheInfo.StreamUsageInjected)
}

func TestSplitSimulatedModelCacheSSEChunksPreservesEventBoundaries(t *testing.T) {
	chunks := splitSimulatedModelCacheSSEChunks([]byte("data: one\r\n\r\ndata: two\n\ntrail"))

	require.Len(t, chunks, 3)
	assert.Equal(t, []byte("data: one\r\n\r\n"), chunks[0])
	assert.Equal(t, []byte("data: two\n\n"), chunks[1])
	assert.Equal(t, []byte("trail"), chunks[2])
}

func TestSimulatedModelCacheRecorderNormalizesInvalidStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	attempt := &simulatedModelCacheAttempt{
		promptText: "hello",
	}

	recorder := beginSimulatedModelCacheRecorder(c, &relaycommon.RelayInfo{IsStream: true}, attempt)
	require.NotNil(t, recorder)

	c.Writer.WriteHeader(-1)

	assert.Equal(t, http.StatusOK, recorder.Status())
}

func withSimulatedModelCacheRelayTestRedis(t *testing.T) context.Context {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())

	oldRedisEnabled := common.RedisEnabled
	oldRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRDB
	})
	return ctx
}

func setSimulatedModelCacheMinInputTokensForTest(t *testing.T, value int) {
	t.Helper()
	original := common.GetSimulatedModelCacheMinInputTokens()
	common.SetSimulatedModelCacheMinInputTokens(value)
	t.Cleanup(func() {
		common.SetSimulatedModelCacheMinInputTokens(original)
	})
}

func TestFlushSimulatedModelCacheRecorderRewritesContentLength(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("Content-Length", "999")
	recorder := &simulatedModelCacheRecorder{
		ResponseWriter: c.Writer,
		status:         http.StatusOK,
	}

	flushSimulatedModelCacheRecorder(recorder, []byte("short"))

	assert.Equal(t, "5", w.Header().Get("Content-Length"))
	assert.Equal(t, "short", w.Body.String())
}
