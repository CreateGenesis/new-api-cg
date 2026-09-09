package openai

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 辅助函数
func HandleStreamFormat(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		info.SendResponseCount++
		return sendStreamData(c, info, data, forceFormat, thinkToContent)
	case types.RelayFormatClaude:
		info.SendResponseCount++
		return handleClaudeFormat(c, data, info)
	case types.RelayFormatGemini:
		// The stateful relaykit path owns its chunk counter so multi-hop and
		// direct conversions observe the same stream state semantics.
		return handleGeminiFormat(c, data, info)
	}
	return nil
}

func handleClaudeFormat(c *gin.Context, data string, info *relaycommon.RelayInfo) error {
	data, err := filterKimiK3ChatStreamData(info, data)
	if err != nil {
		return err
	}
	var streamResponse dto.ChatCompletionsStreamResponse
	if err := common.Unmarshal(common.StringToByteSlice(data), &streamResponse); err != nil {
		return err
	}

	if streamResponse.Usage != nil {
		info.EnsureClaudeConvertInfo().Usage = streamResponse.Usage
	}
	result, err := service.ConvertStreamResponse(c, info, types.RelayFormatClaude, &streamResponse)
	if err != nil {
		return err
	}
	claudeResponses, ok := result.Value.([]*dto.ClaudeResponse)
	if !ok {
		return fmt.Errorf("expected Claude stream responses, got %T", result.Value)
	}
	for _, resp := range claudeResponses {
		if matchedStop := officialMatchedStop(info); resp.Type == "message_delta" && resp.Delta != nil && matchedStop != "" {
			resp.Delta.StopSequence = &matchedStop
		}
		helper.ClaudeData(c, *resp)
	}
	return nil
}

func handleGeminiFormat(c *gin.Context, data string, info *relaycommon.RelayInfo) error {
	data, err := filterKimiK3ChatStreamData(info, data)
	if err != nil {
		return err
	}
	var streamResponse dto.ChatCompletionsStreamResponse
	if err := common.Unmarshal(common.StringToByteSlice(data), &streamResponse); err != nil {
		logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
		return err
	}

	state, err := chatToGeminiStreamState(info, &streamResponse)
	if err != nil {
		return err
	}
	results, err := service.ConvertStreamResponseChunk(c, info, state, &streamResponse)
	if err != nil {
		return err
	}
	return sendGeminiStreamResults(c, results)
}

func chatToGeminiStreamState(info *relaycommon.RelayInfo, streamResponse *dto.ChatCompletionsStreamResponse) (*relayconvert.ResponseStreamState, error) {
	if info != nil && info.ChatToGeminiStreamState != nil {
		state, ok := info.ChatToGeminiStreamState.(*relayconvert.ResponseStreamState)
		if !ok || state == nil {
			return nil, fmt.Errorf("invalid Chat-to-Gemini stream state %T", info.ChatToGeminiStreamState)
		}
		return state, nil
	}

	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatGemini, relayconvert.ResponseStreamOptions{
		ID:      streamResponse.Id,
		Model:   streamResponse.Model,
		Created: streamResponse.Created,
	})
	if err != nil {
		return nil, err
	}
	if info != nil {
		info.ChatToGeminiStreamState = state
	}
	return state, nil
}

func sendGeminiStreamResults(c *gin.Context, results []relayconvert.ResponseResult) error {
	for _, result := range results {
		geminiResponse, ok := result.Value.(*dto.GeminiChatResponse)
		if !ok {
			return fmt.Errorf("expected Gemini stream response, got %T", result.Value)
		}
		if geminiResponse == nil {
			continue
		}
		data, err := common.Marshal(geminiResponse)
		if err != nil {
			logger.LogError(c, "failed to marshal gemini response: "+err.Error())
			return err
		}
		c.Render(-1, common.CustomEvent{Data: "data: " + string(data)})
		_ = helper.FlushWriter(c)
	}
	return nil
}

