package relayconvert

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

const (
	glm53MaxTokens    = uint(131072)
	glm53MaxStopCount = 4
)

func NormalizeGLM53ChatRequest(request *dto.GeneralOpenAIRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if err := validateGLM53MaxTokens("max_tokens", request.MaxTokens); err != nil {
		return err
	}
	request.MaxCompletionTokens = nil
	if err := validateGLM53TopP(request.TopP); err != nil {
		return err
	}
	if err := normalizeGLM53Seed(request.Seed); err != nil {
		return err
	}
	request.N = nil
	request.FrequencyPenalty = nil
	request.PresencePenalty = nil
	request.ParallelTooCalls = nil
	request.LogProbs = nil
	request.TopLogProbs = nil
	request.StreamOptions = nil
	if request.TopK != nil && *request.TopK == 0 {
		return fmt.Errorf("top_k must not be zero for GLM-5.3 Chat Completions")
	}
	if err := validateGLM53ChatTools(request); err != nil {
		return err
	}
	disableThinking, err := normalizeGLM53ChatThinking(request)
	if err != nil {
		return err
	}
	if err := normalizeGLM53Effort(&request.ReasoningEffort, disableThinking); err != nil {
		return err
	}
	if request.ResponseFormat != nil && request.ResponseFormat.Type == "" && len(request.ResponseFormat.JsonSchema) == 0 {
		request.ResponseFormat = nil
	}
	stop, err := normalizeGLM53Stop(request.Stop)
	if err != nil {
		return err
	}
	if len(stop) == 0 {
		request.Stop = nil
	} else {
		request.Stop = stop
	}
	return nil
}

func NormalizeGLM53ResponsesRequest(request *dto.OpenAIResponsesRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if err := validateGLM53MaxTokens("max_output_tokens", request.MaxOutputTokens); err != nil {
		return err
	}
	if err := validateGLM53TopP(request.TopP); err != nil {
		return err
	}
	disableThinking, err := glm53RawThinkingDisabled(request.EnableThinking)
	if err != nil {
		return err
	}
	request.EnableThinking = nil
	if request.Reasoning == nil {
		request.Reasoning = &dto.Reasoning{}
	}
	if err := normalizeGLM53Effort(&request.Reasoning.Effort, disableThinking); err != nil {
		return err
	}
	return nil
}

func NormalizeGLM53ClaudeRequest(request *dto.ClaudeRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if err := validateGLM53MaxTokens("max_tokens", request.MaxTokens); err != nil {
		return err
	}
	request.MaxTokensToSample = nil
	if err := validateGLM53TopP(request.TopP); err != nil {
		return err
	}
	if err := validateGLM53Temperature(request.Temperature); err != nil {
		return err
	}
	if err := validateGLM53ClaudeTools(request); err != nil {
		return err
	}
	disableThinking := false
	if request.Thinking != nil {
		switch request.Thinking.Type {
		case "", "enabled", "adaptive":
		case "disabled":
			disableThinking = true
		default:
			return fmt.Errorf("thinking.type must be enabled, adaptive, disabled, or empty for GLM-5.3 Anthropic Messages")
		}
	}
	request.Thinking = &dto.Thinking{Type: "enabled"}

	outputConfig := make(map[string]any)
	if len(request.OutputConfig) > 0 {
		if err := kitutil.Unmarshal(request.OutputConfig, &outputConfig); err != nil {
			return fmt.Errorf("invalid output_config: %w", err)
		}
	}
	if outputConfig == nil {
		outputConfig = make(map[string]any)
	}
	effort := ""
	if value, exists := outputConfig["effort"]; exists && value != nil {
		var ok bool
		effort, ok = value.(string)
		if !ok {
			return fmt.Errorf("reasoning effort must be a string for GLM-5.3")
		}
	}
	if err := normalizeGLM53Effort(&effort, disableThinking); err != nil {
		return err
	}
	outputConfig["effort"] = effort
	encoded, err := kitutil.Marshal(outputConfig)
	if err != nil {
		return err
	}
	request.OutputConfig = encoded
	if len(request.ResponseFormat) > 0 && kitutil.GetJsonType(request.ResponseFormat) == "null" {
		request.ResponseFormat = nil
	} else if len(request.ResponseFormat) > 0 && kitutil.GetJsonType(request.ResponseFormat) == "object" {
		var responseFormat dto.ResponseFormat
		if err := kitutil.Unmarshal(request.ResponseFormat, &responseFormat); err != nil {
			return fmt.Errorf("invalid response_format: %w", err)
		}
		if responseFormat.Type == "" && len(responseFormat.JsonSchema) == 0 {
			request.ResponseFormat = nil
		}
	}

	stop, err := normalizeGLM53Stop(request.StopSequences)
	if err != nil {
		return fmt.Errorf("stop_sequences: %w", err)
	}
	request.StopSequences = stop
	return nil
}

