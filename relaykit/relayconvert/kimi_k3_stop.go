package relayconvert

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

type stopMatcher struct {
	sequences []string
	pending   string
	matched   string
	didMatch  bool
}

func newStopMatcher(sequences []string) *stopMatcher {
	return &stopMatcher{sequences: append([]string(nil), sequences...)}
}

func (m *stopMatcher) push(text string) string {
	if m == nil || len(m.sequences) == 0 {
		return text
	}
	if m.didMatch {
		return ""
	}

	combined := m.pending + text
	matchIndex := -1
	matchedSequence := ""
	for _, sequence := range m.sequences {
		if index := strings.Index(combined, sequence); index >= 0 && (matchIndex < 0 || index < matchIndex) {
			matchIndex = index
			matchedSequence = sequence
		}
	}
	if matchIndex >= 0 {
		m.pending = ""
		m.matched = matchedSequence
		m.didMatch = true
		return combined[:matchIndex]
	}

	pendingBytes := 0
	for _, sequence := range m.sequences {
		limit := min(len(sequence)-1, len(combined))
		for length := limit; length > pendingBytes; length-- {
			if strings.HasSuffix(combined, sequence[:length]) {
				pendingBytes = length
				break
			}
		}
	}
	emitUntil := len(combined) - pendingBytes
	m.pending = combined[emitUntil:]
	return combined[:emitUntil]
}

func (m *stopMatcher) flush() string {
	if m == nil || m.didMatch {
		return ""
	}
	pending := m.pending
	m.pending = ""
	return pending
}

func StopSequencesFromRequest(request any) []string {
	switch value := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if value == nil {
			return nil
		}
		return kimiK3StopSequences(value.Stop)
	case dto.GeneralOpenAIRequest:
		return kimiK3StopSequences(value.Stop)
	case *dto.ClaudeRequest:
		if value == nil {
			return nil
		}
		return append([]string(nil), value.StopSequences...)
	case dto.ClaudeRequest:
		return append([]string(nil), value.StopSequences...)
	default:
		return nil
	}
}

func KimiK3StopSequencesFromRequest(request any) []string {
	return StopSequencesFromRequest(request)
}

func kimiK3StopSequences(stop any) []string {
	switch value := stop.(type) {
	case string:
		return []string{value}
	case []string:
		return append([]string(nil), value...)
	case []any:
		sequences := make([]string, 0, len(value))
		for _, item := range value {
			sequence, ok := item.(string)
			if !ok {
				return nil
			}
			sequences = append(sequences, sequence)
		}
		return sequences
	default:
		return nil
	}
}

func ApplyStopToChatResponse(response *dto.OpenAITextResponse, sequences []string, matchedFinishReason ...string) string {
	matched, _ := ApplyStopToChatResponseWithMatch(response, sequences, matchedFinishReason...)
	return matched
}

func ApplyStopToChatResponseWithMatch(response *dto.OpenAITextResponse, sequences []string, matchedFinishReason ...string) (string, bool) {
	return applyStopToChatResponse(response, sequences, false, matchedFinishReason...)
}

func ApplyGLM53StopToChatResponse(response *dto.OpenAITextResponse, sequences []string, matchedFinishReason ...string) (string, bool) {
	return applyStopToChatResponse(response, sequences, true, matchedFinishReason...)
}

func applyStopToChatResponse(response *dto.OpenAITextResponse, sequences []string, filterReasoning bool, matchedFinishReason ...string) (string, bool) {
	if response == nil || len(sequences) == 0 {
		return "", false
	}
	matchedSequence := ""
	matchedAny := false
	for index := range response.Choices {
		contentMatcher := newStopMatcher(sequences)
		var reasoningMatcher *stopMatcher
		if filterReasoning {
			message := &response.Choices[index].Message
			if message.ReasoningContent != nil {
				reasoningMatcher = newStopMatcher(sequences)
				filtered := reasoningMatcher.push(*message.ReasoningContent) + reasoningMatcher.flush()
				message.ReasoningContent = &filtered
			} else if message.Reasoning != nil {
				reasoningMatcher = newStopMatcher(sequences)
				filtered := reasoningMatcher.push(*message.Reasoning) + reasoningMatcher.flush()
				message.Reasoning = &filtered
			}
		}
		content := response.Choices[index].Message.StringContent()
		filtered := ""
		if reasoningMatcher == nil || !reasoningMatcher.didMatch {
			filtered = contentMatcher.push(content) + contentMatcher.flush()
		}
		response.Choices[index].Message.SetStringContent(filtered)
		matched := contentMatcher
		if reasoningMatcher != nil && reasoningMatcher.didMatch {
			matched = reasoningMatcher
		}
		if matched.didMatch {
			response.Choices[index].FinishReason = matchedStopFinishReason(matchedFinishReason)
			if !matchedAny {
				matchedSequence = matched.matched
			}
			matchedAny = true
		}
	}
	return matchedSequence, matchedAny
}

