package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
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
	partialMatchResult <-chan simulatedModelCachePartialMatchResult
	partialMatchCancel context.CancelFunc
}

type simulatedModelCachePartialMatchResult struct {
	match service.SimulatedModelCachePartialMatch
	err   error
}

type simulatedModelCacheRecorder struct {
	gin.ResponseWriter
	body                bytes.Buffer
	streamPending       bytes.Buffer
	streamTail          bytes.Buffer
	streamDelaysMS      []int64
	lastStreamEventAt   time.Time
	lastStreamPendingAt time.Time
	responseFormat      types.RelayFormat
	includeStreamUsage  bool
	partialMatchCancel  context.CancelFunc
	status              int
	size                int
	written             bool
	passThrough         bool
	holdingStreamTail   bool
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
	if n == 0 {
		return 0, nil
	}
	if writeErr := w.processStreamWrite(data[:n]); writeErr != nil {
		return 0, writeErr
	}
	return n, nil
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

func (w *simulatedModelCacheRecorder) processStreamWrite(data []byte) error {
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
		eventEnd := boundaryIndex + boundaryLength
		event := append([]byte(nil), w.streamPending.Bytes()[:eventEnd]...)
		delayMS := int64(0)
		if !eventInThisWrite {
			delayMS = now.Sub(w.lastStreamEventAt).Milliseconds()
			if delayMS < 0 {
				delayMS = 0
			}
		}
		w.streamDelaysMS = append(w.streamDelaysMS, delayMS)
		remaining := append([]byte(nil), w.streamPending.Bytes()[eventEnd:]...)
		w.streamPending.Reset()
		_, _ = w.streamPending.Write(remaining)
		w.lastStreamEventAt = now
		eventInThisWrite = true

		if w.holdingStreamTail || isSimulatedModelCacheStreamTailEvent(w.responseFormat, event) {
			w.holdingStreamTail = true
			_, _ = w.streamTail.Write(event)
			continue
		}
		if err := w.writeStreamBytes(event); err != nil {
			return err
		}
	}
	if w.streamPending.Len() > 0 {
		w.lastStreamPendingAt = now
	} else {
		w.lastStreamPendingAt = time.Time{}
	}
	return nil
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
	w.lastStreamPendingAt = time.Time{}
}

func (w *simulatedModelCacheRecorder) releaseStreamTail() error {
	if !w.passThrough {
		return nil
	}
	pending := append([]byte(nil), w.streamTail.Bytes()...)
	pending = append(pending, w.streamPending.Bytes()...)
	w.streamTail.Reset()
	w.streamPending.Reset()
	w.holdingStreamTail = false
	if len(pending) == 0 {
		return nil
	}
	err := w.writeStreamBytes(pending)
	if err != nil {
		return err
	}
	w.ResponseWriter.Flush()
	return nil
}

func (w *simulatedModelCacheRecorder) writeStreamBytes(data []byte) error {
	written, err := w.ResponseWriter.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

type simulatedModelCacheStreamEventKind int

const (
	simulatedModelCacheStreamEventOther simulatedModelCacheStreamEventKind = iota
	simulatedModelCacheStreamEventClaudeDelta
	simulatedModelCacheStreamEventClaudeStop
	simulatedModelCacheStreamEventOpenAIUsage
	simulatedModelCacheStreamEventOpenAIDone
)

func isSimulatedModelCacheStreamTailEvent(format types.RelayFormat, event []byte) bool {
	switch simulatedModelCacheStreamEventType(format, event) {
	case simulatedModelCacheStreamEventClaudeDelta,
		simulatedModelCacheStreamEventClaudeStop,
		simulatedModelCacheStreamEventOpenAIUsage,
		simulatedModelCacheStreamEventOpenAIDone:
		return true
	default:
		return false
	}
}

func simulatedModelCacheStreamEventType(format types.RelayFormat, event []byte) simulatedModelCacheStreamEventKind {
	data, ok := simulatedModelCacheSSEData(event)
	if !ok {
		return simulatedModelCacheStreamEventOther
	}
	if format == types.RelayFormatOpenAI && string(data) == "[DONE]" {
		return simulatedModelCacheStreamEventOpenAIDone
	}

	var payload struct {
		Type    string         `json:"type"`
		Choices []any          `json:"choices"`
		Usage   map[string]any `json:"usage"`
	}
	if common.Unmarshal(data, &payload) != nil {
		return simulatedModelCacheStreamEventOther
	}
	switch format {
	case types.RelayFormatClaude:
		switch payload.Type {
		case "message_delta":
			return simulatedModelCacheStreamEventClaudeDelta
		case "message_stop":
			return simulatedModelCacheStreamEventClaudeStop
		}
	case types.RelayFormatOpenAI:
		if payload.Usage != nil && len(payload.Choices) == 0 {
			return simulatedModelCacheStreamEventOpenAIUsage
		}
	}
	return simulatedModelCacheStreamEventOther
}

func simulatedModelCacheSSEData(event []byte) ([]byte, bool) {
	normalized := bytes.ReplaceAll(event, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))
	dataLines := make([][]byte, 0, 1)
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		dataLines = append(dataLines, bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
	}
	if len(dataLines) == 0 {
		return nil, false
	}
	return bytes.Join(dataLines, []byte("\n")), true
}

