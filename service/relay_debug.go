package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	relayDebugContextKey    = "relay_debug_collector"
	relayDebugPreviewBytes  = 2 * 1024
	relayDebugRedactedValue = "[REDACTED]"
	relayDebugBase64Value   = "[OMITTED_BASE64]"
)

type RelayDebugAttemptMeta struct {
	ChannelId     int
	ChannelName   string
	ChannelType   int
	MultiKey      bool
	MultiKeyIndex int
}

type RelayDebugDecision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

type RelayDebugBody struct {
	Kind           string `json:"kind"`
	ContentType    string `json:"content_type,omitempty"`
	Text           string `json:"text,omitempty"`
	Size           int64  `json:"size"`
	OriginalLength int64  `json:"original_length,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	OmittedReason  string `json:"omitted_reason,omitempty"`
}

type RelayDebugHTTPMessage struct {
	Method   string              `json:"method,omitempty"`
	URL      string              `json:"url,omitempty"`
	Status   int                 `json:"status,omitempty"`
	Headers  map[string][]string `json:"headers,omitempty"`
	Body     *RelayDebugBody     `json:"body,omitempty"`
	recorder *relayDebugBodyRecorder
}

type RelayDebugExchange struct {
	Index    int                   `json:"index"`
	Request  RelayDebugHTTPMessage `json:"request"`
	Response RelayDebugHTTPMessage `json:"response"`
}

type RelayDebugError struct {
	StatusCode         int             `json:"status_code"`
	UpstreamStatusCode int             `json:"upstream_status_code,omitempty"`
	Type               string          `json:"type,omitempty"`
	Code               string          `json:"code,omitempty"`
	Message            string          `json:"message"`
	Local              bool            `json:"local,omitempty"`
	Response           *RelayDebugBody `json:"response,omitempty"`
}

type RelayDebugAttempt struct {
	Index         int                  `json:"index"`
	Stage         string               `json:"stage"`
	ChannelId     int                  `json:"channel_id,omitempty"`
	ChannelName   string               `json:"channel_name,omitempty"`
	ChannelType   int                  `json:"channel_type,omitempty"`
	MultiKeyIndex *int                 `json:"multi_key_index,omitempty"`
	StartedAt     int64                `json:"started_at"`
	DurationMs    int64                `json:"duration_ms"`
	Succeeded     bool                 `json:"succeeded"`
	Exchanges     []RelayDebugExchange `json:"exchanges,omitempty"`
	Error         *RelayDebugError     `json:"error,omitempty"`
	Decision      RelayDebugDecision   `json:"decision"`
}

type RelayDebugClientRequest struct {
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	URL         string              `json:"url"`
	ContentType string              `json:"content_type,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Body        *RelayDebugBody     `json:"body,omitempty"`
	BodySize    int64               `json:"body_size"`
}

type RelayDebugTrace struct {
	Version     int                     `json:"version"`
	RequestId   string                  `json:"request_id"`
	Outcome     string                  `json:"outcome"`
	CreatedAt   int64                   `json:"created_at"`
	FinalizedAt int64                   `json:"finalized_at"`
	Client      RelayDebugClientRequest `json:"client"`
	Attempts    []*RelayDebugAttempt    `json:"attempts"`
}

type RelayRetryOccurrence struct {
	AttemptIndex  int    `json:"attempt_index"`
	Stage         string `json:"stage"`
	ChannelId     int    `json:"channel_id,omitempty"`
	ChannelName   string `json:"channel_name,omitempty"`
	ChannelType   int    `json:"channel_type,omitempty"`
	MultiKeyIndex *int   `json:"multi_key_index,omitempty"`
	Action        string `json:"action"`
	Reason        string `json:"reason"`
}

type RelayRetryErrorSummary struct {
	StatusCode         int                    `json:"status_code"`
	UpstreamStatusCode int                    `json:"upstream_status_code,omitempty"`
	Type               string                 `json:"type,omitempty"`
	Code               string                 `json:"code,omitempty"`
	Message            string                 `json:"message"`
	ResponsePreview    string                 `json:"response_preview,omitempty"`
	Occurrences        []RelayRetryOccurrence `json:"occurrences"`
}

