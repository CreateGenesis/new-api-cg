package openai

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type tntResponsesResponse struct {
	*dto.OpenAIResponsesResponse
	MaxOutputTokens *uint `json:"max_output_tokens"`
}

type tntResponsesStreamPayload struct {
	dto.ResponsesStreamResponse
	Response       *tntResponsesResponse `json:"response,omitempty"`
	SequenceNumber int                   `json:"sequence_number"`
	Text           *string               `json:"text,omitempty"`
	Arguments      *string               `json:"arguments,omitempty"`
	Name           *string               `json:"name,omitempty"`
	LogProbs       *[]any                `json:"logprobs,omitempty"`
}

func responsesResponseForKimiK3Request(info *relaycommon.RelayInfo, response *dto.OpenAIResponsesResponse) *tntResponsesResponse {
	if info == nil || response == nil || (!info.IsTNTTencentOpenAIConversion() && !info.IsKimiK3OfficialCompatibility()) {
		return nil
	}
	request, ok := info.Request.(*dto.OpenAIResponsesRequest)
	if !ok || request == nil {
		return nil
	}

	response.Temperature = 1
	if request.Temperature != nil {
		response.Temperature = *request.Temperature
	}
	response.TopP = 1
	if info.IsKimiK3OfficialCompatibility() {
		response.TopP = 0.95
	}
	if request.TopP != nil {
		response.TopP = *request.TopP
	}
	response.Instructions = request.Instructions
	response.Tools = request.GetToolsMap()
	if response.Tools == nil {
		response.Tools = make([]map[string]any, 0)
	}
	response.ToolChoice = request.ToolChoice
	if len(response.ToolChoice) == 0 {
		response.ToolChoice = []byte(`"auto"`)
	}
	response.Truncation = request.Truncation
	if len(response.Truncation) == 0 {
		response.Truncation = []byte(`"disabled"`)
	}
	response.ParallelToolCalls = true
	if len(request.ParallelToolCalls) > 0 && common.GetJsonType(request.ParallelToolCalls) == "boolean" {
		_ = common.Unmarshal(request.ParallelToolCalls, &response.ParallelToolCalls)
	}
	response.Reasoning = request.Reasoning
	if response.Reasoning == nil && info.IsKimiK3OfficialCompatibility() {
		response.Reasoning = &dto.Reasoning{Effort: "max"}
	}
	response.User = request.User
	response.Metadata = request.Metadata
	if len(response.Metadata) == 0 {
		response.Metadata = []byte(`{}`)
	}

	maxOutputTokens := request.MaxOutputTokens
	if maxOutputTokens == nil && info.IsKimiK3OfficialCompatibility() {
		maxOutputTokens = common.GetPointer(uint(131072))
	}
	return &tntResponsesResponse{
		OpenAIResponsesResponse: response,
		MaxOutputTokens:         maxOutputTokens,
	}
}

func MarshalKimiK3ResponsesResponse(info *relaycommon.RelayInfo, response *dto.OpenAIResponsesResponse) ([]byte, error) {
	restored := responsesResponseForKimiK3Request(info, response)
	var data []byte
	var err error
	if restored != nil {
		data, err = common.Marshal(restored)
	} else {
		data, err = common.Marshal(response)
	}
	if err != nil || info == nil || !info.KimiK3HideThinking {
		return data, err
	}
	filtered, err := stripKimiK3ReasoningUsageDetails(
		string(data),
		"usage.completion_tokens_details",
		"usage.billing_usage.openai_usage.completion_tokens_details",
	)
	return []byte(filtered), err
}

func MarshalKimiK3ResponsesStreamPayload(info *relaycommon.RelayInfo, payload dto.ResponsesStreamResponse) ([]byte, error) {
	restored := responsesResponseForKimiK3Request(info, payload.Response)
	var data []byte
	var err error
	if restored == nil {
		data, err = common.Marshal(payload)
	} else {
		payload.Response = nil
		data, err = common.Marshal(struct {
			dto.ResponsesStreamResponse
			Response *tntResponsesResponse `json:"response,omitempty"`
		}{
			ResponsesStreamResponse: payload,
			Response:                restored,
		})
	}
	if err != nil || info == nil || !info.KimiK3HideThinking {
		return data, err
	}
	filtered, err := stripKimiK3ReasoningUsageDetails(
		string(data),
		"response.usage.completion_tokens_details",
		"response.usage.billing_usage.openai_usage.completion_tokens_details",
	)
	return []byte(filtered), err
}

func OaiChatToResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	var chatResp dto.OpenAITextResponse
	if err := common.Unmarshal(body, &chatResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if info.IsTNTTencentOpenAIConversion() {
		if err := relayconvert.SanitizeTNTTencentChatResponse(&chatResp); err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		jsonFenceFilter := newTNTJSONFenceStreamFilter(info)
		jsonFenceFilter.FilterResponse(&chatResp)
	}
	if info.IsOfficialCompatibility() {
		if matched, didMatch := applyOfficialStopToChatResponse(info, &chatResp); didMatch {
			setOfficialMatchedStop(info, matched)
		}
	}
	if oaiError := chatResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	info.ObserveResponseModel(chatResp.Model)
	if responseID := helper.GetResponseID(c); responseID != "" {
		chatResp.Id = responseID
	}
	convertResult, err := service.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, &chatResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	responsesResp, ok := convertResult.Value.(*dto.OpenAIResponsesResponse)
	if !ok {
		return nil, types.NewOpenAIError(fmt.Errorf("expected OpenAI responses response, got %T", convertResult.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	usage := convertResult.Usage
	if usage == nil || usage.TotalTokens == 0 {
		text := service.ExtractOutputTextFromResponses(responsesResp)
		usage = service.ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
		responsesResp.Usage = relayconvert.UsageFromChatUsage(usage)
	}
	responsesResp.Model = info.DownstreamModelName(responsesResp.Model)
	if info.KimiK3HideThinking {
		relayconvert.HideKimiK3ResponsesThinking(responsesResp)
	}

	responseBody, err := MarshalKimiK3ResponsesResponse(info, responsesResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}

func OaiChatToResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	responseID := helper.GetResponseID(c)
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:                 responseID,
		Model:              info.DownstreamModelName(info.UpstreamModelName),
		EmitSequenceNumber: true,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	streamErr := (*types.NewAPIError)(nil)
	sequenceNumber := 0
	var stopFilter *relayconvert.ChatStreamStopFilter
	jsonFenceFilter := newTNTJSONFenceStreamFilter(info)
	if info.IsOfficialCompatibility() {
		stopFilter = newOfficialChatStreamStopFilter(info)
	}
	// upstreamFailure is the upstream or conversion failure the client was
	// told about with response.failed.
	var upstreamFailure *types.NewAPIError

	sendEvent := func(event relayconvert.ChatToResponsesStreamEvent) bool {
		if info.KimiK3HideThinking && !relayconvert.HideKimiK3ResponsesStreamThinking(&event) {
			return true
		}
		payload := any(event.Payload)
		if info.IsTNTTencentOpenAIConversion() {
			streamResponse := event.Payload
			if event.Type == "response.reasoning_summary_text.done" {
				streamResponse.Part = nil
			}
			tntPayload := tntResponsesStreamPayload{
				ResponsesStreamResponse: streamResponse,
				Response:                responsesResponseForKimiK3Request(info, event.Payload.Response),
				SequenceNumber:          sequenceNumber,
			}
			switch event.Type {
			case "response.output_text.delta", "response.output_text.done":
				emptyLogProbs := make([]any, 0)
				tntPayload.LogProbs = &emptyLogProbs
			}
			if event.Type == "response.output_text.done" || event.Type == "response.reasoning_summary_text.done" {
				tntPayload.Text = &event.DoneText
			}
			if event.Type == "response.function_call_arguments.done" {
				tntPayload.Arguments = &event.DoneArguments
				tntPayload.Name = &event.FunctionName
			}
			payload = tntPayload
		} else if restored := responsesResponseForKimiK3Request(info, event.Payload.Response); restored != nil {
			streamResponse := event.Payload
			streamResponse.Response = nil
			payload = struct {
				dto.ResponsesStreamResponse
				Response *tntResponsesResponse `json:"response,omitempty"`
			}{
				ResponsesStreamResponse: streamResponse,
				Response:                restored,
			}
		}
		data, err := common.Marshal(payload)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
			return false
		}
		if info.KimiK3HideThinking {
			filtered, err := stripKimiK3ReasoningUsageDetails(
				string(data),
				"response.usage.completion_tokens_details",
				"response.usage.billing_usage.openai_usage.completion_tokens_details",
			)
			if err != nil {
				streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
				return false
			}
			data = []byte(filtered)
		}
		if err := helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: event.Type}, string(data)); err != nil {
			if helper.HandleStreamClientDisconnect(c, info, nil, err) {
				return false
			}
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			return false
		}
		sequenceNumber++
		return true
	}
	failResponsesStream := func(err error) bool {
		failureResults, handled := state.FailResponsesStream("server_error", err.Error(), "")
		if !handled {
			return false
		}
		for _, result := range failureResults {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				return true
			}
			if !sendEvent(event) {
				return true
			}
		}
		return true
	}

	info.RequireStreamProtocolEnd()
	streamRetryErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			stopStream(sr, streamErr)
			return
		}

		var errorResp dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
			if oaiError := errorResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
				if failResponsesStream(fmt.Errorf("%s", oaiError.Message)) {
					// The client got response.failed, so the request is never
					// retried; the recorded error keeps the stream failed.
					upstreamFailure = types.WithOpenAIError(*oaiError, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
					sr.Stop(upstreamFailure)
					return
				}
				streamErr = types.WithOpenAIError(*oaiError, resp.StatusCode)
				stopStream(sr, streamErr)
				return
			}
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal chat stream response: %s", err.Error()))
			if failResponsesStream(err) {
				upstreamFailure = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
				sr.Stop(upstreamFailure)
				return
			}
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			stopStream(sr, streamErr)
			return
		}
		if info.IsTNTTencentOpenAIConversion() {
			relayconvert.SanitizeTNTTencentChatStreamChunk(&chunk)
		}
		jsonFenceFilter.Filter(&chunk)
		stopFilter.Filter(&chunk)
		if info.KimiK3HideThinking {
			relayconvert.HideKimiK3ChatStreamThinking(&chunk)
		}
		if matched := stopFilter.MatchedSequence(); matched != "" {
			setOfficialMatchedStop(info, matched)
		}
		if chunk.IsFinished() && info.StreamStatus != nil {
			info.StreamStatus.MarkProtocolEnd("finish_reason")
		}

		info.ObserveResponseModel(chunk.Model)
		results, err := service.ConvertStreamResponseChunk(c, info, state, &chunk)
		if err != nil {
			if failResponsesStream(err) {
				upstreamFailure = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
				sr.Stop(upstreamFailure)
				return
			}
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			stopStream(sr, streamErr)
			return
		}
		for _, result := range results {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				stopStream(sr, streamErr)
				return
			}
			if !sendEvent(event) {
				if info.StreamStatus != nil && info.StreamStatus.IsClientGone() {
					sr.ClientGone(info.StreamStatus.Snapshot().EndError)
				} else {
					stopStream(sr, streamErr)
				}
				return
			}
		}
	})

	if streamErr != nil {
		return nil, streamErr
	}
	if streamRetryErr != nil {
		return nil, streamRetryErr
	}

	usage := state.Usage()
	noUpstreamUsage := usage == nil || usage.TotalTokens == 0
	if upstreamFailure != nil && noUpstreamUsage && state.UsageText() == "" {
		// The stream failed before any output reached the client, which
		// already received response.failed: the request is not charged.
		return nil, upstreamFailure
	}
	_, usageEstimationEnabled := info.UsageEstimationSettings()
	if noUpstreamUsage && !usageEstimationEnabled {
		usage = service.ResponseText2Usage(c, state.UsageText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		state.SetUsage(usage)
	}
	if info.KimiK3HideThinking {
		relayconvert.HideKimiK3ReasoningUsage(usage)
		state.SetUsage(usage)
	}
	if info.StreamStatus != nil && info.StreamStatus.IsClientGone() {
		return usage, nil
	}

	finalResults, err := service.FinalizeStreamResponse(c, info, state)
	if err != nil {
		if failResponsesStream(err) {
			return usage, streamErr
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if !sendEvent(event) {
			if streamErr != nil {
				return nil, streamErr
			}
			return usage, nil
		}
	}

	return usage, nil
}

// stopStream ends a converted stream from the scanner callback. A nil
// streamErr (the stream stops because the client went away) must reach
// StreamResult.Stop as an untyped nil; a typed nil *NewAPIError is a non-nil
// error that StreamResult would record as a failure and dereference.
func stopStream(sr *helper.StreamResult, streamErr *types.NewAPIError) {
	if streamErr == nil {
		sr.Stop(nil)
		return
	}
	sr.Stop(streamErr)
}