func normalizeGLM53ChatThinking(request *dto.GeneralOpenAIRequest) (bool, error) {
	disableThinking := false
	if len(request.THINKING) > 0 && string(request.THINKING) != "null" {
		if kitutil.GetJsonType(request.THINKING) != "object" {
			return false, fmt.Errorf("thinking must be an object for GLM-5.3 Chat Completions")
		}
		var thinking dto.Thinking
		if err := kitutil.Unmarshal(request.THINKING, &thinking); err != nil {
			return false, fmt.Errorf("invalid thinking: %w", err)
		}
		switch thinking.Type {
		case "", "enabled", "adaptive":
		case "disabled":
			disableThinking = true
		default:
			return false, fmt.Errorf("thinking.type must be enabled, adaptive, disabled, or empty for GLM-5.3")
		}
	}

	enableDisabled, err := glm53RawThinkingDisabled(request.EnableThinking)
	if err != nil {
		return false, err
	}
	request.THINKING = json.RawMessage(`{"type":"enabled"}`)
	request.EnableThinking = nil
	return disableThinking || enableDisabled, nil
}

func glm53RawThinkingDisabled(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	switch kitutil.GetJsonType(raw) {
	case "boolean":
		var enabled bool
		if err := kitutil.Unmarshal(raw, &enabled); err != nil {
			return false, fmt.Errorf("invalid enable_thinking: %w", err)
		}
		return !enabled, nil
	case "number":
		value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || value == 0 {
			return false, fmt.Errorf("enable_thinking numeric value must be a non-zero integer for GLM-5.3")
		}
		return false, nil
	case "string":
		var value string
		if err := kitutil.Unmarshal(raw, &value); err != nil {
			return false, fmt.Errorf("invalid enable_thinking: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "null", "true":
			return false, nil
		default:
			return false, fmt.Errorf("enable_thinking string value is invalid for GLM-5.3")
		}
	default:
		return false, fmt.Errorf("enable_thinking value is invalid for GLM-5.3")
	}
}

func normalizeGLM53Effort(effort *string, disableThinking bool) error {
	if effort == nil {
		return nil
	}
	if *effort == "" {
		if disableThinking {
			*effort = "low"
		} else {
			*effort = "max"
		}
	}
	switch *effort {
	case "low", "high", "max":
	default:
		return fmt.Errorf("reasoning effort must be low, high, or max for GLM-5.3")
	}
	if disableThinking {
		*effort = "low"
	}
	return nil
}

func NormalizeGLM53RequestJSON(data []byte, format types.RelayFormat) ([]byte, error) {
	var err error
	data, err = normalizeGLM53IngressJSON(data, format)
	if err != nil {
		return nil, err
	}
	switch format {
	case types.RelayFormatOpenAI:
		var request dto.GeneralOpenAIRequest
		if err := kitutil.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		if err := NormalizeGLM53ChatRequest(&request); err != nil {
			return nil, err
		}
		return kitutil.Marshal(request)
	case types.RelayFormatOpenAIResponses:
		var request dto.OpenAIResponsesRequest
		if err := kitutil.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		if err := NormalizeGLM53ResponsesRequest(&request); err != nil {
			return nil, err
		}
		return kitutil.Marshal(request)
	case types.RelayFormatClaude:
		var request dto.ClaudeRequest
		if err := kitutil.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		if err := NormalizeGLM53ClaudeRequest(&request); err != nil {
			return nil, err
		}
		return kitutil.Marshal(request)
	default:
		return data, nil
	}
}

func validateGLM53MaxTokens(name string, value *uint) error {
	if value == nil {
		return nil
	}
	if *value == 0 || *value > glm53MaxTokens {
		return fmt.Errorf("%s must be between 1 and %d for GLM-5.3", name, glm53MaxTokens)
	}
	return nil
}

func validateGLM53TopP(value *float64) error {
	if value != nil && (*value < 0 || *value > 1) {
		return fmt.Errorf("top_p must be between 0 and 1 for GLM-5.3")
	}
	return nil
}

func validateGLM53Temperature(value *float64) error {
	if value != nil && (*value < 0 || *value > 1) {
		return fmt.Errorf("temperature must be between 0 and 1 for GLM-5.3 Anthropic Messages")
	}
	return nil
}

func normalizeGLM53Stop(stop any) ([]string, error) {
	if stop == nil {
		return nil, nil
	}
	if _, ok := stop.(string); ok {
		return nil, fmt.Errorf("stop must be an array of strings for GLM-5.3")
	}
	values, err := kitutil.Any2Type[[]any](stop)
	if err != nil {
		return nil, fmt.Errorf("stop must be an array for GLM-5.3")
	}
	sequences := make([]string, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			sequences = append(sequences, typed)
		case float64:
			sequences = append(sequences, strconv.FormatFloat(typed, 'f', -1, 64))
		case bool:
			sequences = append(sequences, strconv.FormatBool(typed))
		default:
			return nil, fmt.Errorf("stop array items must be strings, numbers, or booleans for GLM-5.3")
		}
	}
	if len(sequences) == 0 {
		return nil, nil
	}
	if len(sequences) > glm53MaxStopCount {
		return nil, fmt.Errorf("stop accepts at most %d strings for GLM-5.3", glm53MaxStopCount)
	}
	return sequences, nil
}

