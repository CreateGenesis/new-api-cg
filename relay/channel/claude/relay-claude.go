package claude

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

func stopReasonClaude2OpenAI(reason string) string {
	return relayconvert.StopReasonClaudeToOpenAI(reason)
}

func maybeMarkClaudeRefusal(c *gin.Context, stopReason string) {
	if c == nil {
		return
	}
	if strings.EqualFold(stopReason, "refusal") {
		common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "claude_stop_reason=refusal")
	}
}

func StreamResponseClaude2OpenAI(claudeResponse *dto.ClaudeResponse) *dto.ChatCompletionsStreamResponse {
	return relayconvert.StreamResponseClaude2OpenAI(claudeResponse)
}

func ResponseClaude2OpenAI(claudeResponse *dto.ClaudeResponse) *dto.OpenAITextResponse {
	return relayconvert.ResponseClaude2OpenAI(claudeResponse)
}

type ClaudeResponseInfo = relayconvert.ClaudeResponseInfo

func cacheCreationTokensForOpenAIUsage(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	openAIUsage := relayconvert.UsageFromClaudeUsage(usage)
	if openAIUsage == nil {
		return 0
	}
	return openAIUsage.PromptTokens - usage.PromptTokens - usage.PromptTokensDetails.CachedTokens
}

func buildOpenAIStyleUsageFromClaudeUsage(usage *dto.Usage) dto.Usage {
	mapped := relayconvert.UsageFromClaudeUsage(usage)
	if mapped == nil {
		return dto.Usage{}
	}
	return *mapped
}

func buildMessageDeltaPatchUsage(claudeResponse *dto.ClaudeResponse, claudeInfo *ClaudeResponseInfo) *dto.ClaudeUsage {
	return relayconvert.BuildMessageDeltaPatchUsage(claudeResponse, claudeInfo)
}

func shouldSkipClaudeMessageDeltaUsagePatch(info *relaycommon.RelayInfo) bool {
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled {
		return true
	}
	if info == nil {
		return false
	}
	return info.ChannelSetting.PassThroughBodyEnabled
}

func patchClaudeMessageDeltaUsageData(data string, usage *dto.ClaudeUsage) string {
	return relayconvert.PatchClaudeMessageDeltaUsageData(data, usage)
}

func normalizeAnthropicInputUsage(info *relaycommon.RelayInfo, response *dto.ClaudeResponse) bool {
	if info == nil || response == nil {
		return false
	}
	options := info.ConvOptions()
	if options == nil || !options.Claude.AnthropicInputIncludesCache {
		return false
	}

	normalized := false
	if response.Message != nil && hasAnthropicInputUsage(response.Message.Usage) {
		normalized = relayconvert.NormalizeAnthropicInputIncludesCache(response.Message.Usage) || normalized
	}
	if hasAnthropicInputUsage(response.Usage) {
		normalized = relayconvert.NormalizeAnthropicInputIncludesCache(response.Usage) || normalized
	}
	return normalized
}

func hasAnthropicInputUsage(usage *dto.ClaudeUsage) bool {
	if usage == nil {
		return false
	}
	if usage.InputTokens != 0 || usage.CacheReadInputTokens != 0 || usage.CacheCreationInputTokens != 0 ||
		usage.ClaudeCacheCreation5mTokens != 0 || usage.ClaudeCacheCreation1hTokens != 0 {
		return true
	}
	return usage.CacheCreation != nil &&
		(usage.CacheCreation.Ephemeral5mInputTokens != 0 || usage.CacheCreation.Ephemeral1hInputTokens != 0)
}

func FormatClaudeResponseInfo(claudeResponse *dto.ClaudeResponse, oaiResponse *dto.ChatCompletionsStreamResponse, claudeInfo *ClaudeResponseInfo) bool {
	return relayconvert.FormatClaudeResponseInfo(claudeResponse, oaiResponse, claudeInfo)
}

func HandleStreamResponseData(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo, data string) *types.NewAPIError {
	return handleStreamResponseData(c, info, claudeInfo, nil, nil, data)
}

