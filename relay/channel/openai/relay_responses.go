package openai

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	info.ObserveResponseModel(responsesResponse.Model)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)
	// compute usage
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	info.ApplyVendorToolUsage(responseBody)

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	for i := range responsesResponse.Output {
		imageCounter.Observe(&responsesResponse.Output[i], &i)
	}
	imageCounter.Commit(info)

	normalized := service.NormalizeUsageForSemantic(usage, service.UsageSemanticOpenAI)
	usage = &normalized
	responseBody = rewriteResponsesBodyModel(c, info, responseBody)
	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, common.LogText("invalid response or response body"))
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	accumulator := service.NewResponsesUsageAccumulator(info)
	var streamErr *types.NewAPIError
	outputDelivered := false
	upstreamFailed := false

	info.RequireStreamProtocolEnd()
	streamRetryErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal stream response: %s", err.Error()))
			sr.Error(err)
			return
		}
		markResponsesStreamProtocolEnd(info, streamResponse.Type)
		if streamResponse.Type == "response.output_text.delta" || streamResponse.Type == "response.function_call_arguments.delta" ||
			streamResponse.Type == "response.reasoning_summary_text.delta" || streamResponse.Type == "response.reasoning_text.delta" || streamResponse.Type == "response.refusal.delta" {
			outputDelivered = outputDelivered || streamResponse.Delta != ""
		}
		if streamResponse.Type == "response.failed" || streamResponse.Type == "response.error" || streamResponse.Type == "error" {
			upstreamFailed = true
			accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
			if streamResponse.Response != nil {
				if oaiErr := streamResponse.Response.GetOpenAIError(); oaiErr != nil && oaiErr.Type != "" {
					streamErr = types.WithOpenAIError(*oaiErr, http.StatusInternalServerError)
				}
			}
			if streamErr == nil {
				streamErr = types.NewOpenAIError(fmt.Errorf("responses stream error: %s", streamResponse.Type), types.ErrorCodeBadResponse, http.StatusInternalServerError)
			}
			if outputDelivered {
				sendResponsesStreamData(c, streamResponse, data)
			}
			sr.Stop(streamErr)
			return
		}
		outData, err := rewriteResponseModelInJSONDataAtPaths(info, data, "model", "response.model")
		if err != nil {
			logger.LogError(c, common.LogText("failed to rewrite responses stream model: %s", err.Error()))
			sr.Error(err)
			return
		}
		data = outData
		if streamResponse.Response != nil {
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
		}
		if err := helper.ResponseChunkData(c, streamResponse, data); err != nil {
			if helper.HandleStreamClientDisconnect(c, info, sr, err) {
				return
			}
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
	})
	if streamRetryErr != nil {
		return nil, streamRetryErr
	}

	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	info.StreamStatus.RequireTerminal()
	usage := accumulator.Finish()
	if streamErr != nil && (!upstreamFailed || !outputDelivered) {
		return nil, streamErr
	}
	return usage, nil
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !gjson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := sjson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}

func rewriteResponsesBodyModel(c *gin.Context, info *relaycommon.RelayInfo, body []byte) []byte {
	if !shouldRewriteResponseModel(info) {
		return body
	}
	patched, err := rewriteResponseModelInJSONData(info, string(body))
	if err != nil {
		logger.LogError(c, "failed to rewrite responses model: "+err.Error())
		return body
	}
	return []byte(patched)
}
