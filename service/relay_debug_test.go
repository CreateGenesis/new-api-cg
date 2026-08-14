package service

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeRelayDebugBodyRedactsSecretsAndOmitsBase64(t *testing.T) {
	mediaBytes := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 32)...)
	mediaBase64 := base64.StdEncoding.EncodeToString(mediaBytes)
	urlSafeBase64 := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("private-binary-data"), 8))
	payload, err := common.Marshal(map[string]interface{}{
		"prompt":      "keep this prompt",
		"private_key": "private-key-secret",
		"image": map[string]interface{}{
			"inline_data": map[string]interface{}{
				"mime_type": "image/png",
				"data":      mediaBase64,
			},
		},
		"opaque_data": urlSafeBase64,
		"source_url":  "https://example.test/image.png?sig=signed-secret&size=large",
	})
	require.NoError(t, err)

	body := sanitizeRelayDebugBody(payload, "application/json", int64(len(payload)), 1<<20)

	require.NotNil(t, body)
	assert.Equal(t, "json", body.Kind)
	assert.Contains(t, body.Text, "keep this prompt")
	assert.Contains(t, body.Text, relayDebugRedactedValue)
	assert.Contains(t, body.Text, `"omitted":"media"`)
	assert.Contains(t, body.Text, `"omitted":"base64"`)
	assert.NotContains(t, body.Text, "private-key-secret")
	assert.NotContains(t, body.Text, mediaBase64)
	assert.NotContains(t, body.Text, urlSafeBase64)
	assert.NotContains(t, body.Text, "signed-secret")
}

func TestSanitizeRelayDebugMultipartOmitsFilesAndEncodedFields(t *testing.T) {
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	require.NoError(t, writer.WriteField("api_key", "multipart-secret"))
	require.NoError(t, writer.WriteField("audio_data", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("audio"), 32))))
	file, err := writer.CreateFormFile("file", "sample.wav")
	require.NoError(t, err)
	fileBytes := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64)
	_, err = file.Write(fileBytes)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	contentType := writer.FormDataContentType()

	body := sanitizeRelayDebugBody(encoded.Bytes(), contentType, int64(encoded.Len()), 1<<20)

	require.NotNil(t, body)
	assert.Equal(t, "multipart", body.Kind)
	assert.Contains(t, body.Text, relayDebugRedactedValue)
	assert.Contains(t, body.Text, `"filename":"sample.wav"`)
	assert.Contains(t, body.Text, `"omitted":"media"`)
	assert.Contains(t, body.Text, `"omitted":"base64"`)
	assert.NotContains(t, body.Text, "multipart-secret")
	assert.NotContains(t, body.Text, string(fileBytes))
}

func TestRelayRetrySummaryDeduplicatesErrorsAndPreservesOccurrences(t *testing.T) {
	trace := &RelayDebugTrace{
		Outcome: "failed",
		Client:  RelayDebugClientRequest{Method: http.MethodPost, Path: "/v1/chat/completions"},
		Attempts: []*RelayDebugAttempt{
			{
				Index: 1, Stage: "relay", ChannelId: 10, ChannelName: "first",
				Error:    &RelayDebugError{StatusCode: 503, UpstreamStatusCode: 503, Type: "api_error", Code: "busy", Message: "busy"},
				Decision: RelayDebugDecision{Action: "switch_channel", Reason: "status_code_match"},
			},
			{
				Index: 2, Stage: "relay", ChannelId: 11, ChannelName: "second",
				Error:    &RelayDebugError{StatusCode: 503, UpstreamStatusCode: 503, Type: "api_error", Code: "busy", Message: "busy"},
				Decision: RelayDebugDecision{Action: "stop", Reason: "budget_exhausted"},
			},
		},
	}

	summary := buildRelayRetrySummary(trace)

	assert.Equal(t, 2, summary.AttemptCount)
	assert.Equal(t, 2, summary.FailureCount)
	assert.Equal(t, 1, summary.UniqueErrorCount)
	require.Len(t, summary.Errors, 1)
	require.Len(t, summary.Errors[0].Occurrences, 2)
	assert.Equal(t, 10, summary.Errors[0].Occurrences[0].ChannelId)
	assert.Equal(t, "switch_channel", summary.Errors[0].Occurrences[0].Action)
	assert.Equal(t, 11, summary.Errors[0].Occurrences[1].ChannelId)
	assert.Equal(t, "budget_exhausted", summary.Errors[0].Occurrences[1].Reason)
}

