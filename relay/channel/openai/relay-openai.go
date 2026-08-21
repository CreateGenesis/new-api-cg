package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/openrouter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func sendStreamData(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	if data == "" {
		return nil
	}
	data, err := filterKimiK3ChatStreamData(info, data)
	if err != nil {
		return err
	}
	if gjson.Get(data, "usage").IsObject() {
		var usageEnvelope struct {
			Usage *dto.Usage `json:"usage"`
		}
		if err := common.UnmarshalJsonStr(data, &usageEnvelope); err != nil {
			return err
		}
		if usageEnvelope.Usage != nil && applyUsagePostProcessing(info, usageEnvelope.Usage, common.StringToByteSlice(data)) {
			usageJSON, err := common.Marshal(usageEnvelope.Usage)
			if err != nil {
				return err
			}
			data, err = sjson.SetRaw(data, "usage", string(usageJSON))
			if err != nil {
				return err
			}
		}
	}

	if !forceFormat && !thinkToContent {
		if shouldRewriteResponseModel(info) {
			patched, err := rewriteResponseModelInJSONData(info, data)
			if err != nil {
				return err
			}
			return helper.StringData(c, patched)
		}
		return helper.StringData(c, data)
	}

	var lastStreamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &lastStreamResponse); err != nil {
		return err
	}
	rewriteStreamResponseModel(info, &lastStreamResponse)

	if !forceFormat && !thinkToContent {
		return helper.ObjectData(c, lastStreamResponse)
	}

	if !thinkToContent {
		return helper.ObjectData(c, lastStreamResponse)
	}

	hasThinkingContent := false
	hasContent := false
	var thinkingContent strings.Builder
	for _, choice := range lastStreamResponse.Choices {
		if len(choice.Delta.GetReasoningContent()) > 0 {
			hasThinkingContent = true
			thinkingContent.WriteString(choice.Delta.GetReasoningContent())
		}
		if len(choice.Delta.GetContentString()) > 0 {
			hasContent = true
		}
	}

	// Handle think to content conversion
	if info.ThinkingContentInfo.IsFirstThinkingContent {
		if hasThinkingContent {
			response := lastStreamResponse.Copy()
			for i := range response.Choices {
				// send `think` tag with thinking content
				response.Choices[i].Delta.SetContentString("<think>\n" + thinkingContent.String())
				response.Choices[i].Delta.ReasoningContent = nil
				response.Choices[i].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.IsFirstThinkingContent = false
			info.ThinkingContentInfo.HasSentThinkingContent = true
			return helper.ObjectData(c, response)
		}
	}

	if lastStreamResponse.Choices == nil || len(lastStreamResponse.Choices) == 0 {
		return helper.ObjectData(c, lastStreamResponse)
	}

	// Process each choice
	for i, choice := range lastStreamResponse.Choices {
		// Handle transition from thinking to content
		// only send `</think>` tag when previous thinking content has been sent
		if hasContent && !info.ThinkingContentInfo.SendLastThinkingContent && info.ThinkingContentInfo.HasSentThinkingContent {
			response := lastStreamResponse.Copy()
			for j := range response.Choices {
				response.Choices[j].Delta.SetContentString("\n</think>\n")
				response.Choices[j].Delta.ReasoningContent = nil
				response.Choices[j].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.SendLastThinkingContent = true
			helper.ObjectData(c, response)
		}

		// Convert reasoning content to regular content if any
		if len(choice.Delta.GetReasoningContent()) > 0 {
			lastStreamResponse.Choices[i].Delta.SetContentString(choice.Delta.GetReasoningContent())
			lastStreamResponse.Choices[i].Delta.ReasoningContent = nil
			lastStreamResponse.Choices[i].Delta.Reasoning = nil
		} else if !hasThinkingContent && !hasContent {
			// flush thinking content
			lastStreamResponse.Choices[i].Delta.ReasoningContent = nil
			lastStreamResponse.Choices[i].Delta.Reasoning = nil
		}
	}

	return helper.ObjectData(c, lastStreamResponse)
}

func filterKimiK3ChatStreamData(info *relaycommon.RelayInfo, data string) (string, error) {
	if info == nil || !info.KimiK3HideThinking {
		return data, nil
	}
	var response dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &response); err != nil {
		return "", err
	}
	relayconvert.HideKimiK3ChatStreamThinking(&response)
	filtered, err := common.Marshal(response)
	if err != nil {
		return "", err
	}
	return stripKimiK3ReasoningUsageDetails(
		string(filtered),
		"usage.completion_tokens_details",
		"usage.billing_usage.openai_usage.completion_tokens_details",
	)
}

