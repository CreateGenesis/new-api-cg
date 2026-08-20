package relay

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

var errChannelOutputPrefixTooLarge = errors.New("channel response before effective output exceeds the inspection limit")
var errResponseContentMatched = errors.New("upstream response matched a configured fallback rule")

type channelOutputRecorder struct {
	gin.ResponseWriter
	header               http.Header
	format               types.RelayFormat
	info                 *relaycommon.RelayInfo
	relayMode            int
	stream               bool
	retryZeroOutput      bool
	validateUsage        bool
	estimateUsage        bool
	bufferLimit          int
	status               int
	statusWritten        bool
	committed            bool
	nonStreamPassThrough bool
	effectiveOutput      bool
	inputUsageObserved   bool
	outputUsageObserved  bool
	holdingStreamTail    bool
	body                 bytes.Buffer
	streamPending        bytes.Buffer
	streamPrefix         bytes.Buffer
	streamTail           bytes.Buffer
	outputText           strings.Builder
	contentMatcher       *responseContentMatcher
	policyErr            error
	deliveryErr          error
	usageEstimated       bool
	usagePrepared        bool
}

func newChannelOutputRecorder(writer gin.ResponseWriter, info *relaycommon.RelayInfo, retryZeroOutput bool, validateUsage bool, contentPolicy operation_setting.ResponseContentRetryPolicy, bufferLimit int) *channelOutputRecorder {
	recorder := &channelOutputRecorder{
		ResponseWriter:  writer,
		header:          writer.Header().Clone(),
		info:            info,
		retryZeroOutput: retryZeroOutput,
		validateUsage:   validateUsage,
		bufferLimit:     bufferLimit,
		status:          http.StatusOK,
		contentMatcher:  newResponseContentMatcher(contentPolicy),
	}
	if info != nil {
		recorder.format = info.RelayFormat
		recorder.relayMode = info.RelayMode
		recorder.stream = info.IsStream
		_, recorder.estimateUsage = info.UsageEstimationSettings()
	}
	return recorder
}

func (w *channelOutputRecorder) Header() http.Header {
	return w.header
}

func (w *channelOutputRecorder) WriteHeader(code int) {
	if w.statusWritten {
		return
	}
	if code < http.StatusContinue || code > 999 {
		code = http.StatusOK
	}
	w.status = code
	w.statusWritten = true
}

func (w *channelOutputRecorder) WriteHeaderNow() {
	if w.committed {
		w.ResponseWriter.WriteHeaderNow()
		return
	}
	w.statusWritten = true
}

func (w *channelOutputRecorder) Write(data []byte) (int, error) {
	if w.policyErr != nil {
		return 0, w.policyErr
	}
	if w.deliveryErr != nil {
		return 0, w.deliveryErr
	}
	if len(data) == 0 {
		return 0, nil
	}
	w.statusWritten = true
	if !w.stream {
		if w.nonStreamPassThrough {
			return w.writeDownstream(data)
		}
		if w.body.Len()+len(data) > w.bufferLimit {
			if !w.retryZeroOutput && !w.validateUsage {
				w.contentMatcher.disable()
				w.nonStreamPassThrough = true
				w.header.Del("Content-Length")
				if err := w.commitHeader(); err != nil {
					w.policyErr = err
					return 0, err
				}
				if err := w.writeCommitted(w.body.Bytes()); err != nil {
					return 0, err
				}
				w.body.Reset()
				return w.writeDownstream(data)
			}
			w.policyErr = errChannelOutputPrefixTooLarge
			return 0, w.policyErr
		}
		return w.body.Write(data)
	}

	if w.streamPending.Len()+w.streamPrefix.Len()+w.streamTail.Len()+len(data) > w.bufferLimit && !w.committed {
		if (w.retryZeroOutput && !w.effectiveOutput) || (!w.committed && (w.validateUsage || w.estimateUsage)) {
			w.policyErr = errChannelOutputPrefixTooLarge
			return 0, w.policyErr
		}
		w.contentMatcher.disable()
		if w.readyToCommitStream() {
			if err := w.commitBufferedStream(); err != nil {
				w.policyErr = err
				return 0, err
			}
		}
	}
	_, _ = w.streamPending.Write(data)
	if err := w.processStreamEvents(); err != nil {
		if w.deliveryErr == nil {
			w.policyErr = err
		}
		return 0, err
	}
	return len(data), nil
}