type RelayRetrySummary struct {
	Version          int                      `json:"version"`
	Outcome          string                   `json:"outcome"`
	Method           string                   `json:"method"`
	Path             string                   `json:"path"`
	ContentType      string                   `json:"content_type,omitempty"`
	BodySize         int64                    `json:"body_size"`
	AttemptCount     int                      `json:"attempt_count"`
	FailureCount     int                      `json:"failure_count"`
	UniqueErrorCount int                      `json:"unique_error_count"`
	TraceAvailable   bool                     `json:"trace_available"`
	Errors           []RelayRetryErrorSummary `json:"errors"`
}

type relayDebugCollector struct {
	mu           sync.Mutex
	trace        RelayDebugTrace
	current      *RelayDebugAttempt
	failureCount int
	textLimit    int
	finalized    bool
	summary      *RelayRetrySummary
}

type relayDebugBodyRecorder struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	size     int64
	overflow bool
}

type relayDebugReadCloser struct {
	io.ReadCloser
	recorder *relayDebugBodyRecorder
}

func (r *relayDebugReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		_, _ = r.recorder.Write(p[:n])
	}
	return n, err
}

func newRelayDebugBodyRecorder(limit int) *relayDebugBodyRecorder {
	return &relayDebugBodyRecorder{limit: limit}
}

func (r *relayDebugBodyRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.size += int64(len(p))
	remaining := r.limit - r.buffer.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = r.buffer.Write(p[:remaining])
	}
	if r.buffer.Len() < int(r.size) {
		r.overflow = true
	}
	return len(p), nil
}

func (r *relayDebugBodyRecorder) snapshot(contentType string) *RelayDebugBody {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	data := append([]byte(nil), r.buffer.Bytes()...)
	size := r.size
	overflow := r.overflow
	r.mu.Unlock()
	if overflow {
		return &RelayDebugBody{
			Kind:           "omitted",
			ContentType:    contentType,
			Size:           size,
			OriginalLength: size,
			Truncated:      true,
			OmittedReason:  "body_exceeds_debug_limit",
		}
	}
	return sanitizeRelayDebugBody(data, contentType, size, r.limit)
}

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)(authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|token|secret|password|passwd|credential|signature|sig)\s*[:=]\s*([^\s,;]+)`)
	bearerPattern           = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	jwtPattern              = regexp.MustCompile(`\b[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{8,}\b`)
	base64TokenPattern      = regexp.MustCompile(`[A-Za-z0-9+/_-]{64,}={0,2}`)
	urlPattern              = regexp.MustCompile(`https?://[^\s"'<>]+`)
)

func StartRelayDebug(c *gin.Context) {
	if c == nil || !common.RelayDebugLogEnabled || c.Request == nil || c.Request.URL == nil {
		return
	}
	if _, exists := c.Get(relayDebugContextKey); exists {
		return
	}
	limitMB := common.RelayDebugLogTextLimitMB
	if limitMB < 1 || limitMB > 128 {
		limitMB = 16
	}
	collector := &relayDebugCollector{
		textLimit: limitMB << 20,
		trace: RelayDebugTrace{
			Version:   1,
			RequestId: c.GetString(common.RequestIdKey),
			CreatedAt: time.Now().UnixMilli(),
			Client: RelayDebugClientRequest{
				Method:      c.Request.Method,
				Path:        c.Request.URL.Path,
				URL:         relaycommon.SanitizeURLForLog(c.Request.URL.String()),
				ContentType: c.Request.Header.Get("Content-Type"),
				Headers:     sanitizeRelayDebugHeaders(c.Request.Header),
				BodySize:    c.Request.ContentLength,
			},
		},
	}
	if storage, err := common.GetBodyStorage(c); err == nil {
		collector.trace.Client.BodySize = storage.Size()
		if _, err := storage.Seek(0, io.SeekStart); err == nil {
			recorder := newRelayDebugBodyRecorder(collector.textLimit)
			_, _ = io.Copy(recorder, storage)
			collector.trace.Client.Body = recorder.snapshot(collector.trace.Client.ContentType)
			_, _ = storage.Seek(0, io.SeekStart)
			c.Request.Body = io.NopCloser(storage)
		}
	}
	c.Set(relayDebugContextKey, collector)
}