func normalizeGLM53IngressJSON(data []byte, format types.RelayFormat) ([]byte, error) {
	if format != types.RelayFormatOpenAI && format != types.RelayFormatClaude {
		return data, nil
	}
	fields := make(map[string]json.RawMessage)
	if err := kitutil.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return data, nil
	}

	var err error
	switch format {
	case types.RelayFormatOpenAI:
		delete(fields, "max_completion_tokens")
		delete(fields, "n")
		delete(fields, "frequency_penalty")
		delete(fields, "presence_penalty")
		delete(fields, "parallel_tool_calls")
		delete(fields, "logprobs")
		delete(fields, "top_logprobs")
		delete(fields, "stream_options")
		if err = normalizeGLM53RawStreamField(fields, true); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawIntegerField(fields, "max_tokens", true, false, true, true); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawIntegerField(fields, "top_k", false, false, true, true); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawFloatField(fields, "top_p", false, true); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawFloatField(fields, "temperature", false, true); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawSeedField(fields); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawStopField(fields, "stop", true); err != nil {
			return nil, err
		}
		if err = validateGLM53RawChatTools(fields); err != nil {
			return nil, err
		}
	case types.RelayFormatClaude:
		delete(fields, "max_tokens_to_sample")
		if err = normalizeGLM53RawStreamField(fields, false); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawIntegerField(fields, "max_tokens", true, true, false, false); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawIntegerField(fields, "top_k", false, true, false, false); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawFloatField(fields, "top_p", true, false); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawFloatField(fields, "temperature", true, false); err != nil {
			return nil, err
		}
		if err = normalizeGLM53RawStopField(fields, "stop_sequences", false); err != nil {
			return nil, err
		}
	}
	return kitutil.Marshal(fields)
}