func (w *channelOutputRecorder) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

func (w *channelOutputRecorder) Status() int {
	if !w.committed {
		return w.status
	}
	return w.ResponseWriter.Status()
}

func (w *channelOutputRecorder) Size() int {
	return w.ResponseWriter.Size()
}

func (w *channelOutputRecorder) Written() bool {
	return w.ResponseWriter.Written()
}

func (w *channelOutputRecorder) Flush() {
	if w.committed {
		w.ResponseWriter.Flush()
	}
}

func (w *channelOutputRecorder) processStreamEvents() error {
	for {
		boundaryIndex, boundaryLength := simulatedModelCacheSSEBoundary(w.streamPending.Bytes())
		if boundaryIndex < 0 {
			return nil
		}
		event := w.streamPending.Next(boundaryIndex + boundaryLength)
		isTail := (w.retryZeroOutput || w.validateUsage || w.estimateUsage) && (w.holdingStreamTail || isChannelOutputStreamTailEvent(w.format, event))
		observeOutput := !shouldSkipTerminalOutputObservation(w.format, event, w.effectiveOutput)
		if err := w.observeStreamEvent(event, observeOutput); err != nil {
			return err
		}
		if isTail {
			w.holdingStreamTail = true
			_, _ = w.streamTail.Write(event)
			continue
		}
		if w.committed {
			if err := w.writeCommitted(event); err != nil {
				return err
			}
			continue
		}
		_, _ = w.streamPrefix.Write(event)
		if w.readyToCommitStream() {
			if err := w.commitBufferedStream(); err != nil {
				return err
			}
		}
	}
}

func (w *channelOutputRecorder) observeStreamEvent(event []byte, observeOutput bool) error {
	data, ok := simulatedModelCacheSSEData(event)
	if !ok || string(data) == "[DONE]" {
		return nil
	}
	var payload map[string]any
	if common.Unmarshal(data, &payload) != nil {
		return nil
	}
	w.observeUsagePresence(payload)
	effectiveOutput := observeOutput && w.observeEffectiveOutput(payload)
	if effectiveOutput {
		w.effectiveOutput = true
	}
	observeVisibleResponsePayload(w.format, w.relayMode, payload, w.contentMatcher, true)
	if effectiveOutput && w.contentMatcher != nil && !w.contentMatcher.hasVisibleText() &&
		isImmediateNonVisibleStreamOutput(w.format, payload) {
		w.contentMatcher.disable()
	}
	if w.contentMatcher != nil && w.contentMatcher.matched {
		return errResponseContentMatched
	}
	return nil
}

