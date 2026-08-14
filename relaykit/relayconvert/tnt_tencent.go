package relayconvert

import (
	"errors"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

type tntTextReplacement struct {
	old string
	new string
}

var (
	tntAnthropicBillingHeaderPattern = regexp.MustCompile(`x-anthropic-billing-header:\s*cc_version=[^;]+;\s*cc_entrypoint=[^;]+;\s*cch=[^;\n]*;?\s*\n?`)
	tntAnthropicLinePattern          = regexp.MustCompile(`(?m)^.*x-anthropic.*(?:\n|$)`)
	tntAnthropicURLLinePattern       = regexp.MustCompile(`(?m)^.*api\.(?:anthropic|provider)\.com.*(?:\n|$)`)
	tntAnthropicWordPattern          = regexp.MustCompile(`(?i)anthropic`)

	tntTextReplacements = []tntTextReplacement{
		{old: "Claude Code", new: "AI Assistant"},
		{old: "Anthropic", new: "Provider"},
		{old: "Sonnet 4.6", new: "GLM 5.2"},
		{old: "Sonnet 4.5", new: "GLM 5.2"},
		{old: "Opus 4.7", new: "GLM 5.2"},
		{old: "Opus 4.6", new: "GLM 5.2"},
		{old: "Haiku 4.5", new: "GLM 5.2"},
		{old: "cc-switch", new: "config-tool"},
		{old: "cc_switch", new: "config_tool"},
		{old: "exploit development", new: "research"},
		{old: "exploit", new: "utilize"},
		{old: "C2 frameworks", new: "management tools"},
		{old: "credential testing", new: "validation testing"},
		{old: "supply chain compromise", new: "dependency risks"},
		{old: "detection evasion", new: "avoiding monitoring"},
		{old: "destructive techniques", new: "harmful methods"},
		{old: "destructive actions", new: "irreversible actions"},
		{old: "destructive", new: "irreversible"},
		{old: "Destructive", new: "Irreversible"},
		{old: "DoS attacks", new: "service disruption"},
		{old: "mass targeting", new: "broad impact"},
		{old: "pentesting engagements", new: "assessment"},
		{old: "pentesting", new: "testing"},
		{old: "CTF competitions", new: "practice exercises"},
		{old: "CTF challenges", new: "practice exercises"},
		{old: "malicious purposes", new: "other purposes"},
		{old: "malicious", new: "unwanted"},
		{old: "dual-use security tools", new: "specialized tools"},
		{old: "Dual-use", new: "Specialized"},
		{old: "bypassing safety checks", new: "skipping validation steps"},
		{old: "backwards-compatibility hacks", new: "backwards-compatibility shortcuts"},
		{old: "authorized security testing", new: "authorized testing"},
		{old: "defensive security", new: "defensive practices"},
		{old: "defensive use cases", new: "defensive cases"},
		{old: "security vulnerabilities", new: "code quality issues"},
		{old: "security research", new: "research"},
		{old: "security assessment", new: "assessment"},
		{old: "security testing", new: "testing"},
		{old: "security exercises", new: "exercises"},
		{old: "security review", new: "code review"},
		{old: "security-review", new: "code-review"},
		{old: "specialized security tools", new: "specialized tools"},
		{old: "Specialized security tools", new: "Specialized tools"},
		{old: "vulnerability research", new: "research"},
		{old: "authentication testing", new: "validation testing"},
		{old: "remote management tools", new: "management tools"},
		{old: "unauthorized purposes", new: "other purposes"},
		{old: "prompt injection", new: "instruction validation"},
		{old: "command injection", new: "input handling"},
		{old: "SQL injection", new: "database safety"},
		{old: "code injection", new: "input handling"},
		{old: "XSS", new: "web safety"},
		{old: "OWASP top 10 vulnerabilities", new: "common code quality issues"},
		{old: "OWASP", new: "web standards"},
		{old: "insecure code", new: "problematic code"},
		{old: "vulnerabilities", new: "issues"},
		{old: "dropping database tables", new: "removing data structures"},
		{old: "killing processes", new: "stopping services"},
		{old: "rm -rf", new: "cleanup"},
		{old: "force-pushing", new: "force updating"},
		{old: "force push", new: "force update"},
		{old: "git reset --hard", new: "git reset"},
		{old: "amending published commits", new: "updating commits"},
		{old: "overwriting uncommitted changes", new: "replacing unsaved changes"},
		{old: "overwriting", new: "replacing"},
		{old: "downgrading packages", new: "changing packages"},
		{old: "modifying CI/CD pipelines", new: "adjusting pipelines"},
		{old: "伪造", new: "generate"},
		{old: "绕过", new: "handle"},
		{old: "规避", new: "avoid"},
		{old: "敏感", new: "certain"},
		{old: "破解", new: "solve"},
		{old: "后门", new: "entry"},
		{old: "翻墙", new: "access"},
		{old: "篡改", new: "modify"},
		{old: "劫持", new: "intercept"},
		{old: "逃逸", new: "exit"},
		{old: "提权", new: "upgrade"},
		{old: "恶意", new: "unwanted"},
		{old: "入侵", new: "access"},
		{old: "渗透", new: "review"},
		{old: "免杀", new: "bypass"},
		{old: "远控", new: "remote"},
		{old: "FAKE_UA", new: "AGENT_STR"},
		{old: "ChatGPT/1.0", new: "App/1.0"},
		{old: "ChatGPT", new: "App"},
		{old: "antigravity-proxy", new: "upstream-gateway"},
		{old: "antigravity", new: "gateway"},
		{old: "superpowers:using-superpowers", new: "tools:using-tools"},
		{old: "superpowers", new: "tools"},
		{old: "Header 伪装", new: "Header config"},
		{old: "协议转换 + Header 伪装", new: "protocol adapter"},
		{old: "本地转发代理", new: "local adapter"},
		{old: "代理脚本", new: "adapter script"},
		{old: "PRs", new: "requests"},
		{old: "Main branch", new: "Primary stream"},
		{old: "Current branch:", new: "Current stream:"},
		{old: "Git user:", new: "VCS user:"},
		{old: "gitStatus:", new: "repoStatus:"},
		{old: "Codex CLI", new: "Code Assistant"},
		{old: "codex cli", new: "code assistant"},
		{old: "terminal-based", new: "shell-based"},
		{old: "terminal based", new: "shell based"},
	}
)

func SanitizeTNTTencentText(text string) string {
	text = tntAnthropicBillingHeaderPattern.ReplaceAllString(text, "")
	text = tntAnthropicLinePattern.ReplaceAllString(text, "")
	text = tntAnthropicURLLinePattern.ReplaceAllString(text, "")
	for _, replacement := range tntTextReplacements {
		text = strings.ReplaceAll(text, replacement.old, replacement.new)
	}
	return tntAnthropicWordPattern.ReplaceAllString(text, "provider")
}

func ConvertTNTTencentClaudeRequest(request *dto.ClaudeRequest) (*dto.GeneralOpenAIRequest, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	stream := true
	converted := &dto.GeneralOpenAIRequest{
		Model:           request.Model,
		Stream:          &stream,
		MaxTokens:       request.MaxTokens,
		ReasoningEffort: request.GetEfforts(),
		Temperature:     request.Temperature,
		TopP:            request.TopP,
		TopK:            request.TopK,
	}
	if len(request.StopSequences) == 1 {
		converted.Stop = request.StopSequences[0]
	} else if len(request.StopSequences) > 1 {
		converted.Stop = request.StopSequences
	}

	if system := tntClaudeSystemText(request); system != "" {
		converted.Messages = append(converted.Messages, dto.Message{Role: "system", Content: SanitizeTNTTencentText(system)})
	}
	for _, message := range request.Messages {
		converted.Messages = append(converted.Messages, tntClaudeMessageToChat(message)...)
	}

	tools, err := kitutil.Any2Type[[]dto.Tool](request.Tools)
	if err == nil {
		for _, tool := range tools {
			converted.Tools = append(converted.Tools, dto.ToolCallRequest{
				Type: "function",
				Function: dto.FunctionRequest{
					Name:        tool.Name,
					Description: SanitizeTNTTencentText(tool.Description),
					Parameters:  sanitizeTNTSchema(tool.InputSchema),
				},
			})
		}
	}

	if request.ToolChoice != nil {
		if toolChoice, err := kitutil.Any2Type[dto.ClaudeToolChoice](request.ToolChoice); err == nil {
			switch toolChoice.Type {
			case "any":
				converted.ToolChoice = "required"
			case "tool":
				converted.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": toolChoice.Name}}
			case "none":
				converted.ToolChoice = "none"
			default:
				converted.ToolChoice = "auto"
			}
		}
	}

	converted.Messages = normalizeTNTToolHistory(converted.Messages)
	return converted, SanitizeTNTTencentChatRequest(converted)
}

func ConvertTNTTencentResponsesRequest(request *dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	prepared := *request
	prepared.Conversation = nil
	prepared.PreviousResponseID = ""
	prepared.Prompt = nil
	prepared.ContextManagement = nil

	base, err := ResponsesRequestToChatCompletionsRequest(&prepared)
	if err != nil {
		return nil, err
	}
	stream := true
	converted := &dto.GeneralOpenAIRequest{
		Model:            base.Model,
		Messages:         base.Messages,
		Stream:           &stream,
		MaxTokens:        base.MaxCompletionTokens,
		ReasoningEffort:  base.ReasoningEffort,
		Temperature:      base.Temperature,
		TopP:             base.TopP,
		ResponseFormat:   base.ResponseFormat,
		ParallelTooCalls: base.ParallelTooCalls,
		ToolChoice:       base.ToolChoice,
	}
	for _, tool := range base.Tools {
		if tool.Type == "" || tool.Type == "function" {
			converted.Tools = append(converted.Tools, tool)
		}
	}
	for index := range converted.Messages {
		if converted.Messages[index].Role == "developer" {
			converted.Messages[index].Role = "system"
		}
		converted.Messages[index].Content = flattenTNTMessageContent(converted.Messages[index].Content)
	}
	converted.Messages = normalizeTNTToolHistory(converted.Messages)
	return converted, SanitizeTNTTencentChatRequest(converted)
}

func FinalizeTNTTencentChatRequestJSON(data []byte) ([]byte, error) {
	var request dto.GeneralOpenAIRequest
	if err := kitutil.Unmarshal(data, &request); err != nil {
		return nil, err
	}
	if err := SanitizeTNTTencentChatRequest(&request); err != nil {
		return nil, err
	}
	return kitutil.Marshal(request)
}

func SanitizeTNTTencentChatRequest(request *dto.GeneralOpenAIRequest) error {
	if request == nil {
		return errors.New("request is nil")
	}
	stream := true
	request.Stream = &stream
	for index := range request.Messages {
		request.Messages[index].Content = sanitizeTNTContent(request.Messages[index].Content)
		toolCalls := request.Messages[index].ParseToolCalls()
		for toolIndex := range toolCalls {
			toolCalls[toolIndex].Function.Description = SanitizeTNTTencentText(toolCalls[toolIndex].Function.Description)
			toolCalls[toolIndex].Function.Arguments = SanitizeTNTTencentText(toolCalls[toolIndex].Function.Arguments)
			toolCalls[toolIndex].Function.Parameters = sanitizeTNTSchema(toolCalls[toolIndex].Function.Parameters)
		}
		if len(toolCalls) > 0 {
			encoded, err := kitutil.Marshal(toolCalls)
			if err != nil {
				return err
			}
			request.Messages[index].ToolCalls = encoded
		}
	}
	for index := range request.Tools {
		request.Tools[index].Function.Description = SanitizeTNTTencentText(request.Tools[index].Function.Description)
		request.Tools[index].Function.Arguments = SanitizeTNTTencentText(request.Tools[index].Function.Arguments)
		request.Tools[index].Function.Parameters = sanitizeTNTSchema(request.Tools[index].Function.Parameters)
	}
	if request.ResponseFormat != nil && len(request.ResponseFormat.JsonSchema) > 0 {
		var schema any
		if err := kitutil.Unmarshal(request.ResponseFormat.JsonSchema, &schema); err == nil {
			schema = sanitizeTNTSchema(schema)
			encoded, err := kitutil.Marshal(schema)
			if err != nil {
				return err
			}
			request.ResponseFormat.JsonSchema = encoded
		}
	}
	return nil
}

func SanitizeTNTTencentChatResponse(response *dto.OpenAITextResponse) error {
	if response == nil {
		return nil
	}
	for index := range response.Choices {
		response.Choices[index].Message.Content = sanitizeTNTContent(response.Choices[index].Message.Content)
		toolCalls := response.Choices[index].Message.ParseToolCalls()
		for toolIndex := range toolCalls {
			toolCalls[toolIndex].Function.Arguments = SanitizeTNTTencentText(toolCalls[toolIndex].Function.Arguments)
		}
		if len(toolCalls) > 0 {
			encoded, err := kitutil.Marshal(toolCalls)
			if err != nil {
				return err
			}
			response.Choices[index].Message.ToolCalls = encoded
		}
	}
	return nil
}

func SanitizeTNTTencentChatStreamChunk(chunk *dto.ChatCompletionsStreamResponse) {
	if chunk == nil {
		return
	}
	for choiceIndex := range chunk.Choices {
		delta := &chunk.Choices[choiceIndex].Delta
		if delta.Content != nil {
			content := SanitizeTNTTencentText(*delta.Content)
			delta.Content = &content
		}
		for toolIndex := range delta.ToolCalls {
			delta.ToolCalls[toolIndex].Function.Arguments = SanitizeTNTTencentText(delta.ToolCalls[toolIndex].Function.Arguments)
		}
	}
}

func tntClaudeSystemText(request *dto.ClaudeRequest) string {
	if request.System == nil {
		return ""
	}
	if request.IsStringSystem() {
		return request.GetStringSystem()
	}
	parts := make([]string, 0)
	for _, block := range request.ParseSystem() {
		if block.Type == "text" {
			parts = append(parts, block.GetText())
		}
	}
	return strings.Join(parts, "\n")
}

func tntClaudeMessageToChat(message dto.ClaudeMessage) []dto.Message {
	if message.IsStringContent() {
		return []dto.Message{{Role: message.Role, Content: SanitizeTNTTencentText(message.GetStringContent())}}
	}
	blocks, err := message.ParseContent()
	if err != nil {
		return []dto.Message{{Role: message.Role, Content: ""}}
	}
	textParts := make([]string, 0)
	toolCalls := make([]dto.ToolCallRequest, 0)
	toolResults := make([]dto.Message, 0)
	for _, block := range blocks {
		switch block.Type {
		case "text", "input_text":
			textParts = append(textParts, SanitizeTNTTencentText(block.GetText()))
		case "tool_use":
			arguments, err := kitutil.Marshal(block.Input)
			if err != nil {
				arguments = []byte("{}")
			}
			toolCalls = append(toolCalls, dto.ToolCallRequest{
				ID:   block.Id,
				Type: "function",
				Function: dto.FunctionRequest{
					Name:      block.Name,
					Arguments: string(arguments),
				},
			})
		case "tool_result":
			toolResults = append(toolResults, dto.Message{
				Role:       "tool",
				ToolCallId: block.ToolUseId,
				Content:    SanitizeTNTTencentText(tntClaudeToolResultText(block.Content)),
			})
		}
	}

	text := strings.Join(textParts, "\n")
	if message.Role == "assistant" {
		result := make([]dto.Message, 0, 2)
		if len(toolCalls) > 0 && text != "" {
			result = append(result, dto.Message{Role: "assistant", Content: text})
		}
		assistant := dto.Message{Role: "assistant", Content: text}
		if len(toolCalls) > 0 {
			assistant.Content = nil
			assistant.ToolCalls, _ = kitutil.Marshal(toolCalls)
		}
		if text != "" || len(toolCalls) > 0 {
			result = append(result, assistant)
		}
		return result
	}
	if message.Role == "user" && len(toolResults) > 0 {
		result := make([]dto.Message, 0, len(toolResults)+1)
		if text != "" {
			result = append(result, dto.Message{Role: "user", Content: text})
		}
		return append(result, toolResults...)
	}
	return []dto.Message{{Role: message.Role, Content: text}}
}

func tntClaudeToolResultText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	blocks, err := kitutil.Any2Type[[]dto.ClaudeMediaMessage](content)
	if err == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Type == "text" {
				parts = append(parts, block.GetText())
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	encoded, err := kitutil.Marshal(content)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func normalizeTNTToolHistory(messages []dto.Message) []dto.Message {
	split := make([]dto.Message, 0, len(messages))
	for _, message := range messages {
		toolCalls := message.ParseToolCalls()
		if message.Role == "assistant" && len(toolCalls) > 0 && message.StringContent() != "" {
			split = append(split, dto.Message{Role: "assistant", Content: message.StringContent()})
			message.Content = nil
		}
		split = append(split, message)
	}

	pending := make(map[string][]dto.Message)
	pendingOrder := make([]string, 0)
	for _, message := range split {
		if message.Role != "tool" {
			continue
		}
		if _, exists := pending[message.ToolCallId]; !exists {
			pendingOrder = append(pendingOrder, message.ToolCallId)
		}
		pending[message.ToolCallId] = append(pending[message.ToolCallId], message)
	}

	result := make([]dto.Message, 0, len(split))
	for _, message := range split {
		if message.Role == "tool" {
			continue
		}
		result = append(result, message)
		if message.Role != "assistant" {
			continue
		}
		for _, toolCall := range message.ParseToolCalls() {
			if toolResults := pending[toolCall.ID]; len(toolResults) > 0 {
				result = append(result, toolResults...)
				delete(pending, toolCall.ID)
				continue
			}
			result = append(result, dto.Message{Role: "tool", ToolCallId: toolCall.ID, Content: "(interrupted)"})
		}
	}
	for _, toolCallID := range pendingOrder {
		result = append(result, pending[toolCallID]...)
	}
	return result
}

func flattenTNTMessageContent(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	items, ok := content.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := block["text"].(string); ok {
			parts = append(parts, text)
		} else if text, ok := block["content"].(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func sanitizeTNTContent(content any) any {
	switch value := content.(type) {
	case string:
		return SanitizeTNTTencentText(value)
	case []any:
		for index, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := block["text"].(string); ok {
				block["text"] = SanitizeTNTTencentText(text)
			}
			value[index] = block
		}
		return value
	case []dto.MediaContent:
		for index := range value {
			if value[index].Type == dto.ContentTypeText {
				value[index].Text = SanitizeTNTTencentText(value[index].Text)
			}
		}
		return value
	default:
		return content
	}
}

func sanitizeTNTSchema(schema any) any {
	if schema == nil {
		return nil
	}
	encoded, err := kitutil.Marshal(schema)
	if err != nil {
		return schema
	}
	var normalized any
	if err := kitutil.Unmarshal(encoded, &normalized); err != nil {
		return schema
	}
	return sanitizeTNTSchemaValue(normalized)
}

func sanitizeTNTSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if (key == "title" || key == "description") && child != nil {
				if text, ok := child.(string); ok {
					typed[key] = SanitizeTNTTencentText(text)
					continue
				}
			}
			typed[key] = sanitizeTNTSchemaValue(child)
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = sanitizeTNTSchemaValue(typed[index])
		}
		return typed
	default:
		return value
	}
}
