package relayconvert

import (
	"strings"

	"github.com/QuantumNous/new-api/dto"
)

type kimiK3StopMatcher struct {
	sequences []string
	pending   string
	matched   string
}

func newKimiK3StopMatcher(sequences []string) *kimiK3StopMatcher {
	return &kimiK3StopMatcher{sequences: append([]string(nil), sequences...)}
}

func (m *kimiK3StopMatcher) push(text string) string {
	if m == nil || len(m.sequences) == 0 {
		return text
	}
	if m.matched != "" {
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

func (m *kimiK3StopMatcher) flush() string {
	if m == nil || m.matched != "" {
		return ""
	}
	pending := m.pending
	m.pending = ""
	return pending
}

func KimiK3StopSequencesFromRequest(request any) []string {
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

func kimiK3StopSequences(stop any) []string {
	switch value := stop.(type) {
	case string:
		return []string{value}
	case []string:
		return append([]string(nil), value...)
	default:
		return nil
	}
}

func ApplyKimiK3StopToChatResponse(response *dto.OpenAITextResponse, sequences []string, matchedFinishReason ...string) string {
	if response == nil || len(sequences) == 0 {
		return ""
	}
	matchedSequence := ""
	for index := range response.Choices {
		matcher := newKimiK3StopMatcher(sequences)
		content := response.Choices[index].Message.StringContent()
		filtered := matcher.push(content) + matcher.flush()
		response.Choices[index].Message.SetStringContent(filtered)
		if matcher.matched != "" {
			response.Choices[index].FinishReason = kimiK3MatchedFinishReason(matchedFinishReason)
			if matchedSequence == "" {
				matchedSequence = matcher.matched
			}
		}
	}
	return matchedSequence
}

type KimiK3ChatStreamStopFilter struct {
	sequences []string
	matchers  map[int]*kimiK3StopMatcher
	finish    string
}

func NewKimiK3ChatStreamStopFilter(sequences []string, matchedFinishReason ...string) *KimiK3ChatStreamStopFilter {
	if len(sequences) == 0 {
		return nil
	}
	return &KimiK3ChatStreamStopFilter{
		sequences: append([]string(nil), sequences...),
		matchers:  make(map[int]*kimiK3StopMatcher),
		finish:    kimiK3MatchedFinishReason(matchedFinishReason),
	}
}

func (f *KimiK3ChatStreamStopFilter) Filter(chunk *dto.ChatCompletionsStreamResponse) {
	if f == nil || chunk == nil {
		return
	}
	for index := range chunk.Choices {
		choice := &chunk.Choices[index]
		matcher := f.matchers[choice.Index]
		if matcher == nil {
			matcher = newKimiK3StopMatcher(f.sequences)
			f.matchers[choice.Index] = matcher
		}
		if choice.Delta.Content != nil {
			choice.Delta.SetContentString(matcher.push(choice.Delta.GetContentString()))
		}
		if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
			if pending := matcher.flush(); pending != "" {
				choice.Delta.SetContentString(choice.Delta.GetContentString() + pending)
			}
			if matcher.matched != "" {
				choice.FinishReason = &f.finish
			}
		}
	}
}

func (f *KimiK3ChatStreamStopFilter) MatchedSequence() string {
	if f == nil {
		return ""
	}
	for _, matcher := range f.matchers {
		if matcher.matched != "" {
			return matcher.matched
		}
	}
	return ""
}

func kimiK3MatchedFinishReason(values []string) string {
	if len(values) > 0 && strings.TrimSpace(values[0]) != "" {
		return values[0]
	}
	return "stop"
}

func ApplyKimiK3StopToClaudeResponse(response *dto.ClaudeResponse, sequences []string) {
	if response == nil || len(sequences) == 0 {
		return
	}
	matcher := newKimiK3StopMatcher(sequences)
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
	if matcher.matched != "" {
		response.StopReason = "stop_sequence"
		response.StopSequence = &matcher.matched
	}
}

type KimiK3ClaudeStreamStopFilter struct {
	matcher   *kimiK3StopMatcher
	textIndex int
}

func NewKimiK3ClaudeStreamStopFilter(sequences []string) *KimiK3ClaudeStreamStopFilter {
	if len(sequences) == 0 {
		return nil
	}
	return &KimiK3ClaudeStreamStopFilter{
		matcher:   newKimiK3StopMatcher(sequences),
		textIndex: -1,
	}
}

func (f *KimiK3ClaudeStreamStopFilter) Filter(response *dto.ClaudeResponse) []*dto.ClaudeResponse {
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
	if response.Type == "message_delta" && f.matcher.matched != "" {
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