func ProcessStreamResponse(streamResponse dto.ChatCompletionsStreamResponse, responseTextBuilder *strings.Builder, toolCount *int) error {
	for _, choice := range streamResponse.Choices {
		responseTextBuilder.WriteString(choice.Delta.GetContentString())
		responseTextBuilder.WriteString(choice.Delta.GetReasoningContent())
		if choice.Delta.ToolCalls != nil {
			if len(choice.Delta.ToolCalls) > *toolCount {
				*toolCount = len(choice.Delta.ToolCalls)
			}
			for _, tool := range choice.Delta.ToolCalls {
				responseTextBuilder.WriteString(tool.Function.Name)
				responseTextBuilder.WriteString(tool.Function.Arguments)
			}
		}
	}
	return nil
}

func processTokenData(relayMode int, data string, responseTextBuilder *strings.Builder, toolCount *int) (bool, error) {
	if relayMode == relayconstant.RelayModeCompletions {
		var streamResponse dto.CompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			return false, err
		}
		processCompletionsStreamResponse(streamResponse, responseTextBuilder)
		for _, choice := range streamResponse.Choices {
			if strings.TrimSpace(choice.FinishReason) != "" {
				return true, nil
			}
		}
		return false, nil
	}

	// OaiStreamHandler receives chat-completions SSE for converted downstream
	// protocols such as Anthropic Messages, whose route mode is not chat.
	var streamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
		return false, err
	}
	if err := ProcessStreamResponse(streamResponse, responseTextBuilder, toolCount); err != nil {
		return false, err
	}
	return streamResponse.IsFinished(), nil
}

func markResponsesStreamProtocolEnd(info *relaycommon.RelayInfo, eventType string) {
	if info == nil || info.StreamStatus == nil {
		return
	}
	switch eventType {
	case "response.completed", "response.done", "response.incomplete":
		info.StreamStatus.MarkProtocolEnd(eventType)
	}
}

func processCompletionsStreamResponse(streamResponse dto.CompletionsStreamResponse, responseTextBuilder *strings.Builder) {
	for _, choice := range streamResponse.Choices {
		responseTextBuilder.WriteString(choice.Text)
	}
}

func markReportedTextUsage(usage *dto.Usage, usageJSON gjson.Result) {
	if usage == nil || !usageJSON.IsObject() {
		return
	}
	usage.UpstreamInputReported = usageJSON.Get("prompt_tokens").Exists() || usageJSON.Get("input_tokens").Exists()
	usage.UpstreamOutputReported = usageJSON.Get("completion_tokens").Exists() || usageJSON.Get("output_tokens").Exists()
}

func shouldPreserveReportedTextUsage(info *relaycommon.RelayInfo, usage *dto.Usage) bool {
	if info == nil || info.ChannelMeta == nil || usage == nil {
		return false
	}
	if _, enabled := info.UsageEstimationSettings(); enabled {
		return true
	}
	if (info.ChannelOtherSettings.RetryZeroOutput || info.IsKimiK3OfficialCompatibility() || (info.IsGLM53OfficialCompatibility() && !info.IsStream)) && usage.UpstreamInputReported {
		return true
	}
	limits := info.ChannelOtherSettings.UsageTokenLimit
	return limits != nil && ((limits.InputTokens > 0 && usage.UpstreamInputReported) ||
		(limits.OutputTokens > 0 && usage.UpstreamOutputReported))
}