func handleStreamResponseData(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo, stopFilter *relayconvert.KimiK3ClaudeStreamStopFilter, thinkingFilter *relayconvert.KimiK3ClaudeStreamThinkingFilter, data string) *types.NewAPIError {
	var claudeResponse dto.ClaudeResponse
	err := common.UnmarshalJsonStr(data, &claudeResponse)
	if err != nil {
		common.SysLog("error unmarshalling stream response: " + err.Error())
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeError := claudeResponse.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
		return types.WithClaudeError(*claudeError, http.StatusInternalServerError)
	}
	usageNormalized := normalizeAnthropicInputUsage(info, &claudeResponse)
	for _, filteredResponse := range stopFilter.Filter(&claudeResponse) {
		unfilteredResponse := filteredResponse
		filteredResponse = thinkingFilter.Filter(unfilteredResponse)
		if filteredResponse == nil {
			FormatClaudeResponseInfo(unfilteredResponse, nil, claudeInfo)
			continue
		}
		eventData := data
		if usageNormalized || stopFilter != nil || thinkingFilter != nil {
			encoded, marshalErr := common.Marshal(filteredResponse)
			if marshalErr != nil {
				return types.NewError(marshalErr, types.ErrorCodeBadResponseBody)
			}
			eventData = string(encoded)
		}
		if filteredResponse.Type == "message_stop" && info != nil && info.StreamStatus != nil {
			info.StreamStatus.MarkProtocolEnd("message_stop")
		}
		if filteredResponse.StopReason != "" {
			maybeMarkClaudeRefusal(c, filteredResponse.StopReason)
		}
		if filteredResponse.Delta != nil && filteredResponse.Delta.StopReason != nil {
			maybeMarkClaudeRefusal(c, *filteredResponse.Delta.StopReason)
		}
		countClaudeStreamBillableTools(c, info, filteredResponse)
		if info.RelayFormat == types.RelayFormatClaude {
			FormatClaudeResponseInfo(filteredResponse, nil, claudeInfo)

			if filteredResponse.Type == "message_start" {
				// message_start, 获取usage
				if filteredResponse.Message != nil {
					info.UpstreamModelName = filteredResponse.Message.Model
					filteredResponse.Message.Model = info.DownstreamModelName(filteredResponse.Message.Model)
					if stopFilter != nil || filteredResponse.Message.Model != info.UpstreamModelName {
						encoded, marshalErr := common.Marshal(filteredResponse)
						if marshalErr != nil {
							return types.NewError(marshalErr, types.ErrorCodeBadResponseBody)
						}
						eventData = string(encoded)
					}
				}
			} else if filteredResponse.Type == "message_delta" {
				// 确保 message_delta 的 usage 包含完整的 input_tokens 和 cache 相关字段
				// 解决 AWS Bedrock 等上游返回的 message_delta 缺少这些字段的问题
				if !shouldSkipClaudeMessageDeltaUsagePatch(info) {
					eventData = patchClaudeMessageDeltaUsageData(eventData, buildMessageDeltaPatchUsage(filteredResponse, claudeInfo))
				}
			}
			if err := helper.ClaudeChunkData(c, *filteredResponse, eventData); err != nil {
				if helper.HandleStreamClientDisconnect(c, info, nil, err) {
					return nil
				}
				return types.NewError(err, types.ErrorCodeBadResponse)
			}
		} else if info.RelayFormat == types.RelayFormatOpenAI {
			response := StreamResponseClaude2OpenAI(filteredResponse)

			if !FormatClaudeResponseInfo(filteredResponse, response, claudeInfo) {
				continue
			}
			response.Model = info.DownstreamModelName(response.Model)

			err = helper.ObjectData(c, response)
			if err != nil {
				if helper.HandleStreamClientDisconnect(c, info, nil, err) {
					return nil
				}
				logger.LogError(c, "send_stream_response_failed: "+err.Error())
				return types.NewError(err, types.ErrorCodeBadResponse)
			}
		}
	}
	return nil
}

func countClaudeStreamBillableTools(c *gin.Context, info *relaycommon.RelayInfo, claudeResponse *dto.ClaudeResponse) {
	if claudeResponse == nil {
		return
	}
	if claudeResponse.Type == "content_block_start" &&
		claudeResponse.ContentBlock != nil &&
		claudeResponse.ContentBlock.Type == "tool_use" {
		info.CountBillableToolCall(dto.BuildInCallToolUse, claudeResponse.ContentBlock.Name)
	}
	if claudeResponse.Type == "message_delta" &&
		claudeResponse.Usage != nil &&
		claudeResponse.Usage.ServerToolUse != nil &&
		claudeResponse.Usage.ServerToolUse.WebSearchRequests > 0 {
		c.Set("claude_web_search_requests", claudeResponse.Usage.ServerToolUse.WebSearchRequests)
	}
}