func (w *channelOutputRecorder) finish(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) *types.NewAPIError {
	c.Writer = w.ResponseWriter
	clientGone := false
	if w.deliveryErr != nil {
		w.markClientGone(c, info)
		clientGone = true
	}
	if w.stream && c.Request != nil && c.Request.Context().Err() != nil {
		w.markClientGone(c, info)
		clientGone = true
	}
	if w.stream && info != nil && info.StreamStatus != nil && info.StreamStatus.IsClientGone() {
		clientGone = true
	}
	if !clientGone && w.policyErr != nil {
		w.abortCommittedStream()
		return channelOutputPolicyError(w.policyErr)
	}
	if !clientGone && w.nonStreamPassThrough {
		return nil
	}

	if w.stream && !clientGone {
		if err := w.finishPendingStreamBytes(); err != nil {
			if w.deliveryErr != nil {
				w.markClientGone(c, info)
				return nil
			}
			w.abortCommittedStream()
			return channelOutputPolicyError(err)
		}
	}
	if w.estimateUsage {
		w.prepareUsageEstimation(c, info, usage, nil)
	}

	usageModified := false
	if w.validateUsage {
		var err error
		usageModified, err = service.ApplyTextUsagePolicy(c, info, usage)
		if err != nil {
			if w.committed {
				w.streamTail.Reset()
				w.streamPending.Reset()
				if info != nil && info.StreamStatus != nil {
					info.StreamStatus.RecordError(err.Error())
				}
				w.ResponseWriter.Flush()
				return channelCommittedUsageError(err)
			}
			return channelZeroOutputError(err)
		}
	}
	if clientGone {
		return nil
	}

	if !w.stream && !w.usagePrepared {
		if err := w.observeNonStreamBody(w.body.Bytes()); err != nil {
			return channelOutputPolicyError(err)
		}
	}
	streamInterrupted := false
	if w.stream && info != nil && info.StreamStatus != nil {
		streamStatus := info.StreamStatus.Snapshot()
		streamInterrupted = streamStatus.EndReason != relaycommon.StreamEndReasonNone && info.StreamStatus.IsInterrupted()
	}
	if !streamInterrupted && w.contentMatcher != nil && w.contentMatcher.finish() {
		return channelResponseContentMatchError()
	}

	inputUsageReported := usage != nil && (w.inputUsageObserved || usage.UpstreamInputReported || usage.EstimatedInput)
	// A reported input does not make an empty response valid; retry every completed attempt with no effective output.
	if w.retryZeroOutput && !streamInterrupted && !w.effectiveOutput && inputUsageReported && usage != nil && (!usage.Estimated || info != nil && info.UsageEstimationAudit != nil) {
		return channelZeroOutputError(errors.New("upstream returned no effective output"))
	}

	if w.validateUsage && !streamInterrupted {
		var err error
		if usage != nil && info != nil && info.ChannelOtherSettings.UsageTokenLimit != nil {
			limits := info.ChannelOtherSettings.UsageTokenLimit
			normalized := service.NormalizeUsageForBilling(usage)
			inputReported := w.inputUsageObserved || usage.UpstreamInputReported || usage.EstimatedInput
			outputReported := w.outputUsageObserved || usage.UpstreamOutputReported || usage.EstimatedOutput
			if limits.InputTokens > 0 && inputReported && normalized.InputTokens.TotalInputTokens <= 0 && !usage.EstimatedInput {
				err = service.ErrUpstreamUsageMissingInput
			} else if limits.OutputTokens > 0 && outputReported && normalized.OutputTokens <= 0 && !usage.EstimatedOutput {
				err = service.ErrUpstreamUsageMissingOutput
			}
		}
		if err != nil {
			if w.committed {
				w.streamTail.Reset()
				w.streamPending.Reset()
				if info != nil && info.StreamStatus != nil {
					info.StreamStatus.RecordError(err.Error())
				}
				w.ResponseWriter.Flush()
				return channelCommittedUsageError(err)
			}
			return channelZeroOutputError(err)
		}
	}

	if !w.stream {
		body := w.body.Bytes()
		if usageModified || w.usageEstimated {
			body = service.PatchUsageResponseBody(w.format, w.header.Get("Content-Type"), body, usage, simulatedModelCacheResponseModel(info), w.usageEstimated)
		}
		w.header.Set("Content-Length", strconv.Itoa(len(body)))
		if err := w.commitHeader(); err != nil {
			return channelZeroOutputError(err)
		}
		if _, err := w.writeDownstream(body); err != nil {
			w.markClientGone(c, info)
			return nil
		}
		return nil
	}

	if !w.committed {
		if err := w.commitHeader(); err != nil {
			return channelZeroOutputError(err)
		}
		if err := w.writeCommitted(w.streamPrefix.Bytes()); err != nil {
			w.markClientGone(c, info)
			return nil
		}
		w.streamPrefix.Reset()
	}
	tail := w.streamTail.Bytes()
	if (usageModified || w.usageEstimated) && len(tail) > 0 {
		ensureUsage := w.usageEstimated && (w.format != types.RelayFormatOpenAI || info == nil || info.ShouldIncludeUsage)
		tail = service.PatchUsageResponseBody(w.format, "text/event-stream", tail, usage, simulatedModelCacheResponseModel(info), ensureUsage)
	}
	if err := w.writeCommitted(tail); err != nil {
		w.markClientGone(c, info)
		return nil
	}
	w.streamTail.Reset()
	w.ResponseWriter.Flush()
	return nil
}

