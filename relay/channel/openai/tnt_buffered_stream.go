package openai

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type bufferedChatChoice struct {
	index        int
	content      strings.Builder
	reasoning    strings.Builder
	finishReason string
	tools        map[int]*dto.ToolCallRequest
}

type bufferedChatStream struct {
	id      string
	model   string
	created int64
	usage   *dto.Usage
	choices map[int]*bufferedChatChoice
}

func OaiChatBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	buffered := &bufferedChatStream{choices: make(map[int]*bufferedChatChoice)}
	var stopFilter *relayconvert.KimiK3ChatStreamStopFilter
	if info.IsKimiK3OfficialCompatibility() {
		finishReason := "stop"
		if info.RelayFormat == types.RelayFormatClaude {
			finishReason = "stop_sequence"
		}
		stopFilter = relayconvert.NewKimiK3ChatStreamStopFilter(relayconvert.KimiK3StopSequencesFromRequest(info.Request), finishReason)
	}
	var streamErr *types.NewAPIError
	info.RequireStreamProtocolEnd()
	streamRetryErr := helper.StreamScannerHandler(c, resp, info, func(data string, result *helper.StreamResult) {
		var errorResp dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
			if openAIError := errorResp.GetOpenAIError(); openAIError != nil && openAIError.Type != "" {
				streamErr = types.WithOpenAIError(*openAIError, resp.StatusCode)
				result.Stop(streamErr)
				return
			}
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			result.Error(err)
			return
		}
		if info.IsTNTTencentOpenAIConversion() {
			relayconvert.SanitizeTNTTencentChatStreamChunk(&chunk)
		}
		stopFilter.Filter(&chunk)
		if matched := stopFilter.MatchedSequence(); matched != "" {
			info.KimiK3MatchedStopSequence = matched
		}
		buffered.add(&chunk)
		if chunk.IsFinished() && info.StreamStatus != nil {
			info.StreamStatus.MarkProtocolEnd("finish_reason")
		}
	})
	if streamErr != nil {
		return nil, streamErr
	}
	if streamRetryErr != nil {
		return nil, streamRetryErr
	}

	chatResponse := buffered.response()
	if chatResponse.Usage.TotalTokens == 0 {
		chatResponse.Usage = *service.ResponseText2Usage(c, buffered.outputText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	body, err := common.Marshal(chatResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	synthetic := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
	synthetic.Header.Set("Content-Type", "application/json")
	if info.RelayFormat == types.RelayFormatOpenAIResponses {
		return OaiChatToResponsesHandler(c, info, synthetic)
	}
	return OpenaiHandler(c, info, synthetic)
}

func sanitizeTNTStreamData(info *relaycommon.RelayInfo, data string) (string, error) {
	if !info.IsTNTTencentOpenAIConversion() || data == "" {
		return data, nil
	}
	var chunk dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
		return "", err
	}
	relayconvert.SanitizeTNTTencentChatStreamChunk(&chunk)
	encoded, err := common.Marshal(chunk)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (buffered *bufferedChatStream) add(chunk *dto.ChatCompletionsStreamResponse) {
	if chunk == nil {
		return
	}
	if buffered.id == "" {
		buffered.id = chunk.Id
	}
	if buffered.model == "" {
		buffered.model = chunk.Model
	}
	if buffered.created == 0 {
		buffered.created = chunk.Created
	}
	if chunk.Usage != nil {
		usage := *chunk.Usage
		buffered.usage = &usage
	}
	for _, chunkChoice := range chunk.Choices {
		choice := buffered.choices[chunkChoice.Index]
		if choice == nil {
			choice = &bufferedChatChoice{index: chunkChoice.Index, tools: make(map[int]*dto.ToolCallRequest)}
			buffered.choices[chunkChoice.Index] = choice
		}
		choice.content.WriteString(chunkChoice.Delta.GetContentString())
		choice.reasoning.WriteString(chunkChoice.Delta.GetReasoningContent())
		if chunkChoice.FinishReason != nil {
			choice.finishReason = *chunkChoice.FinishReason
		}
		for fallbackIndex, toolDelta := range chunkChoice.Delta.ToolCalls {
			toolIndex := fallbackIndex
			if toolDelta.Index != nil {
				toolIndex = *toolDelta.Index
			}
			tool := choice.tools[toolIndex]
			if tool == nil {
				tool = &dto.ToolCallRequest{Type: "function"}
				choice.tools[toolIndex] = tool
			}
			if toolDelta.ID != "" {
				tool.ID = toolDelta.ID
			}
			if toolType := common.Interface2String(toolDelta.Type); toolType != "" {
				tool.Type = toolType
			}
			if toolDelta.Function.Name != "" {
				tool.Function.Name = toolDelta.Function.Name
			}
			tool.Function.Arguments += toolDelta.Function.Arguments
		}
	}
}

func (buffered *bufferedChatStream) response() *dto.OpenAITextResponse {
	indexes := make([]int, 0, len(buffered.choices))
	for index := range buffered.choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	choices := make([]dto.OpenAITextResponseChoice, 0, len(indexes))
	for _, index := range indexes {
		choice := buffered.choices[index]
		message := dto.Message{Role: "assistant", Content: choice.content.String()}
		if reasoning := choice.reasoning.String(); reasoning != "" {
			message.ReasoningContent = &reasoning
		}
		toolIndexes := make([]int, 0, len(choice.tools))
		for toolIndex := range choice.tools {
			toolIndexes = append(toolIndexes, toolIndex)
		}
		sort.Ints(toolIndexes)
		if len(toolIndexes) > 0 {
			tools := make([]dto.ToolCallRequest, 0, len(toolIndexes))
			for _, toolIndex := range toolIndexes {
				tools = append(tools, *choice.tools[toolIndex])
			}
			message.ToolCalls, _ = common.Marshal(tools)
		}
		choices = append(choices, dto.OpenAITextResponseChoice{
			Index:        choice.index,
			Message:      message,
			FinishReason: choice.finishReason,
		})
	}
	usage := dto.Usage{}
	if buffered.usage != nil {
		usage = *buffered.usage
	}
	return &dto.OpenAITextResponse{
		Id:      buffered.id,
		Object:  "chat.completion",
		Created: buffered.created,
		Model:   buffered.model,
		Choices: choices,
		Usage:   usage,
	}
}

func (buffered *bufferedChatStream) outputText() string {
	indexes := make([]int, 0, len(buffered.choices))
	for index := range buffered.choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var output strings.Builder
	for _, index := range indexes {
		choice := buffered.choices[index]
		output.WriteString(choice.content.String())
		output.WriteString(choice.reasoning.String())
		for _, tool := range choice.tools {
			output.WriteString(tool.Function.Name)
			output.WriteString(tool.Function.Arguments)
		}
	}
	return output.String()
}