func HandleStreamFinalResponse(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo) {
	if claudeInfo.Usage.PromptTokens == 0 {
		//上游出错
	}
	if claudeInfo.Usage.CompletionTokens == 0 || !claudeInfo.Done {
		if common.DebugEnabled {
			common.SysLog("claude response usage is not complete, maybe upstream error")
		}
		// 只补缺失字段，不整份覆盖——保留 message_start 已拿到的 cache 字段
		fallback := service.ResponseText2Usage(c, claudeInfo.ResponseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		if claudeInfo.Usage.CompletionTokens == 0 ||
			(!claudeInfo.Done && fallback.CompletionTokens > claudeInfo.Usage.CompletionTokens) {
			claudeInfo.Usage.CompletionTokens = fallback.CompletionTokens
		}
		if claudeInfo.Usage.PromptTokens == 0 {
			claudeInfo.Usage.PromptTokens = fallback.PromptTokens
		}
		claudeInfo.Usage.TotalTokens = claudeInfo.Usage.PromptTokens + claudeInfo.Usage.CompletionTokens
		claudeInfo.Usage.Estimated = true
	}
	if claudeInfo.Usage != nil {
		claudeInfo.Usage.UsageSemantic = "anthropic"
	}
	if claudeInfo.Usage != nil && claudeInfo.Usage.BillingUsage == nil {
		claudeInfo.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(buildMessageDeltaPatchUsage(nil, claudeInfo))
	}
	if claudeInfo.Usage != nil && claudeInfo.Usage.Estimated && claudeInfo.Usage.BillingUsage != nil {
		claudeInfo.Usage.BillingUsage.Estimated = true
	}

	if info.RelayFormat == types.RelayFormatClaude {
		//
	} else if info.RelayFormat == types.RelayFormatOpenAI {
		if info.ShouldIncludeUsage {
			openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(claudeInfo.Usage)
			response := helper.GenerateFinalUsageResponse(claudeInfo.ResponseId, claudeInfo.Created, info.UpstreamModelName, openAIUsage)
			err := helper.ObjectData(c, response)
			if err != nil {
				common.SysLog("send final response failed: " + err.Error())
			}
		}
		helper.Done(c)
	}
}

func ClaudeStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}
	var err *types.NewAPIError
	var stopFilter *relayconvert.ClaudeStreamStopFilter
	var thinkingFilter *relayconvert.KimiK3ClaudeStreamThinkingFilter
	if info.IsOfficialCompatibility() {
		stopSequences := relayconvert.StopSequencesFromRequest(info.Request)
		if info.IsGLM53OfficialCompatibility() {
			stopFilter = relayconvert.NewGLM53ClaudeStreamStopFilter(stopSequences)
		} else {
			stopFilter = relayconvert.NewClaudeStreamStopFilter(stopSequences)
		}
		if info.KimiK3HideThinking {
			thinkingFilter = relayconvert.NewKimiK3ClaudeStreamThinkingFilter()
		}
	}
	info.RequireStreamProtocolEnd()
	streamRetryErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		err = handleStreamResponseData(c, info, claudeInfo, stopFilter, thinkingFilter, data)
		if err != nil {
			sr.Stop(err)
		}
	})
	if err != nil {
		return nil, err
	}
	if streamRetryErr != nil {
		return nil, streamRetryErr
	}
	if info.StreamStatus != nil && info.StreamStatus.IsClientGone() {
		return claudeInfo.Usage, nil
	}

	HandleStreamFinalResponse(c, info, claudeInfo)
	return claudeInfo.Usage, nil
}