func ApplyKimiK3StopToChatResponse(response *dto.OpenAITextResponse, sequences []string, matchedFinishReason ...string) string {
	return ApplyStopToChatResponse(response, sequences, matchedFinishReason...)
}

type ChatStreamStopFilter struct {
	sequences        []string
	matchers         map[int]*stopMatcher
	reasoningMatcher map[int]*stopMatcher
	finish           string
	filterReasoning  bool
}

func NewChatStreamStopFilter(sequences []string, matchedFinishReason ...string) *ChatStreamStopFilter {
	return newChatStreamStopFilter(sequences, false, matchedFinishReason...)
}

func NewGLM53ChatStreamStopFilter(sequences []string, matchedFinishReason ...string) *ChatStreamStopFilter {
	return newChatStreamStopFilter(sequences, true, matchedFinishReason...)
}

func newChatStreamStopFilter(sequences []string, filterReasoning bool, matchedFinishReason ...string) *ChatStreamStopFilter {
	if len(sequences) == 0 {
		return nil
	}
	return &ChatStreamStopFilter{
		sequences:        append([]string(nil), sequences...),
		matchers:         make(map[int]*stopMatcher),
		reasoningMatcher: make(map[int]*stopMatcher),
		finish:           matchedStopFinishReason(matchedFinishReason),
		filterReasoning:  filterReasoning,
	}
}

func (f *ChatStreamStopFilter) Filter(chunk *dto.ChatCompletionsStreamResponse) {
	if f == nil || chunk == nil {
		return
	}
	for index := range chunk.Choices {
		choice := &chunk.Choices[index]
		matcher := f.matchers[choice.Index]
		if matcher == nil {
			matcher = newStopMatcher(f.sequences)
			f.matchers[choice.Index] = matcher
		}
		reasoningMatcher := f.reasoningMatcher[choice.Index]
		if reasoningMatcher == nil {
			reasoningMatcher = newStopMatcher(f.sequences)
			f.reasoningMatcher[choice.Index] = reasoningMatcher
		}
		if f.filterReasoning {
			if choice.Delta.ReasoningContent != nil {
				filtered := reasoningMatcher.push(*choice.Delta.ReasoningContent)
				choice.Delta.ReasoningContent = &filtered
			} else if choice.Delta.Reasoning != nil {
				filtered := reasoningMatcher.push(*choice.Delta.Reasoning)
				choice.Delta.Reasoning = &filtered
			}
		}
		if choice.Delta.Content != nil {
			if f.filterReasoning {
				appendChatDeltaReasoning(&choice.Delta, reasoningMatcher.flush())
			}
			if reasoningMatcher.didMatch {
				choice.Delta.SetContentString("")
			} else {
				choice.Delta.SetContentString(matcher.push(choice.Delta.GetContentString()))
			}
		}
		if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
			if f.filterReasoning {
				appendChatDeltaReasoning(&choice.Delta, reasoningMatcher.flush())
			}
			if pending := matcher.flush(); pending != "" {
				choice.Delta.SetContentString(choice.Delta.GetContentString() + pending)
			}
			if matcher.didMatch || reasoningMatcher.didMatch {
				choice.FinishReason = &f.finish
			}
		}
	}
}

func appendChatDeltaReasoning(delta *dto.ChatCompletionsStreamResponseChoiceDelta, text string) {
	if delta == nil || text == "" {
		return
	}
	if delta.ReasoningContent != nil {
		value := *delta.ReasoningContent + text
		delta.ReasoningContent = &value
		return
	}
	if delta.Reasoning != nil {
		value := *delta.Reasoning + text
		delta.Reasoning = &value
		return
	}
	delta.ReasoningContent = &text
}

func (f *ChatStreamStopFilter) MatchedSequence() string {
	if f == nil {
		return ""
	}
	for _, matcher := range f.reasoningMatcher {
		if matcher.didMatch {
			return matcher.matched
		}
	}
	for _, matcher := range f.matchers {
		if matcher.didMatch {
			return matcher.matched
		}
	}
	return ""
}

func matchedStopFinishReason(values []string) string {
	if len(values) > 0 && strings.TrimSpace(values[0]) != "" {
		return values[0]
	}
	return "stop"
}

