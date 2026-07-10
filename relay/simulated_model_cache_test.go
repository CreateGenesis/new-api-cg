package relay

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
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

	usage := &dto.Usage{PromptTokens: 70, CompletionTokens: 20, TotalTokens: 90, UsageSemantic: "anthropic"}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var delta dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatClaude, simulatedModelCacheStreamEventClaudeDelta), &delta))
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 35, delta.Usage.InputTokens)
	assert.Equal(t, 35, delta.Usage.CacheReadInputTokens)
	assert.Equal(t, 20, delta.Usage.OutputTokens)
	assert.Contains(t, w.Body.String(), `"vendor":"kept"`)
	assert.True(t, bytes.HasSuffix(w.Body.Bytes(), []byte("data: {\"type\":\"message_stop\"}\n\n")))
	require.NotNil(t, info.SimulatedModelCacheInfo)
	assert.Equal(t, 35, info.SimulatedModelCacheInfo.SimulatedCachedTokens)
	require.NotNil(t, info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.True(t, *info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.Equal(t, 35, usage.PromptTokensDetails.CachedTokens)
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

	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var final dto.ChatCompletionsStreamResponse
	require.NoError(t, common.Unmarshal(findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatOpenAI, simulatedModelCacheStreamEventOpenAIUsage), &final))
	require.NotNil(t, final.Usage)
	assert.Equal(t, 100, final.Usage.PromptTokens)
	assert.Equal(t, 20, final.Usage.CompletionTokens)
	assert.Equal(t, 120, final.Usage.TotalTokens)
	assert.Equal(t, 40, final.Usage.PromptTokensDetails.CachedTokens)
	assert.Less(t, bytes.Index(w.Body.Bytes(), []byte(`"cached_tokens":40`)), bytes.Index(w.Body.Bytes(), []byte("data: [DONE]")))
	assert.Equal(t, 40, info.SimulatedModelCacheInfo.SimulatedCachedTokens)
	assert.Equal(t, 40, usage.PromptTokensDetails.CachedTokens)
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
	usage := &dto.Usage{PromptTokens: 80, CompletionTokens: 10, TotalTokens: 90, UsageSemantic: "anthropic"}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var final dto.ChatCompletionsStreamResponse
	require.NoError(t, common.Unmarshal(findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatOpenAI, simulatedModelCacheStreamEventOpenAIUsage), &final))
	require.NotNil(t, final.Usage)
	assert.Equal(t, 80, final.Usage.PromptTokens, "OpenAI prompt tokens include cached input after Claude conversion")
	assert.Equal(t, 90, final.Usage.TotalTokens)
	assert.Equal(t, 20, final.Usage.PromptTokensDetails.CachedTokens)
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
	usage := &dto.Usage{PromptTokens: 60, CompletionTokens: 5, TotalTokens: 65}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	var delta dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(findSimulatedModelCacheStreamEvent(t, w.Body.Bytes(), types.RelayFormatClaude, simulatedModelCacheStreamEventClaudeDelta), &delta))
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 30, delta.Usage.InputTokens, "Claude input tokens exclude cached input after OpenAI conversion")
	assert.Equal(t, 30, delta.Usage.CacheReadInputTokens)
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
	usage := &dto.Usage{PromptTokens: 40, CompletionTokens: 4, TotalTokens: 44}
	finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage)

	assert.Equal(t, original, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"usage"`)
	require.NotNil(t, info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.False(t, *info.SimulatedModelCacheInfo.StreamUsageInjected)
	assert.Equal(t, 20, usage.PromptTokensDetails.CachedTokens, "local billing still uses the simulated cache match")
}

func TestSimulatedModelCacheStreamNoMatchPreservesRealUpstreamUsage(t *testing.T) {
	c, w, info, attempt, recorder := newSimulatedModelCacheStreamTest(t, types.RelayFormatOpenAI, types.RelayFormatOpenAI, true, service.SimulatedModelCachePartialMatch{})
	original := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":4,\"total_tokens\":44,\"prompt_tokens_details\":{\"cached_tokens\":9}}}\n\ndata: [DONE]\n\n"

	_, err := c.Writer.Write([]byte(original))
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 40, CompletionTokens: 4, TotalTokens: 44, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 9}}
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
	usage := &dto.Usage{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22}

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
			usage := &dto.Usage{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22}

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

	usage := &dto.Usage{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22}
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
	usage := &dto.Usage{PromptTokens: 40, CompletionTokens: 4, TotalTokens: 44, UsageSemantic: "anthropic"}
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
	usage := &dto.Usage{PromptTokens: 30, CompletionTokens: 3, TotalTokens: 33, UsageSemantic: "anthropic"}
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

	usage := &dto.Usage{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22}
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