func TestRelayDebugBodyTruncationAndTraceBudget(t *testing.T) {
	original := strings.Repeat("head", 40) + strings.Repeat("tail", 40)
	body := finalizeRelayDebugBody(&RelayDebugBody{Kind: "text", Text: original}, 64)

	assert.True(t, body.Truncated)
	assert.LessOrEqual(t, len(body.Text), 64)
	assert.Equal(t, int64(len(original)), body.OriginalLength)
	assert.Len(t, body.SHA256, 64)
	assert.Contains(t, body.Text, "...[TRUNCATED]...")

	trace := &RelayDebugTrace{
		Client: RelayDebugClientRequest{Body: &RelayDebugBody{Kind: "text", Text: strings.Repeat("a", 100)}},
		Attempts: []*RelayDebugAttempt{{
			Exchanges: []RelayDebugExchange{{
				Request:  RelayDebugHTTPMessage{Body: &RelayDebugBody{Kind: "text", Text: strings.Repeat("b", 100)}},
				Response: RelayDebugHTTPMessage{Body: &RelayDebugBody{Kind: "text", Text: strings.Repeat("c", 100)}},
			}},
		}},
	}
	enforceRelayDebugTextBudget(trace, 90)

	total := len(trace.Client.Body.Text) + len(trace.Attempts[0].Exchanges[0].Request.Body.Text) + len(trace.Attempts[0].Exchanges[0].Response.Body.Text)
	assert.LessOrEqual(t, total, 90)
	assert.True(t, trace.Client.Body.Truncated)
	assert.True(t, trace.Attempts[0].Exchanges[0].Request.Body.Truncated)
	assert.True(t, trace.Attempts[0].Exchanges[0].Response.Body.Truncated)
}

func TestRelayDebugRecordersShareCaptureBudget(t *testing.T) {
	budget := &relayDebugCaptureBudget{remaining: 8}
	first := newRelayDebugBodyRecorder(budget, 8)
	second := newRelayDebugBodyRecorder(budget, 8)

	_, err := first.Write([]byte("123456"))
	require.NoError(t, err)
	_, err = second.Write([]byte("abcd"))
	require.NoError(t, err)

	firstBody := first.snapshot("text/plain")
	secondBody := second.snapshot("text/plain")
	require.NotNil(t, firstBody)
	require.NotNil(t, secondBody)
	assert.Equal(t, "123456", firstBody.Text)
	assert.Equal(t, "omitted", secondBody.Kind)
	assert.Equal(t, "body_exceeds_debug_limit", secondBody.OmittedReason)
	assert.Zero(t, second.buffer.Cap(), "overflowed recorder must release its capture buffer")
	assert.Zero(t, budget.remaining)
}

func TestRecoveredRelayDebugAttemptRetainsSanitizedUpstreamRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousEnabled := common.RelayDebugLogEnabled
	previousLimit := common.RelayDebugLogTextLimitMB
	common.RelayDebugLogEnabled = true
	common.RelayDebugLogTextLimitMB = 1
	t.Cleanup(func() {
		common.RelayDebugLogEnabled = previousEnabled
		common.RelayDebugLogTextLimitMB = previousLimit
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	StartRelayDebug(context)

	BeginRelayDebugAttempt(context, "relay", RelayDebugAttemptMeta{ChannelId: 1})
	firstRequest := httptest.NewRequest(http.MethodPost, "https://upstream.test/v1/chat/completions", strings.NewReader(`{"model":"test","api_key":"secret"}`))
	firstRequest.Header.Set("Content-Type", "application/json")
	CaptureRelayDebugHTTPRequest(context, firstRequest)
	_, err := ioReadAllAndClose(firstRequest.Body)
	require.NoError(t, err)
	firstError := types.NewErrorWithStatusCode(errors.New("busy"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	CompleteRelayDebugAttempt(context, firstError)

	BeginRelayDebugAttempt(context, "relay", RelayDebugAttemptMeta{ChannelId: 2})
	secondRequest := httptest.NewRequest(http.MethodPost, "https://upstream.test/v1/chat/completions", strings.NewReader(`{"model":"test","authorization":"secret"}`))
	secondRequest.Header.Set("Content-Type", "application/json")
	CaptureRelayDebugHTTPRequest(context, secondRequest)
	_, err = ioReadAllAndClose(secondRequest.Body)
	require.NoError(t, err)
	CompleteRelayDebugAttempt(context, nil)

	collector := relayDebugFromContext(context)
	require.NotNil(t, collector)
	require.Len(t, collector.trace.Attempts, 2)
	recoveredRequest := collector.trace.Attempts[1].Exchanges[0].Request.Body
	require.NotNil(t, recoveredRequest)
	assert.Contains(t, recoveredRequest.Text, relayDebugRedactedValue)
	assert.NotContains(t, recoveredRequest.Text, "secret")
}

func ioReadAllAndClose(body io.ReadCloser) ([]byte, error) {
	data, err := io.ReadAll(body)
	closeErr := body.Close()
	if err != nil {
		return nil, err
	}
	return data, closeErr
}
