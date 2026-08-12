package relay

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

var errChannelOutputPrefixTooLarge = errors.New("channel response before effective output exceeds the inspection limit")
var errResponseContentMatched = errors.New("upstream response matched a configured fallback rule")

type channelOutputRecorder struct {
	gin.ResponseWriter
	header               http.Header
	format               types.RelayFormat
	relayMode            int
	model                string
	stream               bool
	retryZeroOutput      bool
	kimiK3VisibleOutput  bool
	multiplier           float64
	bufferLimit          int
	status               int
	statusWritten        bool
	committed            bool
	nonStreamPassThrough bool
	effectiveOutput      bool
	holdingStreamTail    bool
	body                 bytes.Buffer
	streamPending        bytes.Buffer
	streamPrefix         bytes.Buffer
	streamTail           bytes.Buffer
	outputText           strings.Builder
	contentMatcher       *responseContentMatcher
	policyErr            error
}

func newChannelOutputRecorder(writer gin.ResponseWriter, info *relaycommon.RelayInfo, retryZeroOutput bool, contentPolicy operation_setting.ResponseContentRetryPolicy, multiplier float64, bufferLimit int) *channelOutputRecorder {
	recorder := &channelOutputRecorder{
		ResponseWriter:  writer,
		header:          writer.Header().Clone(),
		retryZeroOutput: retryZeroOutput,
		multiplier:      multiplier,
		bufferLimit:     bufferLimit,
		status:          http.StatusOK,
		contentMatcher:  newResponseContentMatcher(contentPolicy),
	}
	if info != nil {
		recorder.format = info.RelayFormat
		recorder.relayMode = info.RelayMode
		recorder.model = simulatedModelCacheModelName(info)
		recorder.stream = info.IsStream
		recorder.kimiK3VisibleOutput = info.IsKimiK3OfficialCompatibility()
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
	if len(data) == 0 {
		return 0, nil
	}
	w.statusWritten = true
	if !w.stream {
		if w.nonStreamPassThrough {
			return w.ResponseWriter.Write(data)
		}
		if w.body.Len()+len(data) > w.bufferLimit {
			if !w.retryZeroOutput {
				w.contentMatcher.disable()
				w.nonStreamPassThrough = true
				w.header.Del("Content-Length")
				if err := w.commitHeader(); err != nil {
					w.policyErr = err
					return 0, err
				}
				if err := w.writeCommitted(w.body.Bytes()); err != nil {
					w.policyErr = err
					return 0, err
				}
				w.body.Reset()
				return w.ResponseWriter.Write(data)
			}
			w.policyErr = errChannelOutputPrefixTooLarge
			return 0, w.policyErr
		}
		return w.body.Write(data)
	}

	if w.streamPending.Len()+w.streamPrefix.Len()+w.streamTail.Len()+len(data) > w.bufferLimit && !w.committed {
		if w.retryZeroOutput && !w.effectiveOutput {
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
		w.policyErr = err
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
		isTail := w.retryZeroOutput && (w.holdingStreamTail || isChannelOutputStreamTailEvent(w.format, event))
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
	if observeOutput && w.observeEffectiveOutput(payload) {
		w.effectiveOutput = true
	}
	observeVisibleResponsePayload(w.format, w.relayMode, payload, w.contentMatcher, true)
	if w.contentMatcher != nil && w.contentMatcher.matched {
		return errResponseContentMatched
	}
	return nil
}

func (w *channelOutputRecorder) finish(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) *types.NewAPIError {
	c.Writer = w.ResponseWriter
	if w.policyErr != nil {
		w.abortCommittedStream()
		return channelOutputPolicyError(w.policyErr)
	}
	if w.nonStreamPassThrough {
		return nil
	}

	if w.stream {
		if err := w.finishPendingStreamBytes(); err != nil {
			w.abortCommittedStream()
			return channelOutputPolicyError(err)
		}
	} else {
		var payload map[string]any
		if common.Unmarshal(w.body.Bytes(), &payload) == nil {
			if w.observeEffectiveOutput(payload) {
				w.effectiveOutput = true
			}
			observeVisibleResponsePayload(w.format, w.relayMode, payload, w.contentMatcher, false)
		}
	}
	if w.contentMatcher != nil && w.contentMatcher.finish() {
		return channelResponseContentMatchError()
	}

	estimatedOutput := false
	if w.retryZeroOutput {
		normalized := service.NormalizeUsageForBilling(usage)
		if normalized.OutputTokens == 0 && !w.effectiveOutput {
			return channelZeroOutputError(errors.New("upstream returned zero output tokens without effective output"))
		}
		if normalized.OutputTokens == 0 {
			baseEstimate := service.EstimateTokenByModel(w.model, w.outputText.String())
			if baseEstimate <= 0 {
				baseEstimate = 1
			}
			estimatedTokens := scaleMissingTokenEstimate(info, baseEstimate, w.multiplier)
			if estimatedTokens <= 0 {
				estimatedTokens = 1
			}
			estimatedOutput = service.ApplyMissingOutputEstimate(usage, estimatedTokens)
		}
	}

	if !w.stream {
		body := w.body.Bytes()
		if estimatedOutput {
			body = service.PatchSimulatedModelCacheResponseBody(w.format, w.header.Get("Content-Type"), body, usage, simulatedModelCacheResponseModel(info))
		}
		w.header.Set("Content-Length", strconv.Itoa(len(body)))
		if err := w.commitHeader(); err != nil {
			return channelZeroOutputError(err)
		}
		if _, err := w.ResponseWriter.Write(body); err != nil {
			return channelZeroOutputError(err)
		}
		return nil
	}

	if !w.committed {
		if err := w.commitHeader(); err != nil {
			return channelZeroOutputError(err)
		}
		if err := w.writeCommitted(w.streamPrefix.Bytes()); err != nil {
			return channelZeroOutputError(err)
		}
		w.streamPrefix.Reset()
	}
	tail := w.streamTail.Bytes()
	if estimatedOutput && len(tail) > 0 {
		tail = service.PatchSimulatedModelCacheResponseBody(w.format, "text/event-stream", tail, usage, simulatedModelCacheResponseModel(info))
	}
	if err := w.writeCommitted(tail); err != nil {
		return channelZeroOutputError(err)
	}
	w.streamTail.Reset()
	w.ResponseWriter.Flush()
	return nil
}

func (w *channelOutputRecorder) observeEffectiveOutput(payload map[string]any) bool {
	if w.kimiK3VisibleOutput {
		return observeKimiK3VisibleOutputPayload(w.format, w.relayMode, payload, &w.outputText)
	}
	return observeChannelOutputPayload(w.format, w.relayMode, payload, &w.outputText)
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
	if w.retryZeroOutput && (w.holdingStreamTail || isChannelOutputStreamTailEvent(w.format, pending)) {
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
	written, err := w.ResponseWriter.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func channelZeroOutputError(err error) *types.NewAPIError {
	return types.NewErrorWithStatusCode(err, types.ErrorCodeChannelZeroOutput, http.StatusServiceUnavailable)
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

func scaleMissingTokenEstimate(info *relaycommon.RelayInfo, baseTokens int, multiplier float64) int {
	if baseTokens <= 0 {
		return 0
	}
	value := math.Ceil(float64(baseTokens) * multiplier)
	estimatedTokens, clamp := common.QuotaFromFloatChecked(value)
	if clamp != nil && info != nil && info.QuotaClamp == nil {
		info.QuotaClamp = clamp
	}
	return estimatedTokens
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

func observeKimiK3VisibleOutputPayload(format types.RelayFormat, relayMode int, payload map[string]any, output *strings.Builder) bool {
	switch format {
	case types.RelayFormatOpenAI:
		return observeKimiK3OpenAIVisibleOutput(payload, relayMode, output)
	case types.RelayFormatOpenAIResponses:
		return observeKimiK3ResponsesVisibleOutput(payload, output)
	case types.RelayFormatClaude:
		return observeKimiK3ClaudeVisibleOutput(payload, output)
	default:
		return observeChannelOutputPayload(format, relayMode, payload, output)
	}
}

func observeKimiK3OpenAIVisibleOutput(payload map[string]any, relayMode int, output *strings.Builder) bool {
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
			value, _ := choice[key].(map[string]any)
			if value == nil {
				continue
			}
			if observeSemanticOutputValue(value["content"], output) {
				effective = true
			}
			for _, toolKey := range []string{"tool_calls", "function_call"} {
				if hasOutputValue(value[toolKey]) {
					appendOutputMarker(output, "tool")
					effective = true
				}
			}
		}
	}
	return effective
}

func observeKimiK3ResponsesVisibleOutput(payload map[string]any, output *strings.Builder) bool {
	effective := observeKimiK3ResponsesOutputItems(payload["output"], output)
	if response, ok := payload["response"].(map[string]any); ok && observeKimiK3ResponsesOutputItems(response["output"], output) {
		effective = true
	}
	eventType, _ := payload["type"].(string)
	if strings.Contains(eventType, "output_text") && observeSemanticOutputValue(payload["delta"], output) {
		effective = true
	}
	if strings.Contains(eventType, "function_call") || strings.Contains(eventType, "computer_call") {
		appendOutputMarker(output, "tool")
		effective = true
	}
	if item, ok := payload["item"].(map[string]any); ok && observeKimiK3ResponsesOutputItems([]any{item}, output) {
		effective = true
	}
	return effective
}

func observeKimiK3ResponsesOutputItems(value any, output *strings.Builder) bool {
	items, _ := value.([]any)
	effective := false
	for _, itemValue := range items {
		item, _ := itemValue.(map[string]any)
		itemType, _ := item["type"].(string)
		switch itemType {
		case "message":
			if observeSemanticOutputValue(item["content"], output) {
				effective = true
			}
		case "function_call", "custom_tool_call", "computer_call":
			appendOutputMarker(output, "tool")
			effective = true
		}
	}
	return effective
}

func observeKimiK3ClaudeVisibleOutput(payload map[string]any, output *strings.Builder) bool {
	effective := appendOutputString(output, payload["completion"])
	if blocks, ok := payload["content"].([]any); ok {
		for _, blockValue := range blocks {
			block, _ := blockValue.(map[string]any)
			if observeKimiK3ClaudeVisibleBlock(block, output) {
				effective = true
			}
		}
	}
	for _, key := range []string{"content_block", "delta"} {
		block, _ := payload[key].(map[string]any)
		if observeKimiK3ClaudeVisibleBlock(block, output) {
			effective = true
		}
	}
	return effective
}

func observeKimiK3ClaudeVisibleBlock(block map[string]any, output *strings.Builder) bool {
	if block == nil {
		return false
	}
	blockType, _ := block["type"].(string)
	switch blockType {
	case "text", "text_delta":
		return appendOutputString(output, block["text"])
	case "tool_use", "server_tool_use":
		appendOutputMarker(output, "tool")
		return true
	default:
		return false
	}
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