func normalizeGLM53RawIntegerField(fields map[string]json.RawMessage, name string, unsigned bool, allowBoolean bool, allowFraction bool, allowEmptyString bool) error {
	raw, exists := fields[name]
	if !exists || kitutil.GetJsonType(raw) == "null" {
		return nil
	}
	var value int64
	switch kitutil.GetJsonType(raw) {
	case "number":
		text := strings.TrimSpace(string(raw))
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err == nil {
			value = parsed
			break
		}
		floatValue, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(floatValue, 0) || math.IsNaN(floatValue) || (!allowFraction && math.Trunc(floatValue) != floatValue) || floatValue > math.MaxInt64 || floatValue < math.MinInt64 {
			return fmt.Errorf("%s must be an integer for GLM-5.3", name)
		}
		value = int64(math.Trunc(floatValue))
	case "string":
		var text string
		if err := kitutil.Unmarshal(raw, &text); err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
		text = strings.TrimSpace(text)
		if allowEmptyString && (text == "" || text == "null") {
			delete(fields, name)
			return nil
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return fmt.Errorf("%s must be an integer for GLM-5.3", name)
		}
		value = parsed
	case "boolean":
		if !allowBoolean {
			return fmt.Errorf("%s must be numeric for GLM-5.3", name)
		}
		var boolean bool
		if err := kitutil.Unmarshal(raw, &boolean); err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
		if boolean {
			value = 1
		}
	default:
		return fmt.Errorf("%s must be numeric for GLM-5.3", name)
	}
	if unsigned && value < 0 {
		return fmt.Errorf("%s must not be negative for GLM-5.3", name)
	}
	fields[name] = json.RawMessage(strconv.FormatInt(value, 10))
	return nil
}

func normalizeGLM53RawFloatField(fields map[string]json.RawMessage, name string, allowBoolean bool, allowEmptyString bool) error {
	raw, exists := fields[name]
	if !exists || kitutil.GetJsonType(raw) == "null" {
		return nil
	}
	switch kitutil.GetJsonType(raw) {
	case "number":
		return nil
	case "string":
		var text string
		if err := kitutil.Unmarshal(raw, &text); err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
		text = strings.TrimSpace(text)
		if allowEmptyString && (text == "" || text == "null") {
			delete(fields, name)
			return nil
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return fmt.Errorf("%s must be numeric for GLM-5.3", name)
		}
		fields[name] = json.RawMessage(strconv.FormatFloat(value, 'g', -1, 64))
		return nil
	case "boolean":
		if !allowBoolean {
			return fmt.Errorf("%s must be numeric for GLM-5.3", name)
		}
		var value bool
		if err := kitutil.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
		if value {
			fields[name] = json.RawMessage("1")
		} else {
			fields[name] = json.RawMessage("0")
		}
		return nil
	default:
		return fmt.Errorf("%s must be numeric for GLM-5.3", name)
	}
}

func normalizeGLM53RawSeedField(fields map[string]json.RawMessage) error {
	raw, exists := fields["seed"]
	if !exists || kitutil.GetJsonType(raw) == "null" {
		return nil
	}
	var value float64
	switch kitutil.GetJsonType(raw) {
	case "number":
		parsed, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return fmt.Errorf("seed must be numeric for GLM-5.3")
		}
		value = parsed
	case "string":
		var text string
		if err := kitutil.Unmarshal(raw, &text); err != nil {
			return fmt.Errorf("invalid seed: %w", err)
		}
		text = strings.TrimSpace(text)
		if text == "" || text == "null" {
			delete(fields, "seed")
			return nil
		}
		parsed, err := strconv.ParseInt(text, 10, 32)
		if err != nil {
			return fmt.Errorf("seed must be a 32-bit integer for GLM-5.3")
		}
		value = float64(parsed)
	default:
		return fmt.Errorf("seed must be numeric for GLM-5.3")
	}
	if value < math.MinInt32 || value > math.MaxInt32 {
		return fmt.Errorf("seed must be between %d and %d for GLM-5.3", math.MinInt32, math.MaxInt32)
	}
	fields["seed"] = json.RawMessage(strconv.FormatFloat(math.Trunc(value), 'f', 0, 64))
	return nil
}