func (w *simulatedModelCacheRecorder) writePatchedStreamTail(usage *dto.Usage) (bool, bool, error) {
	pending := append([]byte(nil), w.streamTail.Bytes()...)
	pending = append(pending, w.streamPending.Bytes()...)
	w.streamTail.Reset()
	w.streamPending.Reset()
	w.holdingStreamTail = false
	if len(pending) == 0 {
		return false, false, nil
	}
	if w.responseFormat == types.RelayFormatOpenAI && !w.includeStreamUsage {
		err := w.writeStreamBytes(pending)
		if err == nil {
			w.ResponseWriter.Flush()
		}
		return false, false, err
	}

	chunks := splitSimulatedModelCacheSSEChunks(pending)
	targetIndex := -1
	doneIndex := -1
	for i, chunk := range chunks {
		switch simulatedModelCacheStreamEventType(w.responseFormat, chunk) {
		case simulatedModelCacheStreamEventClaudeDelta, simulatedModelCacheStreamEventOpenAIUsage:
			targetIndex = i
		case simulatedModelCacheStreamEventOpenAIDone:
			if doneIndex == -1 {
				doneIndex = i
			}
		}
	}

	targetFound := false
	if w.responseFormat == types.RelayFormatOpenAI && targetIndex == -1 && doneIndex >= 0 {
		usageEvent, err := simulatedModelCacheOpenAIUsageEvent(usage, chunks[doneIndex])
		if err != nil {
			writeErr := w.writeStreamBytes(pending)
			if writeErr == nil {
				w.ResponseWriter.Flush()
			}
			if writeErr != nil {
				return false, false, writeErr
			}
			return false, false, err
		}
		chunks = append(chunks, nil)
		copy(chunks[doneIndex+1:], chunks[doneIndex:])
		chunks[doneIndex] = usageEvent
		targetIndex = doneIndex
		targetFound = true
	}

	if targetIndex >= 0 && !targetFound {
		patchedEvent, patched := patchSimulatedModelCacheStreamUsageEvent(w.responseFormat, chunks[targetIndex], usage)
		if patched {
			chunks[targetIndex] = patchedEvent
			targetFound = true
		}
	}
	for _, chunk := range chunks {
		if err := w.writeStreamBytes(chunk); err != nil {
			return false, targetFound, err
		}
	}
	w.ResponseWriter.Flush()
	return targetFound, targetFound, nil
}

func patchSimulatedModelCacheStreamUsageEvent(format types.RelayFormat, event []byte, usage *dto.Usage) ([]byte, bool) {
	if usage == nil {
		return event, false
	}
	data, ok := simulatedModelCacheSSEData(event)
	if !ok {
		return event, false
	}
	var payload map[string]any
	if common.Unmarshal(data, &payload) != nil {
		return event, false
	}
	usageMap, _ := payload["usage"].(map[string]any)
	cachedTokens := usage.PromptTokensDetails.CachedTokens
	switch format {
	case types.RelayFormatClaude:
		if payload["type"] != "message_delta" {
			return event, false
		}
		if usageMap == nil {
			usageMap = map[string]any{}
			payload["usage"] = usageMap
		}
		inputTokens := usage.PromptTokens
		if usage.UsageSemantic != "anthropic" {
			inputTokens -= cachedTokens + simulatedModelCacheCacheCreationTokens(usage)
			if inputTokens < 0 {
				inputTokens = 0
			}
		}
		usageMap["input_tokens"] = inputTokens
		usageMap["cache_read_input_tokens"] = cachedTokens
		usageMap["output_tokens"] = usage.CompletionTokens
	case types.RelayFormatOpenAI:
		if usageMap == nil {
			return event, false
		}
		promptTokens := simulatedModelCacheOpenAIPromptTokens(usage)
		usageMap["prompt_tokens"] = promptTokens
		usageMap["completion_tokens"] = usage.CompletionTokens
		usageMap["total_tokens"] = promptTokens + usage.CompletionTokens
		promptDetails, _ := usageMap["prompt_tokens_details"].(map[string]any)
		if promptDetails == nil {
			promptDetails = map[string]any{}
			usageMap["prompt_tokens_details"] = promptDetails
		}
		promptDetails["cached_tokens"] = cachedTokens
	default:
		return event, false
	}
	patchedData, err := common.Marshal(payload)
	if err != nil {
		return event, false
	}
	return replaceSimulatedModelCacheSSEData(event, patchedData), true
}

