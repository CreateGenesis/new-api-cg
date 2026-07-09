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
	body        bytes.Buffer
	status      int
	size        int
	written     bool
	passThrough bool
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
	sleepSimulatedModelCacheLatency(c, replay.Response.DurationSeconds)
	info.SetFirstResponseTime()
	body := service.PatchSimulatedModelCacheResponseBody(format, replay.Response.ContentType, replay.Response.Body, &usage)
	writeSimulatedModelCacheResponse(c, replay.Response, body)
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
	c.Writer = recorder
	return recorder
}

func finishSimulatedModelCacheRecorder(c *gin.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt, recorder *simulatedModelCacheRecorder, usage *dto.Usage) {
	if attempt == nil || recorder == nil {
		return
	}
	originalWriter := recorder.ResponseWriter
	c.Writer = originalWriter

	originalBody := append([]byte(nil), recorder.body.Bytes()...)
	if usage == nil {
		if !recorder.passThrough {
			flushSimulatedModelCacheRecorder(recorder, originalBody)
		}
		return
	}
	body := originalBody
	originalUsage := *usage
	if strings.TrimSpace(attempt.promptText) != "" {
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
				body = service.PatchSimulatedModelCacheResponseBody(attempt.finalRequestFormat, recorder.Header().Get("Content-Type"), body, usage)
			}
		}
	}

	if !recorder.passThrough {
		flushSimulatedModelCacheRecorder(recorder, body)
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
			Body:            originalBody,
			Usage:           originalUsage,
			DurationSeconds: time.Since(attempt.startedAt).Seconds(),
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
	if !settings.Enabled {
		return dto.SimulatedModelCacheSettings{}, false
	}
	return settings, true
}

func isSimulatedModelCacheTextFormat(format types.RelayFormat) bool {
	switch format {
	case types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction, types.RelayFormatClaude, types.RelayFormatGemini:
		return true
	default:
		return false
	}
}

func sleepSimulatedModelCacheLatency(c *gin.Context, originalDurationSeconds float64) {
	if originalDurationSeconds <= 0 {
		return
	}
	sleepSeconds := math.Max(0, originalDurationSeconds*0.6+(rand.Float64()*6-3))
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
	for key, value := range response.Headers {
		if value != "" {
			c.Writer.Header().Set(key, value)
		}
	}
	if response.ContentType != "" {
		c.Writer.Header().Set("Content-Type", response.ContentType)
	}
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	statusCode := normalizeSimulatedModelCacheStatusCode(response.StatusCode)
	c.Writer.WriteHeader(statusCode)
	_, _ = c.Writer.Write(body)
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
