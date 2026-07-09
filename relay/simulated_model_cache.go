package relay

import (
	"bytes"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type simulatedModelCacheAttempt struct {
	settings           dto.SimulatedModelCacheSettings
	requestBody        []byte
	promptText         string
	finalRequestFormat types.RelayFormat
	upstreamModelName  string
	startedAt          time.Time
}

type simulatedModelCacheRecorder struct {
	gin.ResponseWriter
	body                bytes.Buffer
	streamPending       bytes.Buffer
	streamDelaysMS      []int64
	lastStreamEventAt   time.Time
	lastStreamPendingAt time.Time
	status              int
	size                int
	written             bool
	passThrough         bool
}

func (w *simulatedModelCacheRecorder) WriteHeader(code int) {
	if w.written {
		return
	}
	code = normalizeSimulatedModelCacheStatusCode(code)
	w.status = code
	w.written = true
	if w.passThrough {
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *simulatedModelCacheRecorder) WriteHeaderNow() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
}

func (w *simulatedModelCacheRecorder) Write(data []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.body.Write(data)
	w.size += n
	if w.passThrough && n > 0 {
		w.recordStreamWrite(data[:n])
	}
	if err != nil || !w.passThrough {
		return n, err
	}
	written, writeErr := w.ResponseWriter.Write(data)
	if written < n {
		n = written
	}
	return n, writeErr
}

func (w *simulatedModelCacheRecorder) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *simulatedModelCacheRecorder) Status() int {
	if w.status < http.StatusContinue {
		return http.StatusOK
	}
	return w.status
}

func (w *simulatedModelCacheRecorder) Size() int {
	return w.size
}

func (w *simulatedModelCacheRecorder) Written() bool {
	return w.written
}

func (w *simulatedModelCacheRecorder) Flush() {
	if w.passThrough {
		w.ResponseWriter.Flush()
	}
}

func (w *simulatedModelCacheRecorder) recordStreamWrite(data []byte) {
	now := time.Now()
	if w.lastStreamEventAt.IsZero() {
		w.lastStreamEventAt = now
	}
	_, _ = w.streamPending.Write(data)
	eventInThisWrite := false
	for {
		boundaryIndex, boundaryLength := simulatedModelCacheSSEBoundary(w.streamPending.Bytes())
		if boundaryIndex < 0 {
			break
		}
		delayMS := int64(0)
		if !eventInThisWrite {
			delayMS = now.Sub(w.lastStreamEventAt).Milliseconds()
			if delayMS < 0 {
				delayMS = 0
			}
		}
		w.streamDelaysMS = append(w.streamDelaysMS, delayMS)
		remaining := append([]byte(nil), w.streamPending.Bytes()[boundaryIndex+boundaryLength:]...)
		w.streamPending.Reset()
		_, _ = w.streamPending.Write(remaining)
		w.lastStreamEventAt = now
		eventInThisWrite = true
	}
	if w.streamPending.Len() > 0 {
		w.lastStreamPendingAt = now
	} else {
		w.lastStreamPendingAt = time.Time{}
	}
}

func (w *simulatedModelCacheRecorder) finishStreamDelays() {
	if !w.passThrough || w.streamPending.Len() == 0 || w.lastStreamPendingAt.IsZero() {
		return
	}
	if w.lastStreamEventAt.IsZero() {
		w.lastStreamEventAt = w.lastStreamPendingAt
	}
	delayMS := w.lastStreamPendingAt.Sub(w.lastStreamEventAt).Milliseconds()
	if delayMS < 0 {
		delayMS = 0
	}
	w.streamDelaysMS = append(w.streamDelaysMS, delayMS)
	w.streamPending.Reset()
	w.lastStreamPendingAt = time.Time{}
}