func relayDebugFromContext(c *gin.Context) *relayDebugCollector {
	if c == nil {
		return nil
	}
	value, exists := c.Get(relayDebugContextKey)
	if !exists {
		return nil
	}
	collector, _ := value.(*relayDebugCollector)
	return collector
}

func BeginRelayDebugAttempt(c *gin.Context, stage string, meta RelayDebugAttemptMeta) {
	collector := relayDebugFromContext(c)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.finalized {
		return
	}
	attempt := &RelayDebugAttempt{
		Index:       len(collector.trace.Attempts) + 1,
		Stage:       stage,
		ChannelId:   meta.ChannelId,
		ChannelName: meta.ChannelName,
		ChannelType: meta.ChannelType,
		StartedAt:   time.Now().UnixMilli(),
	}
	if meta.MultiKey {
		index := meta.MultiKeyIndex
		attempt.MultiKeyIndex = &index
	}
	collector.trace.Attempts = append(collector.trace.Attempts, attempt)
	collector.current = attempt
}

func CaptureRelayDebugHTTPRequest(c *gin.Context, req *http.Request) {
	collector := relayDebugFromContext(c)
	if collector == nil || req == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.current == nil || collector.finalized {
		return
	}
	exchange := RelayDebugExchange{
		Index: len(collector.current.Exchanges) + 1,
		Request: RelayDebugHTTPMessage{
			Method:  req.Method,
			Headers: sanitizeRelayDebugHeaders(req.Header),
		},
	}
	if req.URL != nil {
		exchange.Request.URL = relaycommon.SanitizeURLForLog(req.URL.String())
	}
	if req.Body != nil {
		recorder := newRelayDebugBodyRecorder(collector.textLimit)
		exchange.Request.recorder = recorder
		req.Body = &relayDebugReadCloser{ReadCloser: req.Body, recorder: recorder}
	}
	collector.current.Exchanges = append(collector.current.Exchanges, exchange)
}

func CaptureRelayDebugHTTPResponse(c *gin.Context, resp *http.Response) {
	collector := relayDebugFromContext(c)
	if collector == nil || resp == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.current == nil || len(collector.current.Exchanges) == 0 || collector.finalized {
		return
	}
	exchange := &collector.current.Exchanges[len(collector.current.Exchanges)-1]
	exchange.Response.Status = resp.StatusCode
	exchange.Response.Headers = sanitizeRelayDebugHeaders(resp.Header)
	shouldCaptureBody := resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !common.GetContextKeyBool(c, constant.ContextKeyIsStream)
	if resp.Body != nil && shouldCaptureBody {
		recorder := newRelayDebugBodyRecorder(collector.textLimit)
		exchange.Response.recorder = recorder
		resp.Body = &relayDebugReadCloser{ReadCloser: resp.Body, recorder: recorder}
	}
}

func CompleteRelayDebugAttempt(c *gin.Context, relayErr *types.NewAPIError) {
	collector := relayDebugFromContext(c)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	attempt := collector.current
	if attempt == nil || collector.finalized {
		return
	}
	finalizeRelayDebugExchanges(attempt)
	attempt.DurationMs = time.Now().UnixMilli() - attempt.StartedAt
	if relayErr == nil {
		attempt.Succeeded = true
		if collector.failureCount > 0 {
			collector.trace.Outcome = "recovered"
			for index := range attempt.Exchanges {
				attempt.Exchanges[index].Response.Body = nil
			}
		} else {
			collector.trace.Outcome = "success"
		}
		collector.current = nil
		return
	}

	attempt.Error = relayDebugErrorFromAPIError(relayErr, collector.textLimit)
	if responseBody := lastRelayDebugResponseBody(attempt); responseBody != nil {
		attempt.Error.Response = responseBody
	}
	collector.failureCount++
	collector.trace.Outcome = "failed"
	collector.current = nil
}