func normalizeGLM53RawStreamField(fields map[string]json.RawMessage, chat bool) error {
	raw, exists := fields["stream"]
	if !exists || kitutil.GetJsonType(raw) == "null" || kitutil.GetJsonType(raw) == "boolean" {
		return nil
	}
	var stream bool
	switch kitutil.GetJsonType(raw) {
	case "number":
		value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			return fmt.Errorf("stream numeric value must be an integer for GLM-5.3")
		}
		if !chat && value != 0 && value != 1 {
			return fmt.Errorf("stream numeric value must be 0 or 1 for GLM-5.3 Anthropic Messages")
		}
		stream = value != 0
	case "string":
		var value string
		if err := kitutil.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("invalid stream: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true":
			stream = true
		case "false":
			stream = false
		case "1":
			if chat {
				return fmt.Errorf("stream string value is invalid for GLM-5.3 Chat Completions")
			}
			stream = true
		case "0":
			if chat {
				return fmt.Errorf("stream string value is invalid for GLM-5.3 Chat Completions")
			}
			stream = false
		default:
			return fmt.Errorf("stream string value is invalid for GLM-5.3")
		}
	default:
		return fmt.Errorf("stream value is invalid for GLM-5.3")
	}
	encoded, err := kitutil.Marshal(stream)
	if err != nil {
		return err
	}
	fields["stream"] = encoded
	return nil
}

func normalizeGLM53Seed(value *float64) error {
	if value == nil {
		return nil
	}
	if *value < math.MinInt32 || *value > math.MaxInt32 {
		return fmt.Errorf("seed must be between %d and %d for GLM-5.3", math.MinInt32, math.MaxInt32)
	}
	*value = math.Trunc(*value)
	return nil
}

func normalizeGLM53RawStopField(fields map[string]json.RawMessage, name string, allowPrimitives bool) error {
	raw, exists := fields[name]
	if !exists || kitutil.GetJsonType(raw) == "null" {
		return nil
	}
	if kitutil.GetJsonType(raw) != "array" {
		return nil
	}
	var items []json.RawMessage
	if err := kitutil.Unmarshal(raw, &items); err != nil {
		return err
	}
	if len(items) > glm53MaxStopCount {
		return fmt.Errorf("%s accepts at most %d items for GLM-5.3", name, glm53MaxStopCount)
	}
	converted := make([]string, 0, len(items))
	for _, item := range items {
		switch kitutil.GetJsonType(item) {
		case "string":
			var value string
			if err := kitutil.Unmarshal(item, &value); err != nil {
				return err
			}
			converted = append(converted, value)
		case "number", "boolean":
			if !allowPrimitives {
				return fmt.Errorf("%s array items must be strings for GLM-5.3", name)
			}
			converted = append(converted, strings.TrimSpace(string(item)))
		default:
			if allowPrimitives {
				return fmt.Errorf("%s array items must be strings, numbers, or booleans for GLM-5.3", name)
			}
			return fmt.Errorf("%s array items must be strings for GLM-5.3", name)
		}
	}
	encoded, err := kitutil.Marshal(converted)
	if err != nil {
		return err
	}
	fields[name] = encoded
	return nil
}

func validateGLM53ChatTools(request *dto.GeneralOpenAIRequest) error {
	for index, tool := range request.Tools {
		if tool.Type == "" {
			return fmt.Errorf("tools[%d].type must not be empty for GLM-5.3 Chat Completions", index)
		}
		if isObservedInvalidGLM53ChatToolType(tool.Type) {
			return fmt.Errorf("tools[%d].type is invalid for GLM-5.3 Chat Completions", index)
		}
	}
	if request.ToolChoice == nil {
		return nil
	}
	switch value := request.ToolChoice.(type) {
	case string:
		if value == "" || value == "any" || value == "invalid" || value == "AUTO" {
			return fmt.Errorf("tool_choice is invalid for GLM-5.3 Chat Completions")
		}
		return nil
	case map[string]any:
		typeValue, exists := value["type"]
		if !exists {
			return fmt.Errorf("tool_choice.type is required for GLM-5.3 Chat Completions")
		}
		choiceType, ok := typeValue.(string)
		if !ok || choiceType == "" {
			return fmt.Errorf("tool_choice.type is invalid for GLM-5.3 Chat Completions")
		}
		if choiceType == "function" {
			function, ok := value["function"].(map[string]any)
			if !ok {
				return fmt.Errorf("tool_choice.function is required for GLM-5.3 Chat Completions")
			}
			name, ok := function["name"].(string)
			if !ok || name == "" {
				return fmt.Errorf("tool_choice.function.name is required for GLM-5.3 Chat Completions")
			}
			return nil
		}
		if isObservedInvalidGLM53ChatToolChoiceObjectType(choiceType) {
			return fmt.Errorf("tool_choice.type is invalid for GLM-5.3 Chat Completions")
		}
		return nil
	}
	return fmt.Errorf("tool_choice is invalid for GLM-5.3 Chat Completions")
}

