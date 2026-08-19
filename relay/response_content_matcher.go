package relay

import (
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type responseContentMatcher struct {
	rules    []operation_setting.ResponseContentRetryRule
	text     strings.Builder
	leading  bool
	resolved bool
	matched  bool
}

func newResponseContentMatcher(policy operation_setting.ResponseContentRetryPolicy) *responseContentMatcher {
	if !policy.Enabled || len(policy.Rules) == 0 {
		return nil
	}
	return &responseContentMatcher{
		rules:   append([]operation_setting.ResponseContentRetryRule(nil), policy.Rules...),
		leading: true,
	}
}

func (m *responseContentMatcher) append(text string) {
	if m == nil || m.resolved || m.matched || text == "" {
		return
	}
	if m.leading {
		text = strings.TrimLeftFunc(text, unicode.IsSpace)
		if text == "" {
			return
		}
		m.leading = false
	}
	m.text.WriteString(text)
	candidate := m.text.String()
	possible := false
	for _, rule := range m.rules {
		switch rule.Mode {
		case operation_setting.ResponseContentMatchPrefix:
			if strings.HasPrefix(candidate, rule.Content) {
				m.matched = true
				return
			}
			if strings.HasPrefix(rule.Content, candidate) {
				possible = true
			}
		case operation_setting.ResponseContentMatchExact:
			if strings.HasPrefix(rule.Content, candidate) {
				possible = true
			}
		}
	}
	if !possible {
		m.resolved = true
	}
}

func (m *responseContentMatcher) finish() bool {
	if m == nil || m.resolved {
		return false
	}
	if m.matched {
		return true
	}
	candidate := m.text.String()
	for _, rule := range m.rules {
		if rule.Mode == operation_setting.ResponseContentMatchExact && candidate == rule.Content {
			m.matched = true
			return true
		}
	}
	m.resolved = true
	return false
}

func (m *responseContentMatcher) resolvedWithoutMatch() bool {
	return m == nil || m.resolved
}

func (m *responseContentMatcher) hasVisibleText() bool {
	return m != nil && (!m.leading || m.text.Len() > 0)
}

func (m *responseContentMatcher) disable() {
	if m == nil {
		return
	}
	m.resolved = true
	m.matched = false
}

func observeVisibleResponsePayload(format types.RelayFormat, relayMode int, payload map[string]any, matcher *responseContentMatcher, stream bool) {
	if matcher == nil || matcher.resolved || matcher.matched {
		return
	}
	if observeResponseError(payload, matcher) {
		return
	}
	switch format {
	case types.RelayFormatOpenAI:
		observeVisibleOpenAI(payload, relayMode, matcher)
	case types.RelayFormatOpenAIResponses:
		observeVisibleResponses(payload, matcher, stream)
	case types.RelayFormatClaude:
		observeVisibleClaude(payload, matcher, stream)
	case types.RelayFormatGemini:
		observeVisibleGemini(payload, matcher)
	}
}

func observeResponseError(payload map[string]any, matcher *responseContentMatcher) bool {
	if errorValue, ok := payload["error"]; ok && errorValue != nil {
		if appendResponseErrorText(errorValue, matcher) {
			return true
		}
		return appendResponseErrorText(payload, matcher)
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if errorValue, exists := response["error"]; exists && errorValue != nil {
			if appendResponseErrorText(errorValue, matcher) {
				return true
			}
			return appendResponseErrorText(response, matcher)
		}
	}
	eventType, _ := payload["type"].(string)
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	if eventType == "error" || strings.HasSuffix(eventType, "_error") || strings.HasSuffix(eventType, ".error") ||
		strings.HasSuffix(eventType, "_failed") || strings.HasSuffix(eventType, ".failed") {
		return appendResponseErrorText(payload, matcher)
	}
	return false
}

func appendResponseErrorText(value any, matcher *responseContentMatcher) bool {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return false
		}
		matcher.append(typed)
		return true
	case map[string]any:
		for _, key := range []string{"message", "msg", "error_message", "error_msg", "detail"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				matcher.append(text)
				return true
			}
		}
		if nested, ok := typed["error"]; ok && nested != nil {
			return appendResponseErrorText(nested, matcher)
		}
	}
	return false
}