func CompleteRelayDebugTaskAttempt(c *gin.Context, taskErr *dto.TaskError) {
	collector := relayDebugFromContext(c)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	attempt := collector.current
	if attempt == nil || collector.finalized {
		return
	}
	finalizeRelayDebugExchanges(attempt)
	attempt.DurationMs = time.Now().UnixMilli() - attempt.StartedAt
	if taskErr == nil {
		attempt.Succeeded = true
		if collector.failureCount > 0 {
			collector.trace.Outcome = "recovered"
			for index := range attempt.Exchanges {
				attempt.Exchanges[index].Response.Body = nil
			}
		} else {
			collector.trace.Outcome = "success"
		}
		collector.current = nil
		return
	}
	message := taskErr.Message
	if taskErr.Error != nil {
		message = taskErr.Error.Error()
	}
	attempt.Error = &RelayDebugError{
		StatusCode:         taskErr.StatusCode,
		UpstreamStatusCode: taskErr.StatusCode,
		Type:               "task_error",
		Code:               taskErr.Code,
		Message:            relayDebugTextPreview(sanitizeRelayDebugText(message), relayDebugPreviewBytes),
		Local:              taskErr.LocalError,
		Response:           lastRelayDebugResponseBody(attempt),
	}
	collector.failureCount++
	collector.trace.Outcome = "failed"
	collector.current = nil
}

func RecordRelayDebugStageError(c *gin.Context, stage string, meta RelayDebugAttemptMeta, relayErr *types.NewAPIError, decision RelayDebugDecision) {
	if relayErr == nil {
		return
	}
	BeginRelayDebugAttempt(c, stage, meta)
	CompleteRelayDebugAttempt(c, relayErr)
	SetRelayDebugDecision(c, decision)
}

func SetRelayDebugDecision(c *gin.Context, decision RelayDebugDecision) {
	collector := relayDebugFromContext(c)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for index := len(collector.trace.Attempts) - 1; index >= 0; index-- {
		if collector.trace.Attempts[index].Error != nil {
			collector.trace.Attempts[index].Decision = decision
			return
		}
	}
}

