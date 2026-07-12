package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type simulatedModelCacheAttempt struct {
	settings         dto.SimulatedModelCacheSettings
	promptText       string
	cacheModelName   string
	partialMatch     simulatedModelCachePartialMatchTask
	bypassReason     string
	matchDiagnostics service.SimulatedModelCachePartialMatch
}

type simulatedModelCachePartialMatchTask interface {
	TryResult() (service.SimulatedModelCachePartialMatchResult, bool)
	Cancel()
	StoreWhenReady(context.Context)
}

type simulatedModelCacheRecorder struct {
	gin.ResponseWriter
	attempt                  *simulatedModelCacheAttempt
	body                     bytes.Buffer
	streamPending            bytes.Buffer
	streamTail               bytes.Buffer
	responseFormat           types.RelayFormat
	includeStreamUsage       bool
	responseReservations     []*service.SimulatedModelCacheMemoryReservation
	responseReservedBytes    int64
	responseBufferLimit      int
	status                   int
	size                     int
	written                  bool
	stream                   bool
	passThrough              bool
	holdingStreamTail        bool
	streamInspectionDisabled bool
}

func (a *simulatedModelCacheAttempt) setBypassReason(reason string) {
	if a != nil && a.bypassReason == "" && reason != "" {
		a.bypassReason = reason
	}
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
	w.size += len(data)
	if len(data) == 0 {
		return 0, nil
	}
	if w.stream {
		if w.streamInspectionDisabled {
			return w.ResponseWriter.Write(data)
		}
		processed := 0
		for processed < len(data) {
			chunkSize := len(data) - processed
			if chunkSize > 32*1024 {
				chunkSize = 32 * 1024
			}
			requiredBytes := w.streamPending.Len() + w.streamTail.Len() + chunkSize
			if requiredBytes > w.responseBufferLimit {
				written, err := w.disableStreamInspection(data[processed:], service.SimulatedModelCacheBypassResponseTooLarge)
				return processed + written, err
			}
			if !w.ensureResponseReservation(requiredBytes) {
				written, err := w.disableStreamInspection(data[processed:], service.SimulatedModelCacheBypassResponseBuffer)
				return processed + written, err
			}
			if err := w.processStreamWrite(data[processed : processed+chunkSize]); err != nil {
				return processed, err
			}
			processed += chunkSize
		}
		return len(data), nil
	}
	if w.passThrough {
		return w.ResponseWriter.Write(data)
	}
	requiredBytes := w.body.Len() + len(data)
	if requiredBytes > w.responseBufferLimit {
		return w.switchNonStreamToPassThrough(data, service.SimulatedModelCacheBypassResponseTooLarge)
	}
	if !w.ensureResponseReservation(requiredBytes) {
		return w.switchNonStreamToPassThrough(data, service.SimulatedModelCacheBypassResponseBuffer)
	}
	return w.body.Write(data)
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

func (w *simulatedModelCacheRecorder) releaseResponseReservation() {
	for _, reservation := range w.responseReservations {
		reservation.Release()
	}
	w.responseReservations = nil
	w.responseReservedBytes = 0
}

func (w *simulatedModelCacheRecorder) ensureResponseReservation(requiredBytes int) bool {
	if int64(requiredBytes) <= w.responseReservedBytes {
		return true
	}
	delta := int64(requiredBytes) - w.responseReservedBytes
	reservation := service.ReserveSimulatedModelCacheMemory(delta)
	if reservation == nil {
		return false
	}
	w.responseReservations = append(w.responseReservations, reservation)
	w.responseReservedBytes = int64(requiredBytes)
	return true
}

func (w *simulatedModelCacheRecorder) cancelPartialMatch(reason string) {
	w.attempt.setBypassReason(reason)
	if w.attempt != nil && w.attempt.partialMatch != nil {
		w.attempt.partialMatch.Cancel()
		w.attempt.partialMatch = nil
	}
}

func (w *simulatedModelCacheRecorder) switchNonStreamToPassThrough(data []byte, reason string) (int, error) {
	w.cancelPartialMatch(reason)
	w.passThrough = true
	w.ResponseWriter.WriteHeader(w.Status())
	if w.body.Len() > 0 {
		if _, err := w.ResponseWriter.Write(w.body.Bytes()); err != nil {
			return 0, err
		}
		w.body.Reset()
	}
	w.releaseResponseReservation()
	return w.ResponseWriter.Write(data)
}

func (w *simulatedModelCacheRecorder) disableStreamInspection(data []byte, reason string) (int, error) {
	w.cancelPartialMatch(reason)
	w.streamInspectionDisabled = true
	w.holdingStreamTail = false
	if w.streamTail.Len() > 0 {
		if _, err := w.ResponseWriter.Write(w.streamTail.Bytes()); err != nil {
			return 0, err
		}
		w.streamTail.Reset()
	}
	if w.streamPending.Len() > 0 {
		if _, err := w.ResponseWriter.Write(w.streamPending.Bytes()); err != nil {
			return 0, err
		}
		w.streamPending.Reset()
	}
	w.releaseResponseReservation()
	return w.ResponseWriter.Write(data)
}

func (w *simulatedModelCacheRecorder) processStreamWrite(data []byte) error {
	_, _ = w.streamPending.Write(data)
	for {
		boundaryIndex, boundaryLength := simulatedModelCacheSSEBoundary(w.streamPending.Bytes())
		if boundaryIndex < 0 {
			break
		}
		eventEnd := boundaryIndex + boundaryLength
		event := w.streamPending.Next(eventEnd)

		shouldHoldTail := w.attempt != nil && w.attempt.partialMatch != nil
		if shouldHoldTail && (w.holdingStreamTail || isSimulatedModelCacheStreamTailEvent(w.responseFormat, event)) {
			w.holdingStreamTail = true
			_, _ = w.streamTail.Write(event)
			continue
		}
		if err := w.writeStreamBytes(event); err != nil {
			return err
		}
	}
	return nil
}

func (w *simulatedModelCacheRecorder) releaseStreamTail() error {
	if !w.passThrough {
		return nil
	}
	w.holdingStreamTail = false
	if w.streamTail.Len() > 0 {
		if err := w.writeStreamBytes(w.streamTail.Bytes()); err != nil {
			return err
		}
		w.streamTail.Reset()
	}
	if w.streamPending.Len() > 0 {
		if err := w.writeStreamBytes(w.streamPending.Bytes()); err != nil {
			return err
		}
		w.streamPending.Reset()
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
	w.holdingStreamTail = false
	if w.streamTail.Len() == 0 && w.streamPending.Len() == 0 {
		return false, false, nil
	}
	if w.responseFormat == types.RelayFormatOpenAI && !w.includeStreamUsage {
		err := w.releaseStreamTail()
		if err != nil {
			return false, false, err
		}
		return false, false, nil
	}

	chunks := splitSimulatedModelCacheSSEChunks(w.streamTail.Bytes())
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
			writeErr := w.releaseStreamTail()
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
	if w.streamPending.Len() > 0 {
		if err := w.writeStreamBytes(w.streamPending.Bytes()); err != nil {
			return false, targetFound, err
		}
	}
	w.streamTail.Reset()
	w.streamPending.Reset()
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
		if _, ok := usageMap["cache_creation_input_tokens"]; !ok {
			usageMap["cache_creation_input_tokens"] = usage.PromptTokensDetails.CachedCreationTokens
		}
		if _, ok := usageMap["claude_cache_creation_5_m_tokens"]; !ok {
			usageMap["claude_cache_creation_5_m_tokens"] = usage.ClaudeCacheCreation5mTokens
		}
		if _, ok := usageMap["claude_cache_creation_1_h_tokens"]; !ok {
			usageMap["claude_cache_creation_1_h_tokens"] = usage.ClaudeCacheCreation1hTokens
		}
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

func prepareSimulatedModelCacheAttempt(c *gin.Context, info *relaycommon.RelayInfo, requestBody []byte) *simulatedModelCacheAttempt {
	settings, ok := simulatedModelCacheSettings(info)
	if !ok || len(requestBody) == 0 {
		return nil
	}
	format := info.GetFinalRequestRelayFormat()
	if !isSimulatedModelCacheTextFormat(format) {
		return nil
	}

	attempt := &simulatedModelCacheAttempt{
		settings:       settings,
		cacheModelName: simulatedModelCacheModelName(info),
		promptText:     service.ExtractSimulatedModelCachePromptText(format, requestBody),
	}
	startSimulatedModelCachePartialMatch(c, info, attempt)
	return attempt
}

func startSimulatedModelCachePartialMatch(c *gin.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt) {
	if attempt == nil || info == nil || !attempt.settings.Enabled || strings.TrimSpace(attempt.promptText) == "" {
		return
	}
	handle, bypassReason := service.SubmitSimulatedModelCachePartialMatch(c.Request.Context(), service.SimulatedModelCachePartialMatchRequest{
		UserID:        info.UserId,
		Model:         attempt.cacheModelName,
		PromptText:    attempt.promptText,
		MinMatchRatio: attempt.settings.MinMatchRatio,
		TTLSeconds:    attempt.settings.TTLSeconds,
	})
	attempt.partialMatch = handle
	attempt.setBypassReason(bypassReason)
}

func beginSimulatedModelCacheRecorder(c *gin.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt) *simulatedModelCacheRecorder {
	if attempt == nil {
		return nil
	}
	responseFormat := types.RelayFormat("")
	includeStreamUsage := true
	stream := false
	if info != nil {
		responseFormat = info.RelayFormat
		includeStreamUsage = info.RelayFormat != types.RelayFormatOpenAI || info.ShouldIncludeUsage
		stream = info.IsStream
	}
	bufferLimit := service.SimulatedModelCacheResponseBufferBytes()
	recorder := &simulatedModelCacheRecorder{
		ResponseWriter:      c.Writer,
		attempt:             attempt,
		passThrough:         stream || attempt.partialMatch == nil,
		stream:              stream,
		responseFormat:      responseFormat,
		includeStreamUsage:  includeStreamUsage,
		responseBufferLimit: int(bufferLimit),
	}
	if stream && attempt.partialMatch == nil {
		recorder.streamInspectionDisabled = true
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
	defer recorder.releaseResponseReservation()

	if usage == nil {
		if recorder.stream {
			_ = recorder.releaseStreamTail()
		} else if !recorder.passThrough {
			flushSimulatedModelCacheRecorder(recorder, recorder.body.Bytes())
		}
		return
	}
	originalInputTokens := simulatedModelCacheOriginalInputTokens(usage)
	inputTokensEligible := originalInputTokens >= common.GetSimulatedModelCacheMinInputTokens()
	if !inputTokensEligible {
		attempt.bypassReason = service.SimulatedModelCacheBypassInputTokensLow
	}
	matchFound := false
	match := service.SimulatedModelCachePartialMatch{}
	var preparedFingerprint *service.SimulatedModelCachePreparedFingerprint
	if attempt.partialMatch != nil {
		if inputTokensEligible {
			if c.Request.Context().Err() != nil {
				attempt.setBypassReason(service.SimulatedModelCacheBypassRequestCanceled)
			} else if result, ready := attempt.partialMatch.TryResult(); ready {
				attempt.matchDiagnostics = result.Match
				preparedFingerprint = result.Prepared
				if result.Err != nil {
					if result.Match.BypassReason != "" {
						attempt.setBypassReason(result.Match.BypassReason)
					} else {
						attempt.setBypassReason(service.SimulatedModelCacheBypassRedisError)
					}
				} else if result.Match.BypassReason != "" {
					attempt.setBypassReason(result.Match.BypassReason)
				} else if result.Match.Found {
					matchFound = true
					match = result.Match
				}
			} else {
				attempt.setBypassReason(service.SimulatedModelCacheBypassMatchNotReady)
				attempt.partialMatch.StoreWhenReady(c.Request.Context())
				attempt.partialMatch = nil
			}
		}
		if attempt.partialMatch != nil {
			attempt.partialMatch.Cancel()
			attempt.partialMatch = nil
		}
	}
	if inputTokensEligible && c.Request.Context().Err() == nil && preparedFingerprint != nil {
		err := preparedFingerprint.Store(c.Request.Context())
		if err != nil && !matchFound {
			if errors.Is(err, service.ErrSimulatedModelCacheMemoryBudget) {
				attempt.setBypassReason(service.SimulatedModelCacheBypassMemoryBudget)
			} else if errors.Is(err, service.ErrSimulatedModelCacheRedisDisabled) {
				attempt.setBypassReason(service.SimulatedModelCacheBypassRedisUnavailable)
			} else {
				attempt.setBypassReason(service.SimulatedModelCacheBypassRedisError)
			}
		} else if err != nil {
			logger.LogWarn(c, fmt.Sprintf("simulated model cache fingerprint store failed: request_id=%s error=%s", info.RequestId, err.Error()))
		}
	}

	body := recorder.body.Bytes()
	var patchReservation *service.SimulatedModelCacheMemoryReservation
	if matchFound {
		patchBytes := int64(len(body))
		if recorder.stream {
			patchBytes = int64(recorder.streamTail.Len() + recorder.streamPending.Len())
		}
		patchReservation = service.ReserveSimulatedModelCacheMemory(patchBytes)
		if patchReservation == nil {
			matchFound = false
			attempt.setBypassReason(service.SimulatedModelCacheBypassMemoryBudget)
		} else {
			defer patchReservation.Release()
			info.SimulatedModelCacheInfo = service.ApplySimulatedModelCacheUsageRewrite(usage, service.SimulatedModelCacheUsageRewrite{
				Mode:               "partial_fingerprint",
				MatchRatio:         match.MatchRatio,
				FingerprintVersion: match.FingerprintVersion,
				CandidateCount:     match.CandidateCount,
				MatchDuration:      match.MatchDuration,
			})
		}
	}
	responseUsage := usage
	if matchFound && info.RelayFormat == types.RelayFormatClaude && info.SimulatedModelCacheInfo != nil {
		responseUsageClone := *usage
		responseUsageClone.PromptTokens = info.SimulatedModelCacheInfo.OriginalPromptTokens
		responseUsageClone.InputTokens = info.SimulatedModelCacheInfo.OriginalPromptTokens
		responseUsageClone.TotalTokens = info.SimulatedModelCacheInfo.OriginalPromptTokens + usage.CompletionTokens
		responseUsageClone.UsageSemantic = "anthropic"
		responseUsage = &responseUsageClone
	}
	if matchFound {
		if recorder.stream {
			injected, targetFound, writeErr := recorder.writePatchedStreamTail(responseUsage)
			info.SimulatedModelCacheInfo.StreamUsageInjected = &injected
			if usage.PromptTokensDetails.CachedTokens > 0 && recorder.includeStreamUsage && !targetFound {
				logger.LogWarn(c, fmt.Sprintf("simulated model cache stream usage event not found: request_id=%s format=%s cached_tokens=%d",
					info.RequestId, info.RelayFormat, usage.PromptTokensDetails.CachedTokens))
			}
			if writeErr != nil {
				logger.LogWarn(c, fmt.Sprintf("simulated model cache stream usage write failed: request_id=%s format=%s error=%s",
					info.RequestId, info.RelayFormat, writeErr.Error()))
			}
		} else if !recorder.passThrough {
			body = service.PatchSimulatedModelCacheResponseBody(info.RelayFormat, recorder.Header().Get("Content-Type"), body, responseUsage, simulatedModelCacheResponseModel(info))
		}
	}

	if recorder.stream && (info.SimulatedModelCacheInfo == nil || info.SimulatedModelCacheInfo.StreamUsageInjected == nil) {
		_ = recorder.releaseStreamTail()
	} else if !recorder.stream && !recorder.passThrough {
		flushSimulatedModelCacheRecorder(recorder, body)
	}

	if info.SimulatedModelCacheInfo == nil && attempt.bypassReason != "" {
		info.SimulatedModelCacheInfo = &relaycommon.SimulatedModelCacheInfo{
			FingerprintVersion: service.SimulatedModelCacheFingerprintVersion,
			CandidateCount:     attempt.matchDiagnostics.CandidateCount,
			MatchDurationMS:    attempt.matchDiagnostics.MatchDuration.Milliseconds(),
			BypassReason:       attempt.bypassReason,
		}
	}
}

func simulatedModelCacheOriginalInputTokens(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	inputTokens := usage.PromptTokens
	if inputTokens == 0 && usage.InputTokens > 0 {
		inputTokens = usage.InputTokens
	}
	if usage.UsageSemantic == "anthropic" {
		inputTokens += usage.PromptTokensDetails.CachedTokens + simulatedModelCacheCacheCreationTokens(usage)
	}
	return inputTokens
}

func simulatedModelCacheModelName(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	if model := strings.TrimSpace(info.RequestedModelName); model != "" {
		return model
	}
	if request, ok := info.Request.(*dto.OpenAIResponsesCompactionRequest); ok {
		if model := strings.TrimSpace(request.Model); model != "" {
			return model
		}
	}
	if model := strings.TrimSpace(info.OriginModelName); model != "" {
		return model
	}
	return strings.TrimSpace(info.UpstreamModelName)
}

func restoreSimulatedModelCacheRecorder(c *gin.Context, recorder *simulatedModelCacheRecorder) {
	if recorder != nil {
		if recorder.attempt != nil && recorder.attempt.partialMatch != nil {
			recorder.attempt.partialMatch.Cancel()
			recorder.attempt.partialMatch = nil
		}
		if recorder.stream {
			_ = recorder.releaseStreamTail()
		}
		recorder.releaseResponseReservation()
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