func (w *channelOutputRecorder) prepareUsageEstimation(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, externalBody []byte) {
	if w.usagePrepared || !w.estimateUsage || usage == nil {
		return
	}
	if !w.stream {
		body := w.body.Bytes()
		if len(externalBody) > 0 {
			body = externalBody
		}
		if len(body) > 0 {
			var payload map[string]any
			if common.Unmarshal(body, &payload) == nil {
				w.observeUsagePresence(payload)
				if w.observeEffectiveOutput(payload) {
					w.effectiveOutput = true
				}
				observeVisibleResponsePayload(w.format, w.relayMode, payload, w.contentMatcher, false)
			}
		}
	}
	w.usagePrepared = true
	w.usageEstimated = applyChannelUsageEstimation(c, info, usage, w.outputText.String(), w.effectiveOutput)
}

func (w *channelOutputRecorder) prepareUsageEstimationFromBody(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, body []byte) {
	if !w.estimateUsage || w.usagePrepared {
		return
	}
	w.prepareUsageEstimation(c, info, usage, body)
}

func (w *channelOutputRecorder) observeEffectiveOutput(payload map[string]any) bool {
	return observeChannelOutputPayload(w.format, w.relayMode, payload, &w.outputText)
}

func (w *channelOutputRecorder) observeNonStreamBody(body []byte) error {
	var payload map[string]any
	if common.Unmarshal(body, &payload) == nil {
		w.observeUsagePresence(payload)
		if w.observeEffectiveOutput(payload) {
			w.effectiveOutput = true
		}
		observeVisibleResponsePayload(w.format, w.relayMode, payload, w.contentMatcher, false)
		return nil
	}
	// A few upstreams return SSE even for a non-stream request. Inspect each
	// complete event so an error envelope is still eligible for content retry.
	for _, event := range splitSimulatedModelCacheSSEChunks(body) {
		if err := w.observeStreamEvent(event, true); err != nil {
			if errors.Is(err, errResponseContentMatched) {
				w.policyErr = err
			}
			return err
		}
	}
	return nil
}

func (w *channelOutputRecorder) observeResponseError(c *gin.Context, err *types.NewAPIError) {
	// Buffered adaptors can consume an upstream error event before writing it.
	if w == nil || !responseContentRetryEligibleError(c, err) || w.committed || w.policyErr != nil || w.contentMatcher == nil {
		return
	}
	w.contentMatcher.append(err.Error())
	if w.contentMatcher.finish() {
		w.policyErr = errResponseContentMatched
	}
}

func responseContentRetryEligibleError(c *gin.Context, err *types.NewAPIError) bool {
	if err == nil || types.IsSkipRetryError(err) {
		return false
	}

	code := err.StatusCode
	if code >= http.StatusOK && code < http.StatusMultipleChoices {
		return true
	}
	if code < http.StatusContinue || code > 599 {
		return false
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return true
	}

	ranges := operation_setting.AutomaticRetryStatusCodeRanges
	if c != nil {
		if settings, ok := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting); ok &&
			settings.StatusCodeRetry != nil && settings.StatusCodeRetry.Enabled {
			normalized := settings.StatusCodeRetry.Normalize()
			if channelRanges, parseErr := operation_setting.ParseHTTPStatusCodeRanges(normalized.StatusCodes); parseErr == nil {
				ranges = channelRanges
			}
		}
	}
	return !operation_setting.ShouldRetryByStatusCodeRanges(ranges, code)
}

func (w *channelOutputRecorder) observeUsagePresence(payload map[string]any) {
	usageMaps := make([]map[string]any, 0, 4)
	if usage, ok := payload["usage"].(map[string]any); ok {
		usageMaps = append(usageMaps, usage)
	}
	if usage, ok := payload["usageMetadata"].(map[string]any); ok {
		usageMaps = append(usageMaps, usage)
	}
	for _, parentKey := range []string{"message", "response"} {
		parent, ok := payload[parentKey].(map[string]any)
		if !ok {
			continue
		}
		if usage, ok := parent["usage"].(map[string]any); ok {
			usageMaps = append(usageMaps, usage)
		}
	}
	for _, usage := range usageMaps {
		for _, field := range []string{"prompt_tokens", "input_tokens", "promptTokenCount", "toolUsePromptTokenCount"} {
			if value, exists := usage[field]; exists && value != nil {
				w.inputUsageObserved = true
				break
			}
		}
		for _, field := range []string{"completion_tokens", "output_tokens", "candidatesTokenCount", "thoughtsTokenCount"} {
			if value, exists := usage[field]; exists && value != nil {
				w.outputUsageObserved = true
				break
			}
		}
	}
}