func HasRelayDebugFailures(c *gin.Context) bool {
	collector := relayDebugFromContext(c)
	if collector == nil {
		return false
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.failureCount > 0
}

func AppendRelayDebugAdminInfo(c *gin.Context, other map[string]interface{}) {
	collector := relayDebugFromContext(c)
	if collector == nil || other == nil {
		return
	}
	collector.mu.Lock()
	if collector.current != nil && !collector.finalized {
		attempt := collector.current
		finalizeRelayDebugExchanges(attempt)
		attempt.DurationMs = time.Now().UnixMilli() - attempt.StartedAt
		attempt.Succeeded = true
		if collector.failureCount > 0 {
			collector.trace.Outcome = "recovered"
		} else {
			collector.trace.Outcome = "success"
		}
		for index := range attempt.Exchanges {
			attempt.Exchanges[index].Response.Body = nil
		}
		collector.current = nil
	}
	collector.mu.Unlock()
	summary := finalizeRelayDebug(c, collector)
	if summary == nil {
		return
	}
	adminInfo, ok := other["admin_info"].(map[string]interface{})
	if !ok || adminInfo == nil {
		adminInfo = map[string]interface{}{}
		other["admin_info"] = adminInfo
	}
	adminInfo["relay_retry"] = summary
}

func finalizeRelayDebug(c *gin.Context, collector *relayDebugCollector) *RelayRetrySummary {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.finalized {
		return collector.summary
	}
	if collector.failureCount == 0 {
		collector.finalized = true
		return nil
	}
	if collector.trace.Outcome == "failed" {
		last := collector.trace.Attempts[len(collector.trace.Attempts)-1]
		if last.Succeeded {
			collector.trace.Outcome = "recovered"
		}
	}
	collector.trace.FinalizedAt = time.Now().UnixMilli()
	summary := buildRelayRetrySummary(&collector.trace)
	enforceRelayDebugTextBudget(&collector.trace, collector.textLimit)
	payload, err := common.Marshal(&collector.trace)
	if err == nil {
		err = model.SaveRelayDebugPayload(c.Request.Context(), collector.trace.RequestId, payload)
	}
	if err != nil {
		logger.LogWarn(c, fmt.Sprintf("failed to persist relay debug trace: request_id=%s error=%s", collector.trace.RequestId, err.Error()))
	} else {
		summary.TraceAvailable = true
	}
	collector.summary = summary
	collector.finalized = true
	return summary
}

func finalizeRelayDebugExchanges(attempt *RelayDebugAttempt) {
	for index := range attempt.Exchanges {
		exchange := &attempt.Exchanges[index]
		requestContentType := firstHeaderValue(exchange.Request.Headers, "Content-Type")
		responseContentType := firstHeaderValue(exchange.Response.Headers, "Content-Type")
		exchange.Request.Body = exchange.Request.recorder.snapshot(requestContentType)
		exchange.Response.Body = exchange.Response.recorder.snapshot(responseContentType)
		exchange.Request.recorder = nil
		exchange.Response.recorder = nil
	}
}

func relayDebugErrorFromAPIError(relayErr *types.NewAPIError, limit int) *RelayDebugError {
	result := &RelayDebugError{
		StatusCode:         relayErr.StatusCode,
		UpstreamStatusCode: relayErr.GetUpstreamStatusCode(),
		Type:               string(relayErr.GetErrorType()),
		Code:               string(relayErr.GetErrorCode()),
		Message:            relayDebugTextPreview(sanitizeRelayDebugText(relayErr.Error()), relayDebugPreviewBytes),
	}
	if response := relayErr.GetUpstreamResponse(); response != "" {
		result.Response = sanitizeRelayDebugBody([]byte(response), "application/json", int64(len(response)), limit)
	}
	return result
}

func lastRelayDebugResponseBody(attempt *RelayDebugAttempt) *RelayDebugBody {
	for index := len(attempt.Exchanges) - 1; index >= 0; index-- {
		if attempt.Exchanges[index].Response.Body != nil {
			return attempt.Exchanges[index].Response.Body
		}
	}
	return nil
}

func buildRelayRetrySummary(trace *RelayDebugTrace) *RelayRetrySummary {
	summary := &RelayRetrySummary{
		Version:      1,
		Outcome:      trace.Outcome,
		Method:       trace.Client.Method,
		Path:         trace.Client.Path,
		ContentType:  trace.Client.ContentType,
		BodySize:     trace.Client.BodySize,
		AttemptCount: len(trace.Attempts),
	}
	errorIndexes := make(map[string]int)
	for _, attempt := range trace.Attempts {
		if attempt.Error == nil {
			continue
		}
		summary.FailureCount++
		responseText := ""
		responseHash := ""
		if attempt.Error.Response != nil {
			responseText = attempt.Error.Response.Text
			responseHash = attempt.Error.Response.SHA256
		}
		fingerprintValue := map[string]interface{}{
			"status":          attempt.Error.StatusCode,
			"upstream_status": attempt.Error.UpstreamStatusCode,
			"type":            attempt.Error.Type,
			"code":            attempt.Error.Code,
			"message":         attempt.Error.Message,
			"response":        responseText,
			"response_hash":   responseHash,
		}
		fingerprintJSON, _ := common.Marshal(fingerprintValue)
		fingerprint := fmt.Sprintf("%x", sha256.Sum256(fingerprintJSON))
		occurrence := RelayRetryOccurrence{
			AttemptIndex:  attempt.Index,
			Stage:         attempt.Stage,
			ChannelId:     attempt.ChannelId,
			ChannelName:   attempt.ChannelName,
			ChannelType:   attempt.ChannelType,
			MultiKeyIndex: attempt.MultiKeyIndex,
			Action:        attempt.Decision.Action,
			Reason:        attempt.Decision.Reason,
		}
		if existing, ok := errorIndexes[fingerprint]; ok {
			summary.Errors[existing].Occurrences = append(summary.Errors[existing].Occurrences, occurrence)
			continue
		}
		preview := responseText
		if len(preview) > relayDebugPreviewBytes {
			preview = strings.ToValidUTF8(preview[:relayDebugPreviewBytes], "\uFFFD") + "..."
		}
		errorIndexes[fingerprint] = len(summary.Errors)
		summary.Errors = append(summary.Errors, RelayRetryErrorSummary{
			StatusCode:         attempt.Error.StatusCode,
			UpstreamStatusCode: attempt.Error.UpstreamStatusCode,
			Type:               attempt.Error.Type,
			Code:               attempt.Error.Code,
			Message:            attempt.Error.Message,
			ResponsePreview:    preview,
			Occurrences:        []RelayRetryOccurrence{occurrence},
		})
	}
	summary.UniqueErrorCount = len(summary.Errors)
	return summary
}

func sanitizeRelayDebugHeaders(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string][]string, len(headers))
	for name, values := range headers {
		if isRelayDebugSecretName(name) || strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Set-Cookie") {
			result[name] = []string{relayDebugRedactedValue}
			continue
		}
		cleanValues := make([]string, 0, len(values))
		for _, value := range values {
			cleanValues = append(cleanValues, sanitizeRelayDebugText(value))
		}
		result[name] = cleanValues
	}
	return result
}

func firstHeaderValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func sanitizeRelayDebugBody(data []byte, contentType string, size int64, limit int) *RelayDebugBody {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if mediaType == "" {
		mediaType = contentType
	}
	result := &RelayDebugBody{ContentType: contentType, Size: size, OriginalLength: size}
	if len(data) == 0 {
		result.Kind = "empty"
		return result
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			result.Kind = "omitted"
			result.OmittedReason = "invalid_multipart_boundary"
			return result
		}
		result.Kind = "multipart"
		result.Text = sanitizeRelayDebugMultipart(data, boundary)
		return finalizeRelayDebugBody(result, limit)
	}
	if strings.Contains(mediaType, "json") || looksLikeJSON(data) {
		var value any
		if err := common.Unmarshal(data, &value); err == nil {
			value = sanitizeRelayDebugValue(value, "$", "")
			canonical, marshalErr := common.Marshal(value)
			if marshalErr == nil {
				result.Kind = "json"
				result.Text = string(canonical)
				return finalizeRelayDebugBody(result, limit)
			}
		}
	}
	if mediaType == "application/x-www-form-urlencoded" {
		values, err := url.ParseQuery(string(data))
		if err == nil {
			for key := range values {
				if isRelayDebugSecretName(key) {
					values[key] = []string{relayDebugRedactedValue}
					continue
				}
				for index := range values[key] {
					values[key][index] = sanitizeRelayDebugText(values[key][index])
				}
			}
			result.Kind = "form"
			result.Text = values.Encode()
			return finalizeRelayDebugBody(result, limit)
		}
	}
	if mediaType != "" && !strings.HasPrefix(mediaType, "text/") && mediaType != "application/xml" && mediaType != "application/graphql" {
		result.Kind = "omitted"
		result.OmittedReason = "binary_body"
		return result
	}
	result.Kind = "text"
	result.Text = sanitizeRelayDebugText(string(data))
	return finalizeRelayDebugBody(result, limit)
}

func finalizeRelayDebugBody(body *RelayDebugBody, limit int) *RelayDebugBody {
	if body.OriginalLength == 0 {
		body.OriginalLength = int64(len(body.Text))
	}
	if body.SHA256 == "" {
		body.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(body.Text)))
	}
	if limit <= 0 || len(body.Text) <= limit {
		return body
	}
	marker := "\n...[TRUNCATED]...\n"
	if limit <= len(marker) {
		body.Kind = "omitted"
		body.Text = ""
		body.Truncated = true
		body.OmittedReason = "trace_text_budget_exceeded"
		return body
	}
	retainedSize := limit - len(marker)
	headSize := retainedSize / 2
	tailSize := retainedSize - headSize
	head := strings.ToValidUTF8(body.Text[:headSize], "\uFFFD")
	tail := strings.ToValidUTF8(body.Text[len(body.Text)-tailSize:], "\uFFFD")
	body.Text = head + marker + tail
	body.Truncated = true
	return body
}