func observeVisibleOpenAI(payload map[string]any, relayMode int, matcher *responseContentMatcher) {
	choices, _ := payload["choices"].([]any)
	for _, choiceValue := range choices {
		choice, _ := choiceValue.(map[string]any)
		if choice == nil {
			continue
		}
		if relayMode == constant.RelayModeCompletions {
			appendVisibleString(choice["text"], matcher)
		}
		for _, key := range []string{"message", "delta"} {
			message, _ := choice[key].(map[string]any)
			if message == nil {
				continue
			}
			appendVisibleValue(message["content"], matcher)
			appendVisibleValue(message["refusal"], matcher)
		}
	}
}

func observeVisibleResponses(payload map[string]any, matcher *responseContentMatcher, stream bool) {
	if stream {
		eventType, _ := payload["type"].(string)
		switch eventType {
		case "response.output_text.delta", "response.refusal.delta":
			appendVisibleString(payload["delta"], matcher)
			appendVisibleString(payload["refusal"], matcher)
		case "response.completed":
			if !matcher.hasVisibleText() {
				if response, ok := payload["response"].(map[string]any); ok {
					observeVisibleResponses(response, matcher, false)
				}
			}
		}
		return
	}
	appendVisibleValue(payload["output_text"], matcher)
	output, _ := payload["output"].([]any)
	for _, itemValue := range output {
		item, _ := itemValue.(map[string]any)
		if item == nil {
			continue
		}
		content, _ := item["content"].([]any)
		for _, partValue := range content {
			part, _ := partValue.(map[string]any)
			if part == nil {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "output_text", "text":
				appendVisibleString(part["text"], matcher)
			case "refusal":
				appendVisibleString(part["refusal"], matcher)
				appendVisibleString(part["text"], matcher)
			}
		}
	}
}

func observeVisibleClaude(payload map[string]any, matcher *responseContentMatcher, stream bool) {
	appendVisibleString(payload["completion"], matcher)
	if !stream {
		content, _ := payload["content"].([]any)
		for _, blockValue := range content {
			appendVisibleClaudeBlock(blockValue, matcher)
		}
		return
	}
	eventType, _ := payload["type"].(string)
	switch eventType {
	case "content_block_start":
		appendVisibleClaudeBlock(payload["content_block"], matcher)
	case "content_block_delta":
		appendVisibleClaudeBlock(payload["delta"], matcher)
	}
}

func appendVisibleClaudeBlock(value any, matcher *responseContentMatcher) {
	block, _ := value.(map[string]any)
	if block == nil {
		return
	}
	blockType, _ := block["type"].(string)
	if blockType == "text" || blockType == "text_delta" || blockType == "refusal" || blockType == "refusal_delta" {
		appendVisibleString(block["text"], matcher)
		appendVisibleString(block["refusal"], matcher)
	}
}

func observeVisibleGemini(payload map[string]any, matcher *responseContentMatcher) {
	candidates, _ := payload["candidates"].([]any)
	for _, candidateValue := range candidates {
		candidate, _ := candidateValue.(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, partValue := range parts {
			part, _ := partValue.(map[string]any)
			if part == nil {
				continue
			}
			if thought, _ := part["thought"].(bool); thought {
				continue
			}
			appendVisibleString(part["text"], matcher)
		}
	}
}

func appendVisibleValue(value any, matcher *responseContentMatcher) {
	switch typed := value.(type) {
	case string:
		matcher.append(typed)
	case []any:
		for _, item := range typed {
			appendVisibleValue(item, matcher)
		}
	case map[string]any:
		partType, _ := typed["type"].(string)
		if partType == "text" || partType == "output_text" || partType == "refusal" || partType == "text_delta" || partType == "refusal_delta" {
			appendVisibleString(typed["text"], matcher)
			appendVisibleString(typed["refusal"], matcher)
		}
	}
}

func appendVisibleString(value any, matcher *responseContentMatcher) {
	if text, ok := value.(string); ok {
		matcher.append(text)
	}
}