func validateGLM53RawChatTools(fields map[string]json.RawMessage) error {
	if raw, exists := fields["tools"]; exists && kitutil.GetJsonType(raw) != "null" && kitutil.GetJsonType(raw) == "array" {
		var tools []json.RawMessage
		if err := kitutil.Unmarshal(raw, &tools); err != nil {
			return err
		}
		for index, toolRaw := range tools {
			if kitutil.GetJsonType(toolRaw) != "object" {
				return fmt.Errorf("tools[%d] must be an object for GLM-5.3 Chat Completions", index)
			}
			tool := make(map[string]json.RawMessage)
			if err := kitutil.Unmarshal(toolRaw, &tool); err != nil {
				return err
			}
			typeRaw, exists := tool["type"]
			if !exists || kitutil.GetJsonType(typeRaw) != "string" {
				return fmt.Errorf("tools[%d].type is required for GLM-5.3 Chat Completions", index)
			}
			var toolType string
			if err := kitutil.Unmarshal(typeRaw, &toolType); err != nil {
				return err
			}
			if toolType == "" || isObservedInvalidGLM53ChatToolType(toolType) {
				return fmt.Errorf("tools[%d].type is invalid for GLM-5.3 Chat Completions", index)
			}
			if toolType == "web_search" {
				if _, exists := tool["web_search"]; !exists {
					return fmt.Errorf("tools[%d].web_search is required for GLM-5.3 Chat Completions", index)
				}
			}
			if toolType != "function" {
				continue
			}
			functionRaw, exists := tool["function"]
			if !exists || kitutil.GetJsonType(functionRaw) != "object" {
				return fmt.Errorf("tools[%d].function is required for GLM-5.3 Chat Completions", index)
			}
			function := make(map[string]json.RawMessage)
			if err := kitutil.Unmarshal(functionRaw, &function); err != nil {
				return err
			}
			nameRaw, exists := function["name"]
			if !exists || kitutil.GetJsonType(nameRaw) != "string" {
				return fmt.Errorf("tools[%d].function.name is required for GLM-5.3 Chat Completions", index)
			}
			if parametersRaw, exists := function["parameters"]; exists {
				jsonType := kitutil.GetJsonType(parametersRaw)
				if jsonType != "null" && jsonType != "object" {
					return fmt.Errorf("tools[%d].function.parameters must be an object for GLM-5.3 Chat Completions", index)
				}
			}
			if descriptionRaw, exists := function["description"]; exists {
				jsonType := kitutil.GetJsonType(descriptionRaw)
				if jsonType != "null" && jsonType != "string" {
					return fmt.Errorf("tools[%d].function.description must be a string for GLM-5.3 Chat Completions", index)
				}
			}
		}
	}
	return validateGLM53RawChatToolChoice(fields)
}

func validateGLM53RawChatToolChoice(fields map[string]json.RawMessage) error {
	raw, exists := fields["tool_choice"]
	if !exists || kitutil.GetJsonType(raw) == "null" {
		return nil
	}
	switch kitutil.GetJsonType(raw) {
	case "string":
		var value string
		if err := kitutil.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value == "" || value == "any" || value == "invalid" || value == "AUTO" {
			return fmt.Errorf("tool_choice is invalid for GLM-5.3 Chat Completions")
		}
		return nil
	case "object":
		value := make(map[string]json.RawMessage)
		if err := kitutil.Unmarshal(raw, &value); err != nil {
			return err
		}
		typeRaw, exists := value["type"]
		if !exists || kitutil.GetJsonType(typeRaw) != "string" {
			return fmt.Errorf("tool_choice.type is required for GLM-5.3 Chat Completions")
		}
		var choiceType string
		if err := kitutil.Unmarshal(typeRaw, &choiceType); err != nil {
			return err
		}
		if choiceType == "" || isObservedInvalidGLM53ChatToolChoiceObjectType(choiceType) {
			return fmt.Errorf("tool_choice.type is invalid for GLM-5.3 Chat Completions")
		}
		if choiceType != "function" {
			return nil
		}
		functionRaw, exists := value["function"]
		if !exists || kitutil.GetJsonType(functionRaw) != "object" {
			return fmt.Errorf("tool_choice.function is required for GLM-5.3 Chat Completions")
		}
		function := make(map[string]json.RawMessage)
		if err := kitutil.Unmarshal(functionRaw, &function); err != nil {
			return err
		}
		nameRaw, exists := function["name"]
		if !exists || kitutil.GetJsonType(nameRaw) != "string" {
			return fmt.Errorf("tool_choice.function.name is required for GLM-5.3 Chat Completions")
		}
		var name string
		if err := kitutil.Unmarshal(nameRaw, &name); err != nil {
			return err
		}
		if name == "" {
			return fmt.Errorf("tool_choice.function.name is required for GLM-5.3 Chat Completions")
		}
		return nil
	default:
		return fmt.Errorf("tool_choice is invalid for GLM-5.3 Chat Completions")
	}
}