func enforceRelayDebugTextBudget(trace *RelayDebugTrace, limit int) {
	if trace == nil || limit <= 0 {
		return
	}
	bodies := make([]*RelayDebugBody, 0)
	seen := make(map[*RelayDebugBody]struct{})
	addBody := func(body *RelayDebugBody) {
		if body == nil || body.Text == "" {
			return
		}
		if _, ok := seen[body]; ok {
			return
		}
		seen[body] = struct{}{}
		bodies = append(bodies, body)
	}
	addBody(trace.Client.Body)
	for _, attempt := range trace.Attempts {
		for index := range attempt.Exchanges {
			addBody(attempt.Exchanges[index].Request.Body)
			addBody(attempt.Exchanges[index].Response.Body)
		}
		if attempt.Error != nil {
			addBody(attempt.Error.Response)
		}
	}
	sort.Slice(bodies, func(i, j int) bool {
		return len(bodies[i].Text) < len(bodies[j].Text)
	})
	remaining := limit
	for index, body := range bodies {
		share := remaining / (len(bodies) - index)
		if len(body.Text) <= share {
			remaining -= len(body.Text)
			continue
		}
		for _, oversized := range bodies[index:] {
			finalizeRelayDebugBody(oversized, share)
		}
		return
	}
}

func sanitizeRelayDebugMultipart(data []byte, boundary string) string {
	reader := multipart.NewReader(bytes.NewReader(data), boundary)
	parts := make([]map[string]interface{}, 0)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			parts = append(parts, map[string]interface{}{"omitted_reason": "invalid_multipart_body"})
			break
		}
		fieldPath := part.FormName()
		partType := part.Header.Get("Content-Type")
		if part.FileName() != "" || (partType != "" && !strings.HasPrefix(partType, "text/") && !strings.Contains(partType, "json")) {
			byteSize, _ := io.Copy(io.Discard, part)
			parts = append(parts, map[string]interface{}{
				"field_path": fieldPath,
				"filename":   sanitizeRelayDebugText(part.FileName()),
				"mime_type":  partType,
				"byte_size":  byteSize,
				"omitted":    "media",
			})
			continue
		}
		partData, _ := io.ReadAll(part)
		if placeholder, ok := relayDebugMediaPlaceholder(string(partData), "$multipart."+fieldPath, fieldPath); ok {
			parts = append(parts, placeholder)
			continue
		}
		value := sanitizeRelayDebugText(string(partData))
		if isRelayDebugSecretName(fieldPath) {
			value = relayDebugRedactedValue
		}
		parts = append(parts, map[string]interface{}{"field_path": fieldPath, "value": value})
	}
	encoded, _ := common.Marshal(parts)
	return string(encoded)
}

func sanitizeRelayDebugValue(value any, path string, key string) any {
	if isRelayDebugSecretName(key) {
		return relayDebugRedactedValue
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]interface{}, len(typed))
		for _, childKey := range keys {
			result[childKey] = sanitizeRelayDebugValue(typed[childKey], path+"."+childKey, childKey)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index := range typed {
			result[index] = sanitizeRelayDebugValue(typed[index], fmt.Sprintf("%s[%d]", path, index), key)
		}
		return result
	case string:
		if placeholder, ok := relayDebugMediaPlaceholder(typed, path, key); ok {
			return placeholder
		}
		return sanitizeRelayDebugText(typed)
	default:
		return value
	}
}