func tryServeSimulatedModelCacheReplay(c *gin.Context, info *relaycommon.RelayInfo, requestBody []byte) (*simulatedModelCacheAttempt, bool) {
	settings, ok := simulatedModelCacheSettings(info)
	if !ok || len(requestBody) == 0 {
		return nil, false
	}
	if !common.RedisEnabled || common.RDB == nil {
		return nil, false
	}
	format := info.GetFinalRequestRelayFormat()
	if !isSimulatedModelCacheTextFormat(format) {
		return nil, false
	}

	attempt := &simulatedModelCacheAttempt{
		settings:           settings,
		requestBody:        append([]byte(nil), requestBody...),
		promptText:         service.ExtractSimulatedModelCachePromptText(format, requestBody),
		finalRequestFormat: format,
		upstreamModelName:  info.UpstreamModelName,
		startedAt:          time.Now(),
	}
	if !settings.IsExactReplayEnabled() {
		return attempt, false
	}

	replay, err := service.GetSimulatedModelCacheReplay(c.Request.Context(), service.SimulatedModelCacheLookupRequest{
		UserID:             info.UserId,
		ChannelID:          info.ChannelId,
		UpstreamModel:      attempt.upstreamModelName,
		FinalRequestFormat: format,
		RequestBody:        requestBody,
		ReuseLimit:         settings.ReuseLimit,
		TTLSeconds:         settings.TTLSeconds,
	})
	if err != nil {
		return nil, false
	}
	if !replay.Found {
		return attempt, false
	}

	usage := replay.Response.Usage
	info.SimulatedModelCacheInfo = service.ApplySimulatedModelCacheUsageRewrite(&usage, service.SimulatedModelCacheUsageRewrite{
		Mode:        "exact_replay",
		MatchRatio:  1,
		ReplayCount: replay.ReplayCount,
	})
	body := service.PatchSimulatedModelCacheResponseBody(format, replay.Response.ContentType, replay.Response.Body, &usage, simulatedModelCacheResponseModel(info))
	if !isSimulatedModelCacheStreamResponse(replay.Response) {
		sleepSimulatedModelCacheLatency(c, replay.Response.DurationSeconds, replay.LoadDurationSeconds)
	}
	writeSimulatedModelCacheResponseWithFirstWrite(c, replay.Response, body, info.SetFirstResponseTime)
	service.PostTextConsumeQuota(c, info, &usage, nil)
	return attempt, true
}

func beginSimulatedModelCacheRecorder(c *gin.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt) *simulatedModelCacheRecorder {
	if attempt == nil {
		return nil
	}
	recorder := &simulatedModelCacheRecorder{
		ResponseWriter: c.Writer,
		passThrough:    info != nil && info.IsStream,
	}
	if recorder.passThrough {
		recorder.lastStreamEventAt = attempt.startedAt
		if recorder.lastStreamEventAt.IsZero() {
			recorder.lastStreamEventAt = time.Now()
		}
	}
	c.Writer = recorder
	return recorder
}

func finishSimulatedModelCacheRecorder(c *gin.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt, recorder *simulatedModelCacheRecorder, usage *dto.Usage) {
	if attempt == nil || recorder == nil {
		return
	}
	originalWriter := recorder.ResponseWriter
	c.Writer = originalWriter

	recorder.finishStreamDelays()
	originalBody := append([]byte(nil), recorder.body.Bytes()...)
	if usage == nil {
		if !recorder.passThrough {
			flushSimulatedModelCacheRecorder(recorder, originalBody)
		}
		return
	}
	body := originalBody
	originalUsage := *usage
	if attempt.settings.Enabled && strings.TrimSpace(attempt.promptText) != "" {
		match, _ := service.FindSimulatedModelCachePartialMatch(c.Request.Context(), service.SimulatedModelCachePartialMatchRequest{
			UserID:        info.UserId,
			ChannelID:     info.ChannelId,
			UpstreamModel: attempt.upstreamModelName,
			PromptText:    attempt.promptText,
			MinMatchRatio: attempt.settings.MinMatchRatio,
		})
		if match.Found {
			info.SimulatedModelCacheInfo = service.ApplySimulatedModelCacheUsageRewrite(usage, service.SimulatedModelCacheUsageRewrite{
				Mode:       "partial_rewrite",
				MatchRatio: match.MatchRatio,
			})
			if !recorder.passThrough {
				body = service.PatchSimulatedModelCacheResponseBody(attempt.finalRequestFormat, recorder.Header().Get("Content-Type"), body, usage, simulatedModelCacheResponseModel(info))
			}
		}
	}

	if !recorder.passThrough {
		flushSimulatedModelCacheRecorder(recorder, body)
	}
	streamDelaysMS := []int64(nil)
	if attempt.settings.IsExactReplayEnabled() && isSimulatedModelCacheStreamContentType(recorder.Header().Get("Content-Type")) && len(recorder.streamDelaysMS) > 0 {
		streamDelaysMS = append([]int64(nil), recorder.streamDelaysMS...)
	}
	bodyForReplay := []byte(nil)
	if attempt.settings.IsExactReplayEnabled() {
		bodyForReplay = originalBody
	}
	_ = service.StoreSimulatedModelCacheResponse(c.Request.Context(), service.SimulatedModelCacheStoreRequest{
		UserID:             info.UserId,
		ChannelID:          info.ChannelId,
		UpstreamModel:      attempt.upstreamModelName,
		FinalRequestFormat: attempt.finalRequestFormat,
		RequestBody:        attempt.requestBody,
		PromptText:         attempt.promptText,
		Response: service.SimulatedModelCacheResponse{
			StatusCode:      recorder.Status(),
			Headers:         simulatedModelCacheHeaderSubset(recorder.Header()),
			ContentType:     recorder.Header().Get("Content-Type"),
			Body:            bodyForReplay,
			Usage:           originalUsage,
			DurationSeconds: time.Since(attempt.startedAt).Seconds(),
			StreamDelaysMS:  streamDelaysMS,
		},
		TTLSeconds: attempt.settings.TTLSeconds,
	})
}