func (w *channelOutputRecorder) finishPendingStreamBytes() error {
	if w.streamPending.Len() == 0 {
		return nil
	}
	pending := append([]byte(nil), w.streamPending.Bytes()...)
	w.streamPending.Reset()
	observeOutput := !shouldSkipTerminalOutputObservation(w.format, pending, w.effectiveOutput)
	if err := w.observeStreamEvent(pending, observeOutput); err != nil {
		return err
	}
	if (w.retryZeroOutput || w.validateUsage || w.estimateUsage) &&
		(w.holdingStreamTail || isChannelOutputStreamTailEvent(w.format, pending)) {
		w.holdingStreamTail = true
		_, _ = w.streamTail.Write(pending)
		return nil
	}
	if w.committed {
		return w.writeCommitted(pending)
	}
	_, _ = w.streamPrefix.Write(pending)
	if w.readyToCommitStream() {
		if err := w.commitBufferedStream(); err != nil {
			return err
		}
	}
	return nil
}

func (w *channelOutputRecorder) readyToCommitStream() bool {
	if w.contentMatcher != nil && !w.contentMatcher.resolvedWithoutMatch() {
		return false
	}
	return !w.retryZeroOutput || w.effectiveOutput
}

func (w *channelOutputRecorder) commitBufferedStream() error {
	if err := w.commitHeader(); err != nil {
		return err
	}
	if err := w.writeCommitted(w.streamPrefix.Bytes()); err != nil {
		return err
	}
	w.streamPrefix.Reset()
	w.ResponseWriter.Flush()
	return nil
}

func (w *channelOutputRecorder) abort(c *gin.Context) {
	c.Writer = w.ResponseWriter
	if !w.committed {
		return
	}
	w.abortCommittedStream()
}

func (w *channelOutputRecorder) abortCommittedStream() {
	if !w.committed {
		return
	}
	_ = w.writeCommitted(w.streamTail.Bytes())
	_ = w.writeCommitted(w.streamPending.Bytes())
	w.ResponseWriter.Flush()
}

func (w *channelOutputRecorder) commitHeader() error {
	if w.committed {
		return nil
	}
	target := w.ResponseWriter.Header()
	for key := range target {
		delete(target, key)
	}
	for key, values := range w.header {
		target[key] = append([]string(nil), values...)
	}
	if w.stream {
		target.Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.committed = true
	return nil
}

func (w *channelOutputRecorder) writeCommitted(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	_, err := w.writeDownstream(data)
	if err != nil {
		return err
	}
	return nil
}

func (w *channelOutputRecorder) writeDownstream(data []byte) (int, error) {
	written, err := w.ResponseWriter.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil && w.deliveryErr == nil {
		w.deliveryErr = err
		w.markClientGone(nil, w.info)
	}
	return written, err
}

func (w *channelOutputRecorder) markClientGone(c *gin.Context, info *relaycommon.RelayInfo) {
	if info == nil || info.StreamStatus == nil {
		return
	}
	err := w.deliveryErr
	if err == nil && c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		err = c.Request.Context().Err()
	}
	info.StreamStatus.MarkClientGone(err)
}

func channelZeroOutputError(err error) *types.NewAPIError {
	return types.NewErrorWithStatusCode(err, types.ErrorCodeChannelZeroOutput, http.StatusServiceUnavailable)
}

func channelCommittedUsageError(err error) *types.NewAPIError {
	return types.NewError(
		err,
		types.ErrorCodeChannelZeroOutput,
		types.ErrOptionWithStatusCode(http.StatusServiceUnavailable),
		types.ErrOptionWithSkipRetry(),
	)
}

func channelResponseContentMatchError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(errResponseContentMatched, types.ErrorCodeChannelResponseContentMatch, http.StatusServiceUnavailable)
}

func channelOutputPolicyError(err error) *types.NewAPIError {
	if errors.Is(err, errResponseContentMatched) {
		return channelResponseContentMatchError()
	}
	return channelZeroOutputError(err)
}