func HandleClaudeResponseData(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo, httpResp *http.Response, data []byte) *types.NewAPIError {
	var claudeResponse dto.ClaudeResponse
	err := common.Unmarshal(data, &claudeResponse)
	if err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeError := claudeResponse.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
		return types.WithClaudeError(*claudeError, http.StatusInternalServerError)
	}
	usageNormalized := normalizeAnthropicInputUsage(info, &claudeResponse)
	originalTextContent := make([]string, len(claudeResponse.Content))
	for index := range claudeResponse.Content {
		originalTextContent[index] = claudeResponse.Content[index].GetText()
	}
	stopResponseFiltered := false
	if info.IsOfficialCompatibility() {
		stopSequences := relayconvert.StopSequencesFromRequest(info.Request)
		if info.IsGLM53OfficialCompatibility() {
			_, stopResponseFiltered = relayconvert.ApplyGLM53StopToClaudeResponse(&claudeResponse, stopSequences)
		} else {
			stopResponseFiltered = relayconvert.ApplyStopToClaudeResponse(&claudeResponse, stopSequences) != ""
		}
		if info.KimiK3HideThinking {
			relayconvert.HideKimiK3ClaudeThinking(&claudeResponse)
		}
	}
	claudeResponse.Model = info.DownstreamModelName(claudeResponse.Model)
	maybeMarkClaudeRefusal(c, claudeResponse.StopReason)
	if claudeInfo.Usage == nil {
		claudeInfo.Usage = &dto.Usage{}
	}
	if claudeResponse.Usage != nil {
		claudeInfo.Usage.PromptTokens = claudeResponse.Usage.InputTokens
		claudeInfo.Usage.CompletionTokens = claudeResponse.Usage.OutputTokens
		claudeInfo.Usage.TotalTokens = claudeResponse.Usage.InputTokens + claudeResponse.Usage.OutputTokens
		claudeInfo.Usage.UsageSemantic = "anthropic"
		if claudeResponse.Usage.BillingUsage.IsRecognized() {
			claudeInfo.Usage.BillingUsage = dto.CloneBillingUsage(claudeResponse.Usage.BillingUsage)
		} else {
			claudeInfo.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(claudeResponse.Usage)
		}
		claudeInfo.Usage.PromptTokensDetails.CachedTokens = claudeResponse.Usage.CacheReadInputTokens
		claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens = claudeResponse.Usage.CacheCreationInputTokens
		claudeInfo.Usage.ClaudeCacheCreation5mTokens = claudeResponse.Usage.GetCacheCreation5mTokens()
		claudeInfo.Usage.ClaudeCacheCreation1hTokens = claudeResponse.Usage.GetCacheCreation1hTokens()
	}
	var responseData []byte
	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		openaiResponse := ResponseClaude2OpenAI(&claudeResponse)
		openaiResponse.Model = info.DownstreamModelName(openaiResponse.Model)
		openaiResponse.Usage = buildOpenAIStyleUsageFromClaudeUsage(claudeInfo.Usage)
		responseData, err = common.Marshal(openaiResponse)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	case types.RelayFormatClaude:
		if usageNormalized || info.IsKimiK3OfficialCompatibility() || claudeResponse.Model != info.GetUpstreamModelName() {
			responseData, err = common.Marshal(claudeResponse)
			if err != nil {
				return types.NewError(err, types.ErrorCodeBadResponseBody)
			}
		} else if stopResponseFiltered && info.IsGLM53OfficialCompatibility() {
			responseData = data
			for index := range claudeResponse.Content {
				text := claudeResponse.Content[index].GetText()
				if text == originalTextContent[index] {
					continue
				}
				responseData, err = sjson.SetBytes(responseData, fmt.Sprintf("content.%d.text", index), text)
				if err != nil {
					return types.NewError(err, types.ErrorCodeBadResponseBody)
				}
			}
		} else {
			responseData = data
		}
	}

	if claudeResponse.Usage != nil && claudeResponse.Usage.ServerToolUse != nil && claudeResponse.Usage.ServerToolUse.WebSearchRequests > 0 {
		c.Set("claude_web_search_requests", claudeResponse.Usage.ServerToolUse.WebSearchRequests)
	}
	for _, block := range claudeResponse.Content {
		if block.Type == "tool_use" {
			info.CountBillableToolCall(dto.BuildInCallToolUse, block.Name)
		}
	}

	service.IOCopyBytesGracefully(c, httpResp, responseData)
	return nil
}

func ClaudeHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	logger.LogDebug(c, "responseBody: %s", responseBody)
	handleErr := HandleClaudeResponseData(c, info, claudeInfo, resp, responseBody)
	if handleErr != nil {
		return nil, handleErr
	}
	return claudeInfo.Usage, nil
}