func simulatedModelCacheCacheCreationTokens(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	cacheCreationTokens := usage.PromptTokensDetails.CachedCreationTokens
	splitCacheCreationTokens := usage.ClaudeCacheCreation5mTokens + usage.ClaudeCacheCreation1hTokens
	if splitCacheCreationTokens > cacheCreationTokens {
		return splitCacheCreationTokens
	}
	return cacheCreationTokens
}

func simulatedModelCacheOpenAIPromptTokens(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	promptTokens := usage.PromptTokens
	if usage.UsageSemantic == "anthropic" {
		promptTokens += usage.PromptTokensDetails.CachedTokens + simulatedModelCacheCacheCreationTokens(usage)
	}
	return promptTokens
}

func replaceSimulatedModelCacheSSEData(event []byte, data []byte) []byte {
	lines := strings.SplitAfter(string(event), "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		if replaced {
			lines[i] = ""
			continue
		}
		prefixLen := len(line) - len(strings.TrimLeft(line, " \t"))
		lineEnding := ""
		if strings.HasSuffix(line, "\n") {
			lineEnding = "\n"
			if strings.HasSuffix(line, "\r\n") {
				lineEnding = "\r\n"
			}
		}
		lines[i] = line[:prefixLen] + "data: " + string(data) + lineEnding
		replaced = true
	}
	return []byte(strings.Join(lines, ""))
}

func simulatedModelCacheOpenAIUsageEvent(usage *dto.Usage, doneEvent []byte) ([]byte, error) {
	if usage == nil {
		return nil, fmt.Errorf("simulated model cache stream usage is nil")
	}
	promptTokens := simulatedModelCacheOpenAIPromptTokens(usage)
	payload := map[string]any{
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": usage.CompletionTokens,
			"total_tokens":      promptTokens + usage.CompletionTokens,
			"prompt_tokens_details": map[string]any{
				"cached_tokens": usage.PromptTokensDetails.CachedTokens,
			},
		},
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}
	lineEnding := "\n"
	if bytes.Contains(doneEvent, []byte("\r\n")) {
		lineEnding = "\r\n"
	}
	return []byte("data: " + string(data) + lineEnding + lineEnding), nil
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
		startSimulatedModelCachePartialMatch(c.Request.Context(), info, attempt)
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
		startSimulatedModelCachePartialMatch(c.Request.Context(), info, attempt)
		return attempt, false
	}

	usage := replay.Response.Usage
	info.SimulatedModelCacheInfo = service.ApplySimulatedModelCacheUsageRewrite(&usage, service.SimulatedModelCacheUsageRewrite{
		Mode:        "exact_replay",
		MatchRatio:  1,
		ReplayCount: replay.ReplayCount,
	})
	body := service.PatchSimulatedModelCacheResponseBody(info.RelayFormat, replay.Response.ContentType, replay.Response.Body, &usage, simulatedModelCacheResponseModel(info))
	if !isSimulatedModelCacheStreamResponse(replay.Response) {
		sleepSimulatedModelCacheLatency(c, replay.Response.DurationSeconds, replay.LoadDurationSeconds)
	}
	writeSimulatedModelCacheResponseWithFirstWrite(c, replay.Response, body, info.SetFirstResponseTime)
	service.PostTextConsumeQuota(c, info, &usage, nil)
	return attempt, true
}