func restoreSimulatedModelCacheRecorder(c *gin.Context, recorder *simulatedModelCacheRecorder) {
	if recorder != nil {
		c.Writer = recorder.ResponseWriter
	}
}

func simulatedModelCacheSettings(info *relaycommon.RelayInfo) (dto.SimulatedModelCacheSettings, bool) {
	if info == nil || info.ChannelMeta == nil || info.ChannelOtherSettings.SimulatedModelCache == nil {
		return dto.SimulatedModelCacheSettings{}, false
	}
	settings := info.ChannelOtherSettings.SimulatedModelCache.Normalize()
	if !settings.IsActive() {
		return dto.SimulatedModelCacheSettings{}, false
	}
	return settings, true
}

func simulatedModelCacheResponseModel(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	return info.DownstreamModelName("")
}

func isSimulatedModelCacheTextFormat(format types.RelayFormat) bool {
	switch format {
	case types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction, types.RelayFormatClaude, types.RelayFormatGemini:
		return true
	default:
		return false
	}
}

func sleepSimulatedModelCacheLatency(c *gin.Context, originalDurationSeconds float64, alreadyElapsedSeconds float64) {
	if originalDurationSeconds <= 0 {
		return
	}
	sleepSeconds := math.Max(0, originalDurationSeconds*0.6+(rand.Float64()*6-3))
	sleepSeconds -= alreadyElapsedSeconds
	if sleepSeconds <= 0 {
		return
	}
	timer := time.NewTimer(time.Duration(sleepSeconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-c.Request.Context().Done():
	}
}

func writeSimulatedModelCacheResponse(c *gin.Context, response service.SimulatedModelCacheResponse, body []byte) {
	writeSimulatedModelCacheResponseWithFirstWrite(c, response, body, nil)
}

func writeSimulatedModelCacheResponseWithFirstWrite(c *gin.Context, response service.SimulatedModelCacheResponse, body []byte, beforeFirstWrite func()) {
	for key, value := range response.Headers {
		if value != "" {
			c.Writer.Header().Set(key, value)
		}
	}
	if response.ContentType != "" {
		c.Writer.Header().Set("Content-Type", response.ContentType)
	}
	statusCode := normalizeSimulatedModelCacheStatusCode(response.StatusCode)
	invokeBeforeFirstWrite := func() {
		if beforeFirstWrite != nil {
			beforeFirstWrite()
			beforeFirstWrite = nil
		}
	}
	if isSimulatedModelCacheStreamResponse(response) {
		c.Writer.Header().Del("Content-Length")
		chunks := splitSimulatedModelCacheSSEChunks(body)
		if len(chunks) == 0 {
			invokeBeforeFirstWrite()
			c.Writer.WriteHeader(statusCode)
			return
		}
		for i, chunk := range chunks {
			if !sleepSimulatedModelCacheStreamDelay(c, response, i, len(chunks)) {
				return
			}
			invokeBeforeFirstWrite()
			if i == 0 {
				c.Writer.WriteHeader(statusCode)
			}
			if _, err := c.Writer.Write(chunk); err != nil {
				return
			}
			c.Writer.Flush()
		}
		return
	}
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	invokeBeforeFirstWrite()
	c.Writer.WriteHeader(statusCode)
	_, _ = c.Writer.Write(body)
}

func isSimulatedModelCacheStreamResponse(response service.SimulatedModelCacheResponse) bool {
	return isSimulatedModelCacheStreamContentType(response.ContentType)
}

func isSimulatedModelCacheStreamContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

func splitSimulatedModelCacheSSEChunks(body []byte) [][]byte {
	if len(body) == 0 {
		return nil
	}
	chunks := make([][]byte, 0)
	start := 0
	for start < len(body) {
		boundaryIndex, boundaryLength := simulatedModelCacheSSEBoundary(body[start:])
		if boundaryIndex < 0 {
			break
		}
		end := start + boundaryIndex + boundaryLength
		chunks = append(chunks, body[start:end])
		start = end
	}
	if start < len(body) {
		chunks = append(chunks, body[start:])
	}
	return chunks
}

func simulatedModelCacheSSEBoundary(data []byte) (int, int) {
	lfIndex := bytes.Index(data, []byte("\n\n"))
	crlfIndex := bytes.Index(data, []byte("\r\n\r\n"))
	if lfIndex < 0 {
		if crlfIndex < 0 {
			return -1, 0
		}
		return crlfIndex, len("\r\n\r\n")
	}
	if crlfIndex >= 0 && crlfIndex < lfIndex {
		return crlfIndex, len("\r\n\r\n")
	}
	return lfIndex, len("\n\n")
}

func sleepSimulatedModelCacheStreamDelay(c *gin.Context, response service.SimulatedModelCacheResponse, index int, chunkCount int) bool {
	delay := simulatedModelCacheStreamDelay(response, index, chunkCount)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-c.Request.Context().Done():
		return false
	}
}