func relayDebugMediaPlaceholder(value string, path string, key string) (map[string]interface{}, bool) {
	lowerKey := strings.ToLower(key)
	if strings.HasPrefix(value, "data:") {
		separator := strings.Index(value, ",")
		if separator > 5 && strings.Contains(strings.ToLower(value[:separator]), ";base64") {
			mimeType := strings.Split(strings.TrimPrefix(value[:separator], "data:"), ";")[0]
			decodedSize, _, ok := relayDebugBase64Metadata(value[separator+1:], 4)
			if !ok {
				decodedSize = base64.StdEncoding.DecodedLen(len(value) - separator - 1)
			}
			return map[string]interface{}{"field_path": path, "filename": "", "mime_type": mimeType, "byte_size": decodedSize, "omitted": "media"}, true
		}
	}
	knownMediaKey := lowerKey == "b64_json" || lowerKey == "base64" || lowerKey == "image" || lowerKey == "audio" || lowerKey == "video" || lowerKey == "file"
	minimumLength := 64
	if knownMediaKey {
		minimumLength = 16
	}
	decodedSize, mimeType, ok := relayDebugBase64Metadata(value, minimumLength)
	if !ok {
		return nil, false
	}
	omitted := "base64"
	if knownMediaKey || strings.HasPrefix(mimeType, "image/") || strings.HasPrefix(mimeType, "audio/") || strings.HasPrefix(mimeType, "video/") {
		omitted = "media"
	}
	return map[string]interface{}{"field_path": path, "filename": "", "mime_type": mimeType, "byte_size": decodedSize, "omitted": omitted}, true
}

func relayDebugBase64Metadata(value string, minimumLength int) (int, string, bool) {
	if len(value) < minimumLength || len(value)%4 == 1 {
		return 0, "", false
	}
	padding := 0
	urlAlphabet := false
	standardAlphabet := false
	for index, char := range []byte(value) {
		if padding > 0 && char != '=' {
			return 0, "", false
		}
		switch {
		case char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z', char >= '0' && char <= '9':
		case char == '_' || char == '-':
			urlAlphabet = true
			if standardAlphabet {
				return 0, "", false
			}
		case char == '+' || char == '/':
			standardAlphabet = true
			if urlAlphabet {
				return 0, "", false
			}
		case char == '=':
			padding++
			if index < len(value)-2 || padding > 2 {
				return 0, "", false
			}
		default:
			return 0, "", false
		}
	}
	if padding > 0 && len(value)%4 != 0 {
		return 0, "", false
	}
	decodedSize := len(value) * 6 / 8
	if padding > 0 {
		decodedSize = len(value)/4*3 - padding
	}
	prefixLength := len(value) - padding
	if prefixLength > 684 {
		prefixLength = 684
	}
	prefixLength -= prefixLength % 4
	if prefixLength == 0 {
		return 0, "", false
	}
	encoding := base64.RawStdEncoding
	if urlAlphabet {
		encoding = base64.RawURLEncoding
	}
	decodedPrefix, err := encoding.DecodeString(value[:prefixLength])
	if err != nil {
		return 0, "", false
	}
	return decodedSize, http.DetectContentType(decodedPrefix), true
}

func isRelayDebugSecretName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	switch normalized {
	case "authorization", "proxy-authorization", "auth", "x-auth", "cookie", "set-cookie", "key", "api_key", "api-key", "apikey", "x-api-key", "x-goog-api-key", "access_key", "access-key", "private_key", "private-key", "secret_key", "secret-key", "client_key", "client-key", "password", "passwd", "client_secret", "secret", "signature", "sig", "credential", "awsaccesskeyid", "x-amz-credential", "x-amz-security-token", "x-amz-signature":
		return true
	}
	return strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "signature") || strings.HasSuffix(normalized, "password") || strings.HasSuffix(normalized, "credential")
}

func sanitizeRelayDebugText(value string) string {
	if value == "" {
		return value
	}
	value = bearerPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := strings.Fields(match)
		if len(parts) == 0 {
			return relayDebugRedactedValue
		}
		return parts[0] + " " + relayDebugRedactedValue
	})
	value = jwtPattern.ReplaceAllString(value, relayDebugRedactedValue)
	value = base64TokenPattern.ReplaceAllString(value, relayDebugBase64Value)
	value = secretAssignmentPattern.ReplaceAllStringFunc(value, func(match string) string {
		separator := strings.IndexAny(match, ":=")
		if separator < 0 {
			return relayDebugRedactedValue
		}
		return match[:separator+1] + relayDebugRedactedValue
	})
	value = urlPattern.ReplaceAllStringFunc(value, relaycommon.SanitizeURLForLog)
	return value
}

func relayDebugTextPreview(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "\uFFFD") + "..."
}

func looksLikeJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}