func stripKimiK3ReasoningUsageDetails(data string, paths ...string) (string, error) {
	patched := data
	var err error
	for _, path := range paths {
		if path == "" || !gjson.Get(patched, path+".reasoning_tokens").Exists() {
			continue
		}
		patched, err = sjson.Delete(patched, path+".reasoning_tokens")
		if err != nil {
			return data, err
		}
		details := gjson.Get(patched, path)
		allZero := details.IsObject()
		for _, value := range details.Map() {
			if value.Type != gjson.Null && (value.Type != gjson.Number || value.Num != 0) {
				allZero = false
				break
			}
		}
		if allZero {
			patched, err = sjson.Delete(patched, path)
			if err != nil {
				return data, err
			}
		}
	}
	return patched, nil
}

func applyKimiK3UsageToBody(bodyMap map[string]interface{}, usage *dto.Usage) {
	if usage == nil {
		return
	}
	usageMap, ok := bodyMap["usage"].(map[string]interface{})
	if !ok {
		return
	}
	usageMap["completion_tokens"] = usage.CompletionTokens
	usageMap["total_tokens"] = usage.TotalTokens
	if _, exists := usageMap["output_tokens"]; exists {
		usageMap["output_tokens"] = usage.OutputTokens
	}
	details, ok := usageMap["completion_tokens_details"].(map[string]interface{})
	if !ok {
		return
	}
	delete(details, "reasoning_tokens")
	for _, value := range details {
		if number, ok := value.(float64); !ok || number != 0 {
			return
		}
	}
	delete(usageMap, "completion_tokens_details")
}

func shouldRewriteResponseModel(info *relaycommon.RelayInfo) bool {
	return info != nil && info.DownstreamModelName("") != ""
}

func downstreamResponseModel(info *relaycommon.RelayInfo, fallback string) string {
	if info == nil {
		return fallback
	}
	return info.DownstreamModelName(fallback)
}

func rewriteResponseModelInJSONData(info *relaycommon.RelayInfo, data string) (string, error) {
	return rewriteResponseModelInJSONDataAtPaths(info, data, "model")
}

func rewriteResponseModelInJSONDataAtPaths(info *relaycommon.RelayInfo, data string, paths ...string) (string, error) {
	model := downstreamResponseModel(info, "")
	if model == "" {
		return data, nil
	}
	patched := data
	var err error
	for _, path := range paths {
		if path == "" || !gjson.Get(patched, path).Exists() {
			continue
		}
		patched, err = sjson.Set(patched, path, model)
		if err != nil {
			return data, err
		}
	}
	return patched, nil
}

func rewriteTextResponseModel(info *relaycommon.RelayInfo, response *dto.OpenAITextResponse) bool {
	if response == nil {
		return false
	}
	model := downstreamResponseModel(info, response.Model)
	if model == response.Model {
		return false
	}
	response.Model = model
	return true
}

func rewriteStreamResponseModel(info *relaycommon.RelayInfo, response *dto.ChatCompletionsStreamResponse) bool {
	if response == nil {
		return false
	}
	model := downstreamResponseModel(info, response.Model)
	if model == response.Model {
		return false
	}
	response.Model = model
	return true
}

func OaiStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	defer service.CloseResponseBodyGracefully(resp)

	model := info.UpstreamModelName
	var responseId string
	var createAt int64 = 0
	var systemFingerprint string
	var containStreamUsage bool
	var responseTextBuilder strings.Builder
	var toolCount int
	var usage = &dto.Usage{}
	var lastStreamData string
	var lastStreamDataSent bool
	var payloadSent bool
	var upstreamStreamError *types.NewAPIError
	var secondLastStreamData string // 存储倒数第二个stream data，用于音频模型
	jsonFenceFilter := newTNTJSONFenceStreamFilter(info)
	var stopFilter *relayconvert.ChatStreamStopFilter
	if info.IsOfficialCompatibility() {
		stopFilter = newOfficialChatStreamStopFilter(info)
	}
	seenStreamToolCalls := make(map[string]struct{})
	var streamFunctionCallNames []string

	// 检查是否为音频模型
	isAudioModel := strings.Contains(strings.ToLower(model), "audio")

	info.RequireStreamProtocolEnd()
	streamRetryErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if lastStreamData != "" && !lastStreamDataSent {
			if err := HandleStreamFormat(c, info, lastStreamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
				if helper.HandleStreamClientDisconnect(c, info, sr, err) {
					return
				}
				common.SysLog("error handling stream format: " + err.Error())
				sr.Error(err)
			}
			lastStreamDataSent = true
			payloadSent = true
		}
		var sanitizeErr error
		data, sanitizeErr = sanitizeTNTStreamData(info, jsonFenceFilter, data)
		if sanitizeErr != nil {
			sr.Error(sanitizeErr)
			return
		}
		var errorResponse dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResponse); err == nil {
			if openAIError := errorResponse.GetOpenAIError(); openAIError != nil &&
				(openAIError.Type != "" || openAIError.Message != "" || openAIError.Code != nil) {
				message := strings.TrimSpace(openAIError.Message)
				if message == "" {
					message = "upstream stream returned an error event"
				}
				upstreamStreamError = types.NewErrorWithStatusCode(
					fmt.Errorf("%s", message),
					types.ErrorCodeChannelStreamError,
					http.StatusBadGateway,
				)
				upstreamStreamError.SetUpstreamResponse(resp.StatusCode, data)
				if !payloadSent {
					sr.Stop(upstreamStreamError)
					return
				}
				sr.Error(upstreamStreamError)
			}
		}
		if stopFilter != nil {
			var chunk dto.ChatCompletionsStreamResponse
			if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
				sr.Error(err)
				return
			}
			stopFilter.Filter(&chunk)
			if matched := stopFilter.MatchedSequence(); matched != "" {
				setOfficialMatchedStop(info, matched)
			}
			filtered, err := common.Marshal(chunk)
			if err != nil {
				sr.Error(err)
				return
			}
			data = string(filtered)
		}
		if len(data) > 0 {
			// 对音频模型，保存倒数第二个stream data
			if isAudioModel && lastStreamData != "" {
				secondLastStreamData = lastStreamData
			}

			lastStreamData = data
			collectStreamFunctionCallNames(data, seenStreamToolCalls, &streamFunctionCallNames)
			usageData := data
			if info.KimiK3HideThinking {
				var err error
				usageData, err = filterKimiK3ChatStreamData(info, usageData)
				if err != nil {
					logger.LogError(c, "error filtering stream token data: "+err.Error())
					sr.Error(err)
					return
				}
			}
			finished, err := processTokenData(info.RelayMode, usageData, &responseTextBuilder, &toolCount)
			if err != nil {
				logger.LogError(c, "error processing stream token data: "+err.Error())
				sr.Error(err)
			} else if finished && info.StreamStatus != nil {
				info.StreamStatus.MarkProtocolEnd("finish_reason")
			}
			lastStreamData = data
			lastStreamDataSent = false
			holdForFinal := gjson.Get(data, "usage").IsObject() && !info.ShouldIncludeUsage &&
				gjson.Get(data, "choices.0.delta.content").String() == "" &&
				gjson.Get(data, "choices.0.delta.reasoning_content").String() == "" &&
				gjson.Get(data, "choices.0.delta.reasoning").String() == ""
			if !holdForFinal {
				if err := HandleStreamFormat(c, info, data, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
					if helper.HandleStreamClientDisconnect(c, info, sr, err) {
						return
					}
					common.SysLog("error handling stream format: " + err.Error())
					sr.Error(err)
					return
				}
				lastStreamDataSent = true
				payloadSent = true
			}
		}
	})
	if streamRetryErr != nil {
		if upstreamStreamError != nil {
			return nil, upstreamStreamError
		}
		return nil, streamRetryErr
	}

	// 对音频模型，从倒数第二个stream data中提取usage信息
	if isAudioModel && secondLastStreamData != "" {
		var streamResp struct {
			Usage *dto.Usage `json:"usage"`
		}
		err := common.Unmarshal([]byte(secondLastStreamData), &streamResp)
		if err == nil && streamResp.Usage != nil && service.ValidUsage(streamResp.Usage) {
			usage = streamResp.Usage
			containStreamUsage = true

			if common.DebugEnabled {
				logger.LogDebug(c, "Audio model usage extracted from second last SSE: PromptTokens=%d, CompletionTokens=%d, TotalTokens=%d, InputTokens=%d, OutputTokens=%d",
					usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
					usage.InputTokens, usage.OutputTokens)
			}
		}
	}

	// 处理最后的响应
	shouldSendLastResp := true
	if err := handleLastResponse(lastStreamData, &responseId, &createAt, &systemFingerprint, &model, &usage,
		&containStreamUsage, info, &shouldSendLastResp); err != nil {
		logger.LogError(c, fmt.Sprintf("error handling last response: %s, lastStreamData: [%s]", err.Error(), lastStreamData))
	}

	if info.RelayFormat == types.RelayFormatOpenAI {
		if shouldSendLastResp && !lastStreamDataSent && !info.StreamStatus.IsClientGone() {
			if err := sendStreamData(c, info, lastStreamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
				helper.HandleStreamClientDisconnect(c, info, nil, err)
			}
		}
	}

	if !containStreamUsage {
		usage = service.ResponseText2Usage(c, responseTextBuilder.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		usage.CompletionTokens += toolCount * 7
	}

	applyUsagePostProcessing(info, usage, common.StringToByteSlice(lastStreamData))
	if info.KimiK3HideThinking {
		relayconvert.HideKimiK3ReasoningUsage(usage)
	}

	for _, name := range streamFunctionCallNames {
		info.CountBillableToolCall(dto.BuildInCallFunctionCall, name)
	}

	if !info.StreamStatus.IsClientGone() && (info.RelayFormat == types.RelayFormatOpenAI || !lastStreamDataSent) {
		HandleFinalResponse(c, info, lastStreamData, responseId, createAt, model, systemFingerprint, usage, containStreamUsage)
	}

	return usage, nil
}