func isChannelOutputStreamTailEvent(format types.RelayFormat, event []byte) bool {
	data, ok := simulatedModelCacheSSEData(event)
	if !ok {
		return false
	}
	if string(data) == "[DONE]" {
		return true
	}
	var payload map[string]any
	if common.Unmarshal(data, &payload) != nil {
		return false
	}
	eventType, _ := payload["type"].(string)
	switch format {
	case types.RelayFormatOpenAI:
		_, hasUsage := payload["usage"]
		choices, _ := payload["choices"].([]any)
		return hasUsage && len(choices) == 0
	case types.RelayFormatOpenAIResponses:
		return eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.failed"
	case types.RelayFormatClaude:
		return eventType == "message_delta" || eventType == "message_stop"
	case types.RelayFormatGemini:
		_, hasUsage := payload["usageMetadata"]
		candidates, _ := payload["candidates"].([]any)
		return hasUsage && len(candidates) == 0
	default:
		return false
	}
}

func shouldSkipTerminalOutputObservation(format types.RelayFormat, event []byte, alreadyEffective bool) bool {
	if format != types.RelayFormatOpenAIResponses || !alreadyEffective {
		return false
	}
	data, ok := simulatedModelCacheSSEData(event)
	if !ok {
		return false
	}
	var payload struct {
		Type string `json:"type"`
	}
	return common.Unmarshal(data, &payload) == nil && payload.Type == "response.completed"
}

func observeChannelOutputPayload(format types.RelayFormat, relayMode int, payload map[string]any, output *strings.Builder) bool {
	switch format {
	case types.RelayFormatOpenAI:
		return observeOpenAIOutput(payload, relayMode, output)
	case types.RelayFormatOpenAIResponses:
		return observeResponsesOutput(payload, output)
	case types.RelayFormatClaude:
		return observeClaudeOutput(payload, output)
	case types.RelayFormatGemini:
		return observeGeminiOutput(payload, output)
	default:
		return false
	}
}

func isImmediateNonVisibleStreamOutput(format types.RelayFormat, payload map[string]any) bool {
	switch format {
	case types.RelayFormatOpenAI:
		choices, _ := payload["choices"].([]any)
		for _, choiceValue := range choices {
			choice, _ := choiceValue.(map[string]any)
			for _, key := range []string{"message", "delta"} {
				message, _ := choice[key].(map[string]any)
				if hasOutputValue(message["reasoning"]) || hasOutputValue(message["reasoning_content"]) ||
					hasOutputValue(message["thinking"]) || hasOutputValue(message["tool_calls"]) ||
					hasOutputValue(message["function_call"]) {
					return true
				}
			}
		}
	case types.RelayFormatOpenAIResponses:
		eventType, _ := payload["type"].(string)
		if strings.Contains(eventType, "reasoning") || strings.Contains(eventType, "function_call") ||
			strings.Contains(eventType, "computer_call") || strings.Contains(eventType, "image_generation") ||
			strings.Contains(eventType, "code_interpreter") {
			return true
		}
	case types.RelayFormatClaude:
		for _, key := range []string{"content_block", "delta"} {
			block, _ := payload[key].(map[string]any)
			blockType, _ := block["type"].(string)
			if blockType == "thinking" || blockType == "thinking_delta" || blockType == "redacted_thinking" ||
				blockType == "tool_use" || blockType == "server_tool_use" {
				return true
			}
		}
	case types.RelayFormatGemini:
		candidates, _ := payload["candidates"].([]any)
		for _, candidateValue := range candidates {
			candidate, _ := candidateValue.(map[string]any)
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, partValue := range parts {
				part, _ := partValue.(map[string]any)
				thought, _ := part["thought"].(bool)
				if (thought && hasOutputValue(part["text"])) || hasOutputValue(part["functionCall"]) {
					return true
				}
			}
		}
	}
	return false
}

func observeOpenAIOutput(payload map[string]any, relayMode int, output *strings.Builder) bool {
	choices, _ := payload["choices"].([]any)
	effective := false
	for _, choiceValue := range choices {
		choice, _ := choiceValue.(map[string]any)
		if choice == nil {
			continue
		}
		if relayMode == relayconstant.RelayModeCompletions && appendOutputString(output, choice["text"]) {
			effective = true
		}
		for _, key := range []string{"message", "delta"} {
			if value, ok := choice[key].(map[string]any); ok && observeSemanticOutputObject(value, output) {
				effective = true
			}
		}
	}
	return effective
}

