package channel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type responseHeaderTimeoutRoundTripper func(*http.Request) (*http.Response, error)

func (f responseHeaderTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDoRequestWithResponseHeaderTimeoutCancelsBeforeHeaders(t *testing.T) {
	client := &http.Client{Transport: responseHeaderTimeoutRoundTripper(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, context.Cause(req.Context())
	})}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	require.NoError(t, err)

	resp, err := doRequestWithResponseHeaderTimeout(client, req, 10*time.Millisecond)

	require.Nil(t, resp)
	require.ErrorIs(t, err, errResponseHeaderTimeout)
}

func TestDoRequestWithResponseHeaderTimeoutStopsAfterHeaders(t *testing.T) {
	var upstreamContext context.Context
	client := &http.Client{Transport: responseHeaderTimeoutRoundTripper(func(req *http.Request) (*http.Response, error) {
		upstreamContext = req.Context()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("complete body")),
			Request:    req,
		}, nil
	})}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	require.NoError(t, err)

	resp, err := doRequestWithResponseHeaderTimeout(client, req, time.Hour)

	require.NoError(t, err)
	require.NotNil(t, resp)
	content, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "complete body", string(content))
	select {
	case <-upstreamContext.Done():
		require.Fail(t, "response context canceled before the response body closed")
	default:
	}
	require.NoError(t, resp.Body.Close())
	require.ErrorIs(t, upstreamContext.Err(), context.Canceled)
}

func TestResponseHeaderTimeoutPolicyShortensActiveWait(t *testing.T) {
	const channelID = 910001
	requestStarted := make(chan struct{})
	client := &http.Client{Transport: responseHeaderTimeoutRoundTripper(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		return nil, context.Cause(req.Context())
	})}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() {
		_, requestErr := doRequestWithResponseHeaderTimeoutForChannel(client, req, channelID, time.Hour)
		result <- requestErr
	}()
	<-requestStarted

	refreshActiveResponseHeaderTimeout(channelID, time.Nanosecond)
	require.ErrorIs(t, <-result, errResponseHeaderTimeout)
}

func TestResponseHeaderWaitReconfigurationReplacesTimer(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	wait := &responseHeaderWait{
		startedAt: time.Now(),
		cancel:    cancel,
		active:    true,
	}
	wait.reconfigure(time.Hour)
	firstTimer := wait.timer
	require.NotNil(t, firstTimer)

	wait.reconfigure(2 * time.Hour)
	secondTimer := wait.timer
	require.NotNil(t, secondTimer)
	require.NotSame(t, firstTimer, secondTimer)
	require.False(t, firstTimer.Stop())

	wait.reconfigure(0)
	require.Nil(t, wait.timer)
	require.False(t, secondTimer.Stop())
	require.NoError(t, context.Cause(ctx))
	require.False(t, wait.stop())
}

func TestResponseHeaderTimeoutForRelayScope(t *testing.T) {
	timeoutSeconds := 180
	settings := dto.ChannelOtherSettings{
		ResponseHeaderTimeout: &dto.ResponseHeaderTimeoutSettings{
			Enabled:        true,
			TimeoutSeconds: &timeoutSeconds,
		},
	}
	tests := []struct {
		name   string
		format types.RelayFormat
		mode   int
		stream bool
		want   time.Duration
	}{
		{name: "stream chat completions", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions, stream: true, want: 3 * time.Minute},
		{name: "stream completions", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeCompletions, stream: true, want: 3 * time.Minute},
		{name: "stream responses", format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses, stream: true, want: 3 * time.Minute},
		{name: "stream responses compact", format: types.RelayFormatOpenAIResponsesCompaction, mode: relayconstant.RelayModeResponsesCompact, stream: true, want: 3 * time.Minute},
		{name: "stream claude", format: types.RelayFormatClaude, mode: relayconstant.RelayModeChatCompletions, stream: true, want: 3 * time.Minute},
		{name: "stream gemini", format: types.RelayFormatGemini, mode: relayconstant.RelayModeGemini, stream: true, want: 3 * time.Minute},
		{name: "non-stream chat completions excluded", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions},
		{name: "stream embeddings excluded", format: types.RelayFormatEmbedding, mode: relayconstant.RelayModeEmbeddings, stream: true},
		{name: "stream images excluded", format: types.RelayFormatOpenAIImage, mode: relayconstant.RelayModeImagesGenerations, stream: true},
		{name: "realtime excluded", format: types.RelayFormatOpenAIRealtime, mode: relayconstant.RelayModeRealtime, stream: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				RelayFormat: testCase.format,
				RelayMode:   testCase.mode,
				IsStream:    testCase.stream,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: settings},
			}

			require.Equal(t, testCase.want, responseHeaderTimeoutForRelay(info))
		})
	}

	disabledInfo := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		IsStream:    true,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
			ResponseHeaderTimeout: &dto.ResponseHeaderTimeoutSettings{Enabled: false},
		}},
	}
	require.Zero(t, responseHeaderTimeoutForRelay(disabledInfo))

	defaultInfo := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		IsStream:    true,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	require.Equal(t, 3*time.Minute, responseHeaderTimeoutForRelay(defaultInfo))
}

func TestResponseBodyIdleReadCloserReturnsChannelTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	body := newResponseBodyIdleReadCloser(reader, 20*time.Millisecond)

	_, err := io.ReadAll(body)

	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, types.ErrorCodeChannelResponseBodyTimeout, apiErr.GetErrorCode())
	require.Equal(t, http.StatusGatewayTimeout, apiErr.StatusCode)
	require.ErrorIs(t, err, errResponseBodyTimeout)

	wrapped := types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	require.Equal(t, types.ErrorCodeChannelResponseBodyTimeout, wrapped.GetErrorCode())
	require.Equal(t, http.StatusGatewayTimeout, wrapped.StatusCode)
}

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}