func officialStopSequences(info *relaycommon.RelayInfo) []string {
	if info == nil || !info.IsOfficialCompatibility() {
		return nil
	}
	return relayconvert.StopSequencesFromRequest(info.Request)
}

func officialStopFinishReason(info *relaycommon.RelayInfo) string {
	if info != nil && info.RelayFormat == types.RelayFormatClaude && info.IsKimiK3OfficialCompatibility() {
		return "stop_sequence"
	}
	return "stop"
}

func newOfficialChatStreamStopFilter(info *relaycommon.RelayInfo) *relayconvert.ChatStreamStopFilter {
	sequences := officialStopSequences(info)
	finishReason := officialStopFinishReason(info)
	if info != nil && info.IsGLM53OfficialCompatibility() && info.RelayFormat == types.RelayFormatOpenAI {
		return relayconvert.NewGLM53ChatStreamStopFilter(sequences, finishReason)
	}
	return relayconvert.NewChatStreamStopFilter(sequences, finishReason)
}

func applyOfficialStopToChatResponse(info *relaycommon.RelayInfo, response *dto.OpenAITextResponse) (string, bool) {
	sequences := officialStopSequences(info)
	finishReason := officialStopFinishReason(info)
	if info != nil && info.IsGLM53OfficialCompatibility() && info.RelayFormat == types.RelayFormatOpenAI {
		return relayconvert.ApplyGLM53StopToChatResponse(response, sequences, finishReason)
	}
	return relayconvert.ApplyStopToChatResponseWithMatch(response, sequences, finishReason)
}

func setOfficialMatchedStop(info *relaycommon.RelayInfo, matched string) {
	if info == nil || matched == "" {
		return
	}
	if info.IsGLM53OfficialCompatibility() {
		info.GLM53MatchedStopSequence = matched
		return
	}
	info.KimiK3MatchedStopSequence = matched
}

func officialMatchedStop(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	if info.IsGLM53OfficialCompatibility() {
		return info.GLM53MatchedStopSequence
	}
	return info.KimiK3MatchedStopSequence
}