func ApplyStopToClaudeResponse(response *dto.ClaudeResponse, sequences []string) string {
	matched, _ := applyStopToClaudeResponse(response, sequences, true)
	return matched
}

func ApplyGLM53StopToClaudeResponse(response *dto.ClaudeResponse, sequences []string) (string, bool) {
	return applyStopToClaudeResponse(response, sequences, false)
}

func applyStopToClaudeResponse(response *dto.ClaudeResponse, sequences []string, reportStopSequence bool) (string, bool) {
	if response == nil || len(sequences) == 0 {
		return "", false
	}
	matcher := newStopMatcher(sequences)
	lastTextIndex := -1
	for index := range response.Content {
		if response.Content[index].Type != "text" {
			continue
		}
		lastTextIndex = index
		response.Content[index].SetText(matcher.push(response.Content[index].GetText()))
	}
	if pending := matcher.flush(); pending != "" && lastTextIndex >= 0 {
		response.Content[lastTextIndex].SetText(response.Content[lastTextIndex].GetText() + pending)
	}
	if matcher.didMatch && reportStopSequence {
		response.StopReason = "stop_sequence"
		response.StopSequence = &matcher.matched
	}
	return matcher.matched, matcher.didMatch
}

func ApplyKimiK3StopToClaudeResponse(response *dto.ClaudeResponse, sequences []string) {
	_ = ApplyStopToClaudeResponse(response, sequences)
}

type ClaudeStreamStopFilter struct {
	matcher            *stopMatcher
	textIndex          int
	reportStopSequence bool
}

func NewClaudeStreamStopFilter(sequences []string) *ClaudeStreamStopFilter {
	return newClaudeStreamStopFilter(sequences, true)
}

func NewGLM53ClaudeStreamStopFilter(sequences []string) *ClaudeStreamStopFilter {
	return newClaudeStreamStopFilter(sequences, false)
}

func newClaudeStreamStopFilter(sequences []string, reportStopSequence bool) *ClaudeStreamStopFilter {
	if len(sequences) == 0 {
		return nil
	}
	return &ClaudeStreamStopFilter{
		matcher:            newStopMatcher(sequences),
		textIndex:          -1,
		reportStopSequence: reportStopSequence,
	}
}

func (f *ClaudeStreamStopFilter) Filter(response *dto.ClaudeResponse) []*dto.ClaudeResponse {
	if response == nil {
		return nil
	}
	if f == nil {
		return []*dto.ClaudeResponse{response}
	}

	if response.Type == "content_block_start" && response.ContentBlock != nil && response.ContentBlock.Type == "text" {
		f.textIndex = response.GetIndex()
		if response.ContentBlock.Text != nil {
			response.ContentBlock.SetText(f.matcher.push(response.ContentBlock.GetText()))
		}
	}
	if response.Type == "content_block_delta" && response.Delta != nil && response.Delta.Type == "text_delta" {
		f.textIndex = response.GetIndex()
		if response.Delta.Text != nil {
			response.Delta.SetText(f.matcher.push(response.Delta.GetText()))
		}
	}

	terminal := response.Type == "message_delta" || response.Type == "message_stop"
	if !terminal {
		return []*dto.ClaudeResponse{response}
	}

	result := make([]*dto.ClaudeResponse, 0, 2)
	if pending := f.matcher.flush(); pending != "" && f.textIndex >= 0 {
		result = append(result, &dto.ClaudeResponse{
			Type:  "content_block_delta",
			Index: &f.textIndex,
			Delta: &dto.ClaudeMediaMessage{
				Type: "text_delta",
				Text: &pending,
			},
		})
	}
	if response.Type == "message_delta" && f.matcher.didMatch && f.reportStopSequence {
		if response.Delta == nil {
			response.Delta = &dto.ClaudeMediaMessage{}
		}
		stopReason := "stop_sequence"
		response.Delta.StopReason = &stopReason
		response.Delta.StopSequence = &f.matcher.matched
	}
	result = append(result, response)
	return result
}

type KimiK3ChatStreamStopFilter = ChatStreamStopFilter

func NewKimiK3ChatStreamStopFilter(sequences []string, matchedFinishReason ...string) *KimiK3ChatStreamStopFilter {
	return NewChatStreamStopFilter(sequences, matchedFinishReason...)
}

type KimiK3ClaudeStreamStopFilter = ClaudeStreamStopFilter

func NewKimiK3ClaudeStreamStopFilter(sequences []string) *KimiK3ClaudeStreamStopFilter {
	return NewClaudeStreamStopFilter(sequences)
}