func startSimulatedModelCachePartialMatch(ctx context.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt) {
	if attempt == nil || info == nil || !attempt.settings.Enabled || strings.TrimSpace(attempt.promptText) == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	matchCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan simulatedModelCachePartialMatchResult, 1)
	attempt.partialMatchResult = resultCh
	attempt.partialMatchCancel = cancel
	req := service.SimulatedModelCachePartialMatchRequest{
		UserID:        info.UserId,
		ChannelID:     info.ChannelId,
		UpstreamModel: attempt.upstreamModelName,
		PromptText:    attempt.promptText,
		MinMatchRatio: attempt.settings.MinMatchRatio,
	}
	go func() {
		match, err := service.FindSimulatedModelCachePartialMatch(matchCtx, req)
		result := simulatedModelCachePartialMatchResult{match: match, err: err}
		select {
		case resultCh <- result:
		case <-matchCtx.Done():
		}
	}()
}

func waitSimulatedModelCachePartialMatch(ctx context.Context, attempt *simulatedModelCacheAttempt) (service.SimulatedModelCachePartialMatch, bool) {
	if attempt == nil || attempt.partialMatchResult == nil {
		return service.SimulatedModelCachePartialMatch{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return service.SimulatedModelCachePartialMatch{}, false
	}
	select {
	case result := <-attempt.partialMatchResult:
		return result.match, result.err == nil && result.match.Found
	case <-ctx.Done():
		return service.SimulatedModelCachePartialMatch{}, false
	}
}

func beginSimulatedModelCacheRecorder(c *gin.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt) *simulatedModelCacheRecorder {
	if attempt == nil {
		return nil
	}
	responseFormat := types.RelayFormat("")
	includeStreamUsage := true
	passThrough := false
	if info != nil {
		responseFormat = info.RelayFormat
		includeStreamUsage = info.RelayFormat != types.RelayFormatOpenAI || info.ShouldIncludeUsage
		passThrough = info.IsStream
	}
	recorder := &simulatedModelCacheRecorder{
		ResponseWriter:     c.Writer,
		passThrough:        passThrough,
		responseFormat:     responseFormat,
		includeStreamUsage: includeStreamUsage,
		partialMatchCancel: attempt.partialMatchCancel,
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
	if attempt.partialMatchCancel != nil {
		defer attempt.partialMatchCancel()
	}

	recorder.finishStreamDelays()
	originalBody := append([]byte(nil), recorder.body.Bytes()...)
	if usage == nil {
		if recorder.passThrough {
			_ = recorder.releaseStreamTail()
		} else {
			flushSimulatedModelCacheRecorder(recorder, originalBody)
		}
		return
	}
	body := originalBody
	originalUsage := *usage
	if attempt.settings.Enabled && strings.TrimSpace(attempt.promptText) != "" {
		match, found := waitSimulatedModelCachePartialMatch(c.Request.Context(), attempt)
		if found {
			info.SimulatedModelCacheInfo = service.ApplySimulatedModelCacheUsageRewrite(usage, service.SimulatedModelCacheUsageRewrite{
				Mode:       "partial_rewrite",
				MatchRatio: match.MatchRatio,
			})
			if recorder.passThrough {
				injected, targetFound, writeErr := recorder.writePatchedStreamTail(usage)
				info.SimulatedModelCacheInfo.StreamUsageInjected = &injected
				if usage.PromptTokensDetails.CachedTokens > 0 && recorder.includeStreamUsage && !targetFound {
					logger.LogWarn(c, fmt.Sprintf("simulated model cache stream usage event not found: request_id=%s format=%s cached_tokens=%d",
						info.RequestId, info.RelayFormat, usage.PromptTokensDetails.CachedTokens))
				}
				if writeErr != nil {
					logger.LogWarn(c, fmt.Sprintf("simulated model cache stream usage write failed: request_id=%s format=%s error=%s",
						info.RequestId, info.RelayFormat, writeErr.Error()))
				}
			} else {
				body = service.PatchSimulatedModelCacheResponseBody(info.RelayFormat, recorder.Header().Get("Content-Type"), body, usage, simulatedModelCacheResponseModel(info))
			}
		}
	}

	if recorder.passThrough && (info.SimulatedModelCacheInfo == nil || info.SimulatedModelCacheInfo.StreamUsageInjected == nil) {
		_ = recorder.releaseStreamTail()
	} else if !recorder.passThrough {
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
		if recorder.partialMatchCancel != nil {
			recorder.partialMatchCancel()
		}
		if recorder.passThrough {
			recorder.finishStreamDelays()
			_ = recorder.releaseStreamTail()
		}
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
	if recorder.passThrough {
		return
	}
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