func collectStreamFunctionCallNames(data string, seen map[string]struct{}, names *[]string) {
	var streamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
		return
	}
	for _, choice := range streamResponse.Choices {
		for i, tc := range choice.Delta.ToolCalls {
			name := tc.Function.Name
			if name == "" {
				continue
			}
			toolIdx := i
			if tc.Index != nil {
				toolIdx = *tc.Index
			}
			key := fmt.Sprintf("%d-%d", choice.Index, toolIdx)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			*names = append(*names, name)
		}
	}
}

func OpenaiHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	var simpleResponse dto.OpenAITextResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	logger.LogDebug(c, "upstream response body: %s", responseBody)
	// Unmarshal to simpleResponse
	if info.ChannelType == constant.ChannelTypeOpenRouter && info.ChannelOtherSettings.IsOpenRouterEnterprise() {
		// 尝试解析为 openrouter enterprise
		var enterpriseResponse openrouter.OpenRouterEnterpriseResponse
		err = common.Unmarshal(responseBody, &enterpriseResponse)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		if enterpriseResponse.Success {
			responseBody = enterpriseResponse.Data
		} else {
			logger.LogError(c, fmt.Sprintf("openrouter enterprise response success=false, data: %s", enterpriseResponse.Data))
			return nil, types.NewOpenAIError(fmt.Errorf("openrouter response success=false"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
	}

	err = common.Unmarshal(responseBody, &simpleResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	tntResponseSanitized := info.IsTNTTencentOpenAIConversion()
	if tntResponseSanitized {
		if err := relayconvert.SanitizeTNTTencentChatResponse(&simpleResponse); err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		jsonFenceFilter := newTNTJSONFenceStreamFilter(info)
		jsonFenceFilter.FilterResponse(&simpleResponse)
	}
	stopResponseFiltered := false
	if info.IsOfficialCompatibility() {
		stopSequences := officialStopSequences(info)
		if len(stopSequences) > 0 {
			originalChoiceContent := make([]string, len(simpleResponse.Choices))
			originalChoiceReasoning := make([]string, len(simpleResponse.Choices))
			for index := range simpleResponse.Choices {
				originalChoiceContent[index] = simpleResponse.Choices[index].Message.StringContent()
				originalChoiceReasoning[index] = simpleResponse.Choices[index].Message.GetReasoningContent()
			}
			if matched, didMatch := applyOfficialStopToChatResponse(info, &simpleResponse); didMatch {
				setOfficialMatchedStop(info, matched)
				stopResponseFiltered = true
				if info.IsGLM53OfficialCompatibility() {
					responseBody, err = patchChatStopResponseJSON(responseBody, originalChoiceContent, originalChoiceReasoning, &simpleResponse)
					if err != nil {
						return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
					}
				}
			}
			if info.IsKimiK3OfficialCompatibility() {
				stopResponseFiltered = true
			}
		}
	}
	responseModelModified := rewriteTextResponseModel(info, &simpleResponse)

	if oaiError := simpleResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	for _, choice := range simpleResponse.Choices {
		if choice.FinishReason == constant.FinishReasonContentFilter {
			common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "openai_finish_reason=content_filter")
			break
		}
	}

	for _, choice := range simpleResponse.Choices {
		for _, tc := range choice.Message.ParseToolCalls() {
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, tc.Function.Name)
		}
	}

	forceFormat := false
	if info.ChannelSetting.ForceFormat {
		forceFormat = true
	}

	usageModified := false
	markReportedTextUsage(&simpleResponse.Usage, gjson.GetBytes(responseBody, "usage"))
	preserveReportedUsage := shouldPreserveReportedTextUsage(info, &simpleResponse.Usage)
	if simpleResponse.Usage.PromptTokens == 0 && !preserveReportedUsage {
		completionTokens := simpleResponse.Usage.CompletionTokens
		if completionTokens == 0 {
			for _, choice := range simpleResponse.Choices {
				ctkm := service.CountTextToken(choice.Message.StringContent()+choice.Message.GetReasoningContent(), info.UpstreamModelName)
				completionTokens += ctkm
			}
		}
		simpleResponse.Usage = dto.Usage{
			PromptTokens:     info.GetEstimatePromptTokens(),
			CompletionTokens: completionTokens,
			TotalTokens:      info.GetEstimatePromptTokens() + completionTokens,
			Estimated:        true,
		}
		usageModified = true
	}

	usagePostProcessed := applyUsagePostProcessing(info, &simpleResponse.Usage, responseBody)
	thinkingResponseFiltered := info.KimiK3HideThinking
	if thinkingResponseFiltered {
		relayconvert.HideKimiK3ChatThinking(&simpleResponse)
	}

	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		if usageModified || usagePostProcessed || responseModelModified || tntResponseSanitized || (stopResponseFiltered && info.IsKimiK3OfficialCompatibility()) || thinkingResponseFiltered {
			var bodyMap map[string]interface{}
			err = common.Unmarshal(responseBody, &bodyMap)
			if err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
			if usageModified || usagePostProcessed {
				bodyMap["usage"] = simpleResponse.Usage
			} else if thinkingResponseFiltered {
				applyKimiK3UsageToBody(bodyMap, &simpleResponse.Usage)
			}
			if responseModelModified {
				bodyMap["model"] = simpleResponse.Model
			}
			if tntResponseSanitized || (stopResponseFiltered && info.IsKimiK3OfficialCompatibility()) || thinkingResponseFiltered {
				bodyMap["choices"] = simpleResponse.Choices
			}
			responseBody, _ = common.Marshal(bodyMap)
			if thinkingResponseFiltered {
				filteredBody, filterErr := stripKimiK3ReasoningUsageDetails(
					string(responseBody),
					"usage.completion_tokens_details",
					"usage.billing_usage.openai_usage.completion_tokens_details",
				)
				if filterErr != nil {
					return nil, types.NewError(filterErr, types.ErrorCodeBadResponseBody)
				}
				responseBody = []byte(filteredBody)
			}
		}
		if forceFormat {
			responseBody, err = common.Marshal(simpleResponse)
			if err != nil {
				return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
			}
			if thinkingResponseFiltered {
				filteredBody, filterErr := stripKimiK3ReasoningUsageDetails(
					string(responseBody),
					"usage.completion_tokens_details",
					"usage.billing_usage.openai_usage.completion_tokens_details",
				)
				if filterErr != nil {
					return nil, types.NewError(filterErr, types.ErrorCodeBadResponseBody)
				}
				responseBody = []byte(filteredBody)
			}
		} else {
			break
		}
	case types.RelayFormatClaude:
		convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatClaude, &simpleResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		if claudeResponse, ok := convertResult.Value.(*dto.ClaudeResponse); ok && info.IsKimiK3OfficialCompatibility() && officialMatchedStop(info) != "" {
			stopSequence := officialMatchedStop(info)
			claudeResponse.StopSequence = &stopSequence
		}
		claudeRespStr, err := common.Marshal(convertResult.Value)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		responseBody = claudeRespStr
	case types.RelayFormatGemini:
		convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatGemini, &simpleResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		geminiRespStr, err := common.Marshal(convertResult.Value)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		responseBody = geminiRespStr
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)

	return &simpleResponse.Usage, nil
}