func simulatedModelCacheStreamDelay(response service.SimulatedModelCacheResponse, index int, chunkCount int) time.Duration {
	delaysMS := response.StreamDelaysMS
	alreadyElapsed := time.Duration(0)
	if index == 0 && response.ReplayPreparationSeconds > 0 {
		alreadyElapsed = time.Duration(response.ReplayPreparationSeconds * float64(time.Second))
	}
	if len(delaysMS) > 0 {
		if index >= len(delaysMS) {
			index = len(delaysMS) - 1
		}
		delayMS := delaysMS[index]
		if delayMS > 0 {
			delay := time.Duration(delayMS)*time.Millisecond - alreadyElapsed
			if delay > 0 {
				return delay
			}
		}
		return 0
	}
	if response.DurationSeconds <= 0 || chunkCount <= 0 {
		return 0
	}
	delay := time.Duration(response.DurationSeconds*float64(time.Second)/float64(chunkCount)) - alreadyElapsed
	if delay > 0 {
		return delay
	}
	return 0
}

func flushSimulatedModelCacheRecorder(recorder *simulatedModelCacheRecorder, body []byte) {
	statusCode := normalizeSimulatedModelCacheStatusCode(recorder.Status())
	recorder.ResponseWriter.Header().Set("Content-Length", strconv.Itoa(len(body)))
	recorder.ResponseWriter.WriteHeader(statusCode)
	_, _ = recorder.ResponseWriter.Write(body)
}

func normalizeSimulatedModelCacheStatusCode(statusCode int) int {
	if statusCode < http.StatusContinue || statusCode > 999 {
		return http.StatusOK
	}
	return statusCode
}

func simulatedModelCacheHeaderSubset(headers http.Header) map[string]string {
	out := map[string]string{}
	for _, key := range []string{"Content-Type", "Cache-Control"} {
		if value := headers.Get(key); value != "" {
			out[key] = value
		}
	}
	return out
}
