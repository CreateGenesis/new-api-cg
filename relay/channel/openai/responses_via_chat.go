package openai

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"
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
	if restored != nil {
		return common.Marshal(restored)
	}
	return common.Marshal(response)
}

func MarshalKimiK3ResponsesStreamPayload(info *relaycommon.RelayInfo, payload dto.ResponsesStreamResponse) ([]byte, error) {
	restored := responsesResponseForKimiK3Request(info, payload.Response)
	if restored == nil {
		return common.Marshal(payload)
	}
	payload.Response = nil
	return common.Marshal(struct {
		dto.ResponsesStreamResponse
		Response *tntResponsesResponse `json:"response,omitempty"`
	}{
		ResponsesStreamResponse: payload,
		Response:                restored,
	})
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
	}
	if info.IsKimiK3OfficialCompatibility() {
		if matched := relayconvert.ApplyKimiK3StopToChatResponse(&chatResp, relayconvert.KimiK3StopSequencesFromRequest(info.Request)); matched != "" {
			info.KimiK3MatchedStopSequence = matched
		}
	}
	if oaiError := chatResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	if responseID := helper.GetResponseID(c); responseID != "" {
		chatResp.Id = responseID
	}
	convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, &chatResp)
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
		ID:    responseID,
		Model: info.DownstreamModelName(info.UpstreamModelName),
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	streamErr := (*types.NewAPIError)(nil)
	sequenceNumber := 0
	var stopFilter *relayconvert.KimiK3ChatStreamStopFilter
	if info.IsKimiK3OfficialCompatibility() {
		stopFilter = relayconvert.NewKimiK3ChatStreamStopFilter(relayconvert.KimiK3StopSequencesFromRequest(info.Request))
	}

	sendEvent := func(event relayconvert.ChatToResponsesStreamEvent) bool {
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
		helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: event.Type}, string(data))
		sequenceNumber++
		return true
	}

	info.RequireStreamProtocolEnd()
	streamRetryErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}

		var errorResp dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
			if oaiError := errorResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
				streamErr = types.WithOpenAIError(*oaiError, resp.StatusCode)
				sr.Stop(streamErr)
				return
			}
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			logger.LogError(c, "failed to unmarshal chat stream response: "+err.Error())
			sr.Error(err)
			return
		}
		if info.IsTNTTencentOpenAIConversion() {
			relayconvert.SanitizeTNTTencentChatStreamChunk(&chunk)
		}
		stopFilter.Filter(&chunk)
		if matched := stopFilter.MatchedSequence(); matched != "" {
			info.KimiK3MatchedStopSequence = matched
		}
		if chunk.IsFinished() && info.StreamStatus != nil {
			info.StreamStatus.MarkProtocolEnd("finish_reason")
		}

		results, err := relayconvert.ConvertStreamResponseChunk(c, info, state, &chunk)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		for _, result := range results {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				sr.Stop(streamErr)
				return
			}
			if !sendEvent(event) {
				sr.Stop(streamErr)
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
	if usage == nil || usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(c, state.UsageText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		state.SetUsage(usage)
	}

	finalResults, err := relayconvert.FinalizeStreamResponse(c, info, state)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if !sendEvent(event) {
			return nil, streamErr
		}
	}

	return usage, nil
}
