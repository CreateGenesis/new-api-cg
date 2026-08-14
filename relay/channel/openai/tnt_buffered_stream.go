package openai

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
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

const tntJSONOpeningFence = "```json"

var tntJSONClosingFences = []string{
	"```", "```\n", "```\r\n",
	"\n```", "\n```\n", "\n```\r\n",
	"\r\n```", "\r\n```\n", "\r\n```\r\n",
}

type tntJSONFenceStreamFilter struct {
	choices map[int]*tntJSONFenceChoiceFilter
}

type tntJSONFenceChoiceFilter struct {
	state   uint8
	pending string
}

const (
	tntJSONFenceOpening uint8 = iota
	tntJSONFenceOpeningLineEnd
	tntJSONFenceBody
	tntJSONFencePassThrough
)

func newTNTJSONFenceStreamFilter(info *relaycommon.RelayInfo) *tntJSONFenceStreamFilter {
	if info == nil || !info.IsTNTTencentOpenAIConversion() || !isTNTJSONResponseRequest(info.Request) {
		return nil
	}
	return &tntJSONFenceStreamFilter{choices: make(map[int]*tntJSONFenceChoiceFilter)}
}

func isTNTJSONResponseRequest(request dto.Request) bool {
	switch typed := request.(type) {
	case *dto.GeneralOpenAIRequest:
		return typed != nil && typed.ResponseFormat != nil && isTNTJSONResponseFormatType(typed.ResponseFormat.Type)
	case *dto.ClaudeRequest:
		if typed == nil {
			return false
		}
		raw := typed.ResponseFormat
		if len(raw) == 0 {
			raw = typed.OutputFormat
		}
		var format dto.ResponseFormat
		return common.Unmarshal(raw, &format) == nil && isTNTJSONResponseFormatType(format.Type)
	case *dto.OpenAIResponsesRequest:
		if typed == nil {
			return false
		}
		var text struct {
			Format dto.ResponseFormat `json:"format"`
		}
		return common.Unmarshal(typed.Text, &text) == nil && isTNTJSONResponseFormatType(text.Format.Type)
	default:
		return false
	}
}

func isTNTJSONResponseFormatType(formatType string) bool {
	return formatType == "json_object" || formatType == "json_schema"
}

func (f *tntJSONFenceStreamFilter) Filter(chunk *dto.ChatCompletionsStreamResponse) {
	if f == nil || chunk == nil {
		return
	}
	for index := range chunk.Choices {
		choice := &chunk.Choices[index]
		filter := f.choices[choice.Index]
		if filter == nil {
			filter = &tntJSONFenceChoiceFilter{}
			f.choices[choice.Index] = filter
		}
		content := ""
		if choice.Delta.Content != nil {
			content = *choice.Delta.Content
		}
		filtered := filter.write(content)
		if choice.FinishReason != nil {
			filtered += filter.finish()
			delete(f.choices, choice.Index)
		}
		if choice.Delta.Content != nil || filtered != "" {
			choice.Delta.SetContentString(filtered)
		}
	}
}

func (f *tntJSONFenceStreamFilter) FilterResponse(response *dto.OpenAITextResponse) {
	if f == nil || response == nil {
		return
	}
	for index := range response.Choices {
		content := response.Choices[index].Message.StringContent()
		if content == "" && !response.Choices[index].Message.IsStringContent() {
			continue
		}
		filter := &tntJSONFenceChoiceFilter{}
		response.Choices[index].Message.SetStringContent(filter.write(content) + filter.finish())
	}
}

func (f *tntJSONFenceChoiceFilter) write(content string) string {
	switch f.state {
	case tntJSONFenceOpening:
		f.pending += content
		if len(f.pending) < len(tntJSONOpeningFence) {
			if strings.HasPrefix(tntJSONOpeningFence, f.pending) {
				return ""
			}
			f.state = tntJSONFencePassThrough
			output := f.pending
			f.pending = ""
			return output
		}
		if !strings.HasPrefix(f.pending, tntJSONOpeningFence) {
			f.state = tntJSONFencePassThrough
			output := f.pending
			f.pending = ""
			return output
		}
		content = f.pending[len(tntJSONOpeningFence):]
		f.pending = ""
		f.state = tntJSONFenceOpeningLineEnd
		return f.write(content)
	case tntJSONFenceOpeningLineEnd:
		f.pending += content
		if f.pending == "" || f.pending == "\r" {
			return ""
		}
		if strings.HasPrefix(f.pending, "\r\n") {
			content = f.pending[2:]
		} else if strings.HasPrefix(f.pending, "\n") {
			content = f.pending[1:]
		} else {
			content = f.pending
		}
		f.pending = ""
		f.state = tntJSONFenceBody
		return f.write(content)
	case tntJSONFenceBody:
		candidate := f.pending + content
		keep := 0
		for length := 1; length <= len(candidate); length++ {
			suffix := candidate[len(candidate)-length:]
			for _, fence := range tntJSONClosingFences {
				if strings.HasPrefix(fence, suffix) {
					keep = length
					break
				}
			}
		}
		f.pending = candidate[len(candidate)-keep:]
		return candidate[:len(candidate)-keep]
	default:
		return content
	}
}

func (f *tntJSONFenceChoiceFilter) finish() string {
	defer func() {
		f.pending = ""
		f.state = tntJSONFencePassThrough
	}()
	switch f.state {
	case tntJSONFenceOpening:
		return f.pending
	case tntJSONFenceOpeningLineEnd:
		return f.pending
	case tntJSONFenceBody:
		for _, fence := range tntJSONClosingFences {
			if f.pending == fence {
				return ""
			}
		}
		return f.pending
	default:
		return ""
	}
}

func OaiChatBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	buffered := &bufferedChatStream{choices: make(map[int]*bufferedChatChoice)}
	jsonFenceFilter := newTNTJSONFenceStreamFilter(info)
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
	streamRetryErr := helper.StreamScannerHandlerWithOptions(c, resp, info, helper.StreamScannerOptions{BufferedResponse: true}, func(data string, result *helper.StreamResult) {
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
		jsonFenceFilter.Filter(&chunk)
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

func sanitizeTNTStreamData(info *relaycommon.RelayInfo, filter *tntJSONFenceStreamFilter, data string) (string, error) {
	if !info.IsTNTTencentOpenAIConversion() || data == "" {
		return data, nil
	}
	var chunk dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
		return "", err
	}
	relayconvert.SanitizeTNTTencentChatStreamChunk(&chunk)
	filter.Filter(&chunk)
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