func handleLastResponse(lastStreamData string, responseId *string, createAt *int64,
	systemFingerprint *string, model *string, usage **dto.Usage,
	containStreamUsage *bool, info *relaycommon.RelayInfo,
	shouldSendLastResp *bool) error {

	var lastStreamResponse dto.ChatCompletionsStreamResponse
	if err := common.Unmarshal(common.StringToByteSlice(lastStreamData), &lastStreamResponse); err != nil {
		return err
	}

	*responseId = lastStreamResponse.Id
	*createAt = lastStreamResponse.Created
	*systemFingerprint = lastStreamResponse.GetSystemFingerprint()
	*model = lastStreamResponse.Model
	usageJSON := gjson.Get(lastStreamData, "usage")
	markReportedTextUsage(lastStreamResponse.Usage, usageJSON)

	if service.ValidUsage(lastStreamResponse.Usage) || shouldPreserveReportedTextUsage(info, lastStreamResponse.Usage) {
		*containStreamUsage = true
		*usage = dto.MergeUsageNonZero(*usage, lastStreamResponse.Usage)
		if !info.ShouldIncludeUsage {
			*shouldSendLastResp = lo.SomeBy(lastStreamResponse.Choices, func(choice dto.ChatCompletionsStreamResponseChoice) bool {
				return choice.Delta.GetContentString() != "" || choice.Delta.GetReasoningContent() != ""
			})
		}
	}

	return nil
}

func HandleFinalResponse(c *gin.Context, info *relaycommon.RelayInfo, lastStreamData string,
	responseId string, createAt int64, model string, systemFingerprint string,
	usage *dto.Usage, containStreamUsage bool) {

	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		if info.ShouldIncludeUsage && !containStreamUsage {
			model = downstreamResponseModel(info, model)
			response := helper.GenerateFinalUsageResponse(responseId, createAt, model, *usage)
			response.SetSystemFingerprint(systemFingerprint)
			helper.ObjectData(c, response)
		}
		helper.Done(c)

	case types.RelayFormatClaude:
		var streamResponse dto.ChatCompletionsStreamResponse
		if err := common.Unmarshal(common.StringToByteSlice(lastStreamData), &streamResponse); err != nil {
			common.SysLog("error unmarshalling stream response: " + err.Error())
			return
		}

		info.ClaudeConvertInfo.Usage = usage
		info.SendResponseCount++

		result, err := service.ConvertStreamResponse(c, info, types.RelayFormatClaude, &streamResponse)
		if err != nil {
			common.SysLog("error converting Claude stream response: " + err.Error())
			return
		}
		claudeResponses, ok := result.Value.([]*dto.ClaudeResponse)
		if !ok {
			common.SysLog(fmt.Sprintf("expected Claude stream responses, got %T", result.Value))
			return
		}
		for _, resp := range claudeResponses {
			_ = helper.ClaudeData(c, *resp)
		}
		info.ClaudeConvertInfo.Done = true

	case types.RelayFormatGemini:
		var streamResponse dto.ChatCompletionsStreamResponse
		if err := common.Unmarshal(common.StringToByteSlice(lastStreamData), &streamResponse); err != nil {
			common.SysLog("error unmarshalling stream response: " + err.Error())
			return
		}

		state, err := chatToGeminiStreamState(info, &streamResponse)
		if err != nil {
			common.SysLog("error creating Gemini stream state: " + err.Error())
			return
		}
		state.SetUsage(usage)

		results, err := service.ConvertStreamResponseChunk(c, info, state, &streamResponse)
		if err != nil {
			common.SysLog("error converting final Gemini stream response: " + err.Error())
			return
		}
		if err := sendGeminiStreamResults(c, results); err != nil {
			common.SysLog("error sending final Gemini stream response: " + err.Error())
			return
		}

		results, err = service.FinalizeStreamResponse(c, info, state)
		if err != nil {
			common.SysLog("error finalizing Gemini stream response: " + err.Error())
			return
		}
		if err := sendGeminiStreamResults(c, results); err != nil {
			common.SysLog("error sending finalized Gemini stream response: " + err.Error())
		}
	}
}

func sendResponsesStreamData(c *gin.Context, streamResponse dto.ResponsesStreamResponse, data string) {
	if data == "" {
		return
	}
	_ = helper.ResponseChunkData(c, streamResponse, data)
}