func patchChatStopResponseJSON(body []byte, originalContent []string, originalReasoning []string, response *dto.OpenAITextResponse) ([]byte, error) {
	patched := body
	for index := range response.Choices {
		content := response.Choices[index].Message.StringContent()
		if index < len(originalContent) && content != originalContent[index] {
			var err error
			patched, err = sjson.SetBytes(patched, fmt.Sprintf("choices.%d.message.content", index), content)
			if err != nil {
				return nil, err
			}
		}

		reasoning := response.Choices[index].Message.GetReasoningContent()
		if index < len(originalReasoning) && reasoning != originalReasoning[index] {
			path := fmt.Sprintf("choices.%d.message.reasoning_content", index)
			if response.Choices[index].Message.ReasoningContent == nil {
				path = fmt.Sprintf("choices.%d.message.reasoning", index)
			}
			var err error
			patched, err = sjson.SetBytes(patched, path, reasoning)
			if err != nil {
				return nil, err
			}
		}

		finishPath := fmt.Sprintf("choices.%d.finish_reason", index)
		if gjson.GetBytes(patched, finishPath).String() != response.Choices[index].FinishReason {
			var err error
			patched, err = sjson.SetBytes(patched, finishPath, response.Choices[index].FinishReason)
			if err != nil {
				return nil, err
			}
		}
	}
	return patched, nil
}