func isObservedInvalidGLM53ChatToolType(value string) bool {
	switch value {
	case "unknown", "web_search_preview", "custom", "computer", "use":
		return true
	default:
		return false
	}
}

func isObservedInvalidGLM53ChatToolChoiceObjectType(value string) bool {
	switch value {
	case "unknown", "code_interpreter", "web_search", "auto", "none", "required", "custom":
		return true
	default:
		return false
	}
}

func validateGLM53ClaudeTools(request *dto.ClaudeRequest) error {
	if request.Tools != nil {
		tools, err := kitutil.Any2Type[[]map[string]any](request.Tools)
		if err != nil {
			return fmt.Errorf("tools must be an array for GLM-5.3 Anthropic Messages")
		}
		for index, tool := range tools {
			name, exists := tool["name"]
			if !exists {
				return fmt.Errorf("tools[%d].name is required for GLM-5.3 Anthropic Messages", index)
			}
			if _, ok := name.(string); !ok {
				return fmt.Errorf("tools[%d].name must be a string for GLM-5.3 Anthropic Messages", index)
			}
			if schema, exists := tool["input_schema"]; exists {
				if _, ok := schema.(map[string]any); !ok {
					return fmt.Errorf("tools[%d].input_schema must be an object for GLM-5.3 Anthropic Messages", index)
				}
			}
			if description, exists := tool["description"]; exists && description != nil {
				if _, ok := description.(string); !ok {
					return fmt.Errorf("tools[%d].description must be a string for GLM-5.3 Anthropic Messages", index)
				}
			}
		}
	}
	if request.ToolChoice != nil {
		if _, ok := request.ToolChoice.(map[string]any); !ok {
			return fmt.Errorf("tool_choice must be an object for GLM-5.3 Anthropic Messages")
		}
	}
	if len(request.Metadata) > 0 {
		jsonType := kitutil.GetJsonType(request.Metadata)
		if jsonType != "null" && jsonType != "object" {
			return fmt.Errorf("metadata must be an object for GLM-5.3 Anthropic Messages")
		}
	}
	if len(request.McpServers) > 0 {
		jsonType := kitutil.GetJsonType(request.McpServers)
		if jsonType != "null" && jsonType != "array" {
			return fmt.Errorf("mcp_servers must be an array for GLM-5.3 Anthropic Messages")
		}
		if jsonType == "array" {
			servers, err := kitutil.Any2Type[[]map[string]any](request.McpServers)
			if err != nil {
				return fmt.Errorf("mcp_servers items must be objects for GLM-5.3 Anthropic Messages")
			}
			for index, server := range servers {
				typeValue, typeOK := server["type"].(string)
				name, nameOK := server["name"].(string)
				url, urlOK := server["url"].(string)
				if !typeOK || typeValue != "url" {
					return fmt.Errorf("mcp_servers[%d].type must be url for GLM-5.3 Anthropic Messages", index)
				}
				if !nameOK || strings.TrimSpace(name) == "" {
					return fmt.Errorf("mcp_servers[%d].name must not be empty for GLM-5.3 Anthropic Messages", index)
				}
				if !urlOK || strings.TrimSpace(url) == "" {
					return fmt.Errorf("mcp_servers[%d].url must not be empty for GLM-5.3 Anthropic Messages", index)
				}
			}
		}
	}
	return nil
}
