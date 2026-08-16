package claude

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	openaiadapter "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ClaudeToResponsesHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	var claudeResponse dto.ClaudeResponse
	if err := common.Unmarshal(body, &claudeResponse); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if claudeError := claudeResponse.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
		return nil, types.WithClaudeError(*claudeError, resp.StatusCode)
	}
	relayconvert.ApplyStopToClaudeResponse(&claudeResponse, relayconvert.StopSequencesFromRequest(info.Request))
	if info.KimiK3HideThinking {
		relayconvert.HideKimiK3ClaudeThinking(&claudeResponse)
	}
	converted, err := relayconvert.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, &claudeResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	response, ok := converted.Value.(*dto.OpenAIResponsesResponse)
	if !ok {
		return nil, types.NewOpenAIError(fmt.Errorf("expected OpenAI responses response, got %T", converted.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	response.Model = info.DownstreamModelName(response.Model)
	data, err := openaiadapter.MarshalKimiK3ResponsesResponse(info, response)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, data)
	return converted.Usage, nil
}

func ClaudeToResponsesStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)
	model := info.DownstreamModelName(info.UpstreamModelName)
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatClaude, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:    helper.GetResponseID(c),
		Model: model,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	var streamErr *types.NewAPIError
	stopFilter := relayconvert.NewClaudeStreamStopFilter(relayconvert.StopSequencesFromRequest(info.Request))
	var thinkingFilter *relayconvert.KimiK3ClaudeStreamThinkingFilter
	if info.KimiK3HideThinking {
		thinkingFilter = relayconvert.NewKimiK3ClaudeStreamThinkingFilter()
	}
	info.RequireStreamProtocolEnd()
	retryErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var claudeResponse dto.ClaudeResponse
		if err := common.UnmarshalJsonStr(data, &claudeResponse); err != nil {
			sr.Error(err)
			return
		}
		if claudeError := claudeResponse.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
			streamErr = types.WithClaudeError(*claudeError, resp.StatusCode)
			sr.Stop(streamErr)
			return
		}
		for _, filteredResponse := range stopFilter.Filter(&claudeResponse) {
			filteredResponse = thinkingFilter.Filter(filteredResponse)
			if filteredResponse == nil {
				continue
			}
			if filteredResponse.Type == "message_stop" && info.StreamStatus != nil {
				info.StreamStatus.MarkProtocolEnd("message_stop")
			}
			results, err := relayconvert.ConvertStreamResponseChunk(c, info, state, filteredResponse)
			if err != nil {
				streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
				sr.Stop(streamErr)
				return
			}
			for _, result := range results {
				payload, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
				if !ok {
					streamErr = types.NewOpenAIError(fmt.Errorf("expected Responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
					sr.Stop(streamErr)
					return
				}
				if payload.Payload.Response != nil {
					payload.Payload.Response.Model = model
				}
				encoded, err := openaiadapter.MarshalKimiK3ResponsesStreamPayload(info, payload.Payload)
				if err != nil {
					streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
					sr.Stop(streamErr)
					return
				}
				if err := helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: payload.Type}, string(encoded)); err != nil {
					if helper.HandleStreamClientDisconnect(c, info, sr, err) {
						return
					}
					streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
					sr.Stop(streamErr)
					return
				}
			}
		}
	})
	if streamErr != nil {
		return nil, streamErr
	}
	if retryErr != nil {
		return nil, retryErr
	}
	if info.StreamStatus != nil && info.StreamStatus.IsClientGone() {
		return state.Usage(), nil
	}
	finalResults, err := relayconvert.FinalizeStreamResponse(c, info, state)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		payload, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected Responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if payload.Payload.Response != nil {
			payload.Payload.Response.Model = model
		}
		encoded, err := openaiadapter.MarshalKimiK3ResponsesStreamPayload(info, payload.Payload)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
		if err := helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: payload.Type}, string(encoded)); err != nil {
			if helper.HandleStreamClientDisconnect(c, info, nil, err) {
				return state.Usage(), nil
			}
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
	}
	if info.StreamStatus != nil && !info.StreamStatus.Snapshot().ProtocolEndReceived && strings.TrimSpace(state.UsageText()) != "" {
		info.StreamStatus.MarkProtocolEnd("converted_response_completed")
	}
	return state.Usage(), nil
}