func observeResponsesOutput(payload map[string]any, output *strings.Builder) bool {
	effective := false
	if values, ok := payload["output"].([]any); ok {
		for _, value := range values {
			if item, ok := value.(map[string]any); ok && observeSemanticOutputObject(item, output) {
				effective = true
			}
		}
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if values, ok := response["output"].([]any); ok {
			for _, value := range values {
				if item, ok := value.(map[string]any); ok && observeSemanticOutputObject(item, output) {
					effective = true
				}
			}
		}
	}
	eventType, _ := payload["type"].(string)
	for _, key := range []string{"item", "part"} {
		if observeSemanticOutputValue(payload[key], output) {
			effective = true
		}
	}
	if strings.Contains(eventType, "output_text") || strings.Contains(eventType, "reasoning") ||
		strings.Contains(eventType, "function_call") || strings.Contains(eventType, "image_generation") ||
		strings.Contains(eventType, "computer_call") || strings.Contains(eventType, "code_interpreter") {
		for _, key := range []string{"delta", "text", "arguments", "partial_image_b64"} {
			if observeSemanticOutputValue(payload[key], output) {
				effective = true
			}
		}
		if strings.Contains(eventType, "function_call") || strings.Contains(eventType, "computer_call") {
			appendOutputMarker(output, "tool")
			effective = true
		}
	}
	return effective
}

func observeClaudeOutput(payload map[string]any, output *strings.Builder) bool {
	effective := appendOutputString(output, payload["completion"])
	if values, ok := payload["content"].([]any); ok {
		for _, value := range values {
			if block, ok := value.(map[string]any); ok && observeSemanticOutputObject(block, output) {
				effective = true
			}
		}
	}
	for _, key := range []string{"content_block", "delta"} {
		if value, ok := payload[key].(map[string]any); ok && observeSemanticOutputObject(value, output) {
			effective = true
		}
	}
	return effective
}

func observeGeminiOutput(payload map[string]any, output *strings.Builder) bool {
	candidates, _ := payload["candidates"].([]any)
	effective := false
	for _, candidateValue := range candidates {
		candidate, _ := candidateValue.(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, partValue := range parts {
			if part, ok := partValue.(map[string]any); ok && observeSemanticOutputObject(part, output) {
				effective = true
			}
		}
	}
	return effective
}

func observeSemanticOutputValue(value any, output *strings.Builder) bool {
	switch typed := value.(type) {
	case string:
		return appendOutputString(output, typed)
	case []any:
		effective := false
		for _, item := range typed {
			if observeSemanticOutputValue(item, output) {
				effective = true
			}
		}
		return effective
	case map[string]any:
		return observeSemanticOutputObject(typed, output)
	default:
		return false
	}
}

func observeSemanticOutputObject(value map[string]any, output *strings.Builder) bool {
	effective := false
	for _, key := range []string{
		"text", "content", "output_text", "reasoning", "reasoning_content", "thinking", "summary",
		"arguments", "partial_json", "delta", "transcript", "code", "result", "refusal",
	} {
		if observeSemanticOutputValue(value[key], output) {
			effective = true
		}
	}
	for _, key := range []string{"tool_calls", "function_call", "functionCall", "tool_use", "server_tool_use", "computer_call"} {
		if hasOutputValue(value[key]) {
			appendOutputMarker(output, "tool")
			_ = observeSemanticOutputValue(value[key], output)
			effective = true
		}
	}
	for _, key := range []string{"inlineData", "inline_data", "fileData", "image_url", "audio", "media", "partial_image_b64"} {
		if hasOutputValue(value[key]) {
			appendOutputMarker(output, "media")
			effective = true
		}
	}
	blockType, _ := value["type"].(string)
	if strings.Contains(blockType, "tool_use") || strings.Contains(blockType, "function_call") ||
		strings.Contains(blockType, "computer_call") || strings.Contains(blockType, "image_generation") {
		appendOutputMarker(output, "tool")
		effective = true
	}
	return effective
}

func appendOutputString(output *strings.Builder, value any) bool {
	text, ok := value.(string)
	if !ok || text == "" {
		return false
	}
	output.WriteString(text)
	output.WriteByte('\n')
	return true
}

func appendOutputMarker(output *strings.Builder, marker string) {
	output.WriteByte('[')
	output.WriteString(marker)
	output.WriteString("]\n")
}

func hasOutputValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}
