package relayconvert

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

const (
	kimiK3DefaultMaxTokens = uint(131072)
	kimiK3MaxTokens        = uint(1048576)
	kimiK3MaxStopCount     = 5
	kimiK3MaxStopBytes     = 32
)

func NormalizeKimiK3ChatRequest(request *dto.GeneralOpenAIRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if request.Model != "kimi-k3" {
		return fmt.Errorf("Kimi K3 compatibility requires the mapped model kimi-k3")
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return fmt.Errorf("max_tokens and max_completion_tokens cannot both be set")
	}
	if request.MaxTokens == nil && request.MaxCompletionTokens == nil {
		request.MaxTokens = common.GetPointer(kimiK3DefaultMaxTokens)
	}
	if request.GetMaxTokens() == 0 || request.GetMaxTokens() > kimiK3MaxTokens {
		return fmt.Errorf("max_tokens must be between 1 and %d for kimi-k3", kimiK3MaxTokens)
	}
	hideThinking := KimiK3RequestDisablesThinking(request)
	applyKimiK3ReasoningEffortDefault(&request.ReasoningEffort)
	if err := validateKimiK3ThinkingSwitches(request.EnableThinking, request.ChatTemplateKwargs, request.THINKING); err != nil {
		return err
	}
	if hideThinking {
		request.ReasoningEffort = "low"
		request.EnableThinking = nil
		request.THINKING = nil
		request.Temperature = nil
		if len(request.ChatTemplateKwargs) > 0 && common.GetJsonType(request.ChatTemplateKwargs) == "object" {
			var kwargs map[string]any
			if err := common.Unmarshal(request.ChatTemplateKwargs, &kwargs); err != nil {
				return fmt.Errorf("invalid chat_template_kwargs: %w", err)
			}
			delete(kwargs, "enable_thinking")
			if len(kwargs) == 0 {
				request.ChatTemplateKwargs = nil
			} else {
				encoded, err := common.Marshal(kwargs)
				if err != nil {
					return err
				}
				request.ChatTemplateKwargs = encoded
			}
		}
	}
	if err := normalizeKimiK3ChatSampling(&request.Temperature, &request.TopP, &request.N, &request.PresencePenalty, &request.FrequencyPenalty); err != nil {
		return err
	}
	if request.TopK != nil {
		return fmt.Errorf("top_k is not supported by kimi-k3")
	}
	if err := validateKimiK3Stop(request.Stop); err != nil {
		return err
	}
	stopSequences := kimiK3StopSequences(request.Stop)
	if len(stopSequences) == 0 {
		request.Stop = nil
	} else {
		request.Stop = stopSequences[0]
	}
	if err := validateKimiK3ToolChoice(request.ToolChoice); err != nil {
		return err
	}
	if err := validateKimiK3ResponseFormat(request.ResponseFormat); err != nil {
		return err
	}
	return validateKimiK3ChatMedia(request.Messages)
}

func NormalizeKimiK3ResponsesRequest(request *dto.OpenAIResponsesRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if request.Model != "kimi-k3" {
		return fmt.Errorf("Kimi K3 compatibility requires the mapped model kimi-k3")
	}
	if request.MaxOutputTokens == nil {
		request.MaxOutputTokens = common.GetPointer(kimiK3DefaultMaxTokens)
	}
	if *request.MaxOutputTokens == 0 || *request.MaxOutputTokens > kimiK3MaxTokens {
		return fmt.Errorf("max_output_tokens must be between 1 and %d for kimi-k3", kimiK3MaxTokens)
	}
	hideThinking := KimiK3RequestDisablesThinking(request)
	if request.Reasoning == nil {
		request.Reasoning = &dto.Reasoning{Effort: "max"}
	} else {
		applyKimiK3ReasoningEffortDefault(&request.Reasoning.Effort)
	}
	if err := validateKimiK3ThinkingSwitches(request.EnableThinking, nil, nil); err != nil {
		return err
	}
	if hideThinking {
		request.Reasoning.Effort = "low"
		request.EnableThinking = nil
		request.Temperature = nil
	}
	if err := normalizeKimiK3ChatSampling(&request.Temperature, &request.TopP, nil, nil, nil); err != nil {
		return err
	}
	chatRequest, err := ResponsesRequestToChatCompletionsRequest(request)
	if err != nil {
		return err
	}
	return NormalizeKimiK3ChatRequest(chatRequest)
}

func NormalizeKimiK3ClaudeRequest(request *dto.ClaudeRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if request.Model != "kimi-k3" {
		return fmt.Errorf("Kimi K3 compatibility requires the mapped model kimi-k3")
	}
	if request.MaxTokens == nil {
		request.MaxTokens = common.GetPointer(kimiK3DefaultMaxTokens)
	}
	if *request.MaxTokens == 0 || *request.MaxTokens > kimiK3MaxTokens {
		return fmt.Errorf("max_tokens must be between 1 and %d for kimi-k3", kimiK3MaxTokens)
	}
	if request.MaxTokensToSample != nil {
		return fmt.Errorf("max_tokens_to_sample is not supported by kimi-k3")
	}
	hideThinking := KimiK3RequestDisablesThinking(request)
	var outputConfig dto.OutputConfigForEffort
	if len(request.OutputConfig) > 0 {
		if err := common.Unmarshal(request.OutputConfig, &outputConfig); err != nil {
			return fmt.Errorf("invalid output_config: %w", err)
		}
	}
	applyKimiK3ReasoningEffortDefault(&outputConfig.Effort)
	if hideThinking {
		outputConfig.Effort = "low"
		request.Thinking = nil
		request.Temperature = nil
	}
	if err := normalizeKimiK3ClaudeSampling(&request.Temperature, &request.TopP); err != nil {
		return err
	}
	encoded, err := common.Marshal(outputConfig)
	if err != nil {
		return err
	}
	request.OutputConfig = encoded
	if err := validateKimiK3Stop(request.StopSequences); err != nil {
		return err
	}
	if err := validateKimiK3ClaudeToolChoice(request.ToolChoice); err != nil {
		return err
	}
	if len(request.ResponseFormat) > 0 && len(request.OutputFormat) > 0 {
		return fmt.Errorf("response_format and output_format cannot both be set for kimi-k3")
	}
	if len(request.OutputFormat) > 0 {
		request.ResponseFormat = request.OutputFormat
		request.OutputFormat = nil
	}
	if len(request.ResponseFormat) > 0 {
		var responseFormat dto.ResponseFormat
		if err := common.Unmarshal(request.ResponseFormat, &responseFormat); err != nil {
			return fmt.Errorf("invalid response_format: %w", err)
		}
		if err := validateKimiK3ResponseFormat(&responseFormat); err != nil {
			return err
		}
	}
	return validateKimiK3ClaudeMedia(request)
}

func KimiK3RequestDisablesThinking(request any) bool {
	switch value := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if value == nil {
			return false
		}
		return value.ReasoningEffort == "none" || kimiK3RawThinkingDisabled(value.EnableThinking) ||
			kimiK3RawThinkingDisabled(value.THINKING) || kimiK3ChatTemplateThinkingDisabled(value.ChatTemplateKwargs)
	case dto.GeneralOpenAIRequest:
		return KimiK3RequestDisablesThinking(&value)
	case *dto.OpenAIResponsesRequest:
		return value != nil && ((value.Reasoning != nil && value.Reasoning.Effort == "none") || kimiK3RawThinkingDisabled(value.EnableThinking))
	case dto.OpenAIResponsesRequest:
		return KimiK3RequestDisablesThinking(&value)
	case *dto.ClaudeRequest:
		return value != nil && (value.GetEfforts() == "none" || (value.Thinking != nil && value.Thinking.Type == "disabled"))
	case dto.ClaudeRequest:
		return KimiK3RequestDisablesThinking(&value)
	default:
		return false
	}
}

func kimiK3RawThinkingDisabled(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	if common.GetJsonType(raw) == "boolean" {
		var enabled bool
		return common.Unmarshal(raw, &enabled) == nil && !enabled
	}
	if common.GetJsonType(raw) != "object" {
		return false
	}
	var thinking dto.Thinking
	return common.Unmarshal(raw, &thinking) == nil && thinking.Type == "disabled"
}

func kimiK3ChatTemplateThinkingDisabled(raw json.RawMessage) bool {
	if len(raw) == 0 || common.GetJsonType(raw) != "object" {
		return false
	}
	var kwargs map[string]any
	if common.Unmarshal(raw, &kwargs) != nil {
		return false
	}
	enabled, exists := kwargs["enable_thinking"].(bool)
	return exists && !enabled
}

func validateKimiK3ResponseFormat(responseFormat *dto.ResponseFormat) error {
	if responseFormat == nil {
		return nil
	}
	switch responseFormat.Type {
	case "json_object":
		return nil
	case "json_schema":
		var schema dto.FormatJsonSchema
		if err := common.Unmarshal(responseFormat.JsonSchema, &schema); err != nil {
			return fmt.Errorf("invalid response_format.json_schema: %w", err)
		}
		if strings.TrimSpace(schema.Name) == "" || schema.Schema == nil {
			return fmt.Errorf("response_format.json_schema requires name and schema for kimi-k3")
		}
		return nil
	default:
		return fmt.Errorf("response_format.type must be json_object or json_schema for kimi-k3")
	}
}

func normalizeKimiK3ChatSampling(temperature, topP *(*float64), n *(*int), presencePenalty, frequencyPenalty *(*float64)) error {
	const expectedTemperature = 1.0
	if *temperature == nil {
		*temperature = common.GetPointer(expectedTemperature)
	} else if **temperature != expectedTemperature {
		return fmt.Errorf("temperature must be %.1f for kimi-k3", expectedTemperature)
	}
	if *topP == nil {
		*topP = common.GetPointer(0.95)
	} else if **topP != 0.95 {
		return fmt.Errorf("top_p must be 0.95 for kimi-k3")
	}
	if n != nil {
		if *n == nil {
			*n = common.GetPointer(1)
		} else if **n != 1 {
			return fmt.Errorf("n must be 1 for kimi-k3")
		}
	}
	for name, value := range map[string]*(*float64){"presence_penalty": presencePenalty, "frequency_penalty": frequencyPenalty} {
		if value == nil {
			continue
		}
		if *value == nil {
			*value = common.GetPointer(0.0)
		} else if **value != 0 {
			return fmt.Errorf("%s must be 0 for kimi-k3", name)
		}
	}
	return nil
}

func normalizeKimiK3ClaudeSampling(temperature, topP *(*float64)) error {
	if *temperature != nil && (**temperature < 0 || **temperature > 1) {
		return fmt.Errorf("temperature must be between 0 and 1 for kimi-k3")
	}
	if *topP != nil && (**topP < 0 || **topP > 1) {
		return fmt.Errorf("top_p must be between 0 and 1 for kimi-k3")
	}
	return nil
}

func applyKimiK3ReasoningEffortDefault(effort *string) {
	if effort != nil && *effort == "" {
		*effort = "max"
	}
}

func validateKimiK3ThinkingSwitches(values ...json.RawMessage) error {
	for _, raw := range values {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		if common.GetJsonType(raw) == "boolean" {
			var value bool
			if err := common.Unmarshal(raw, &value); err != nil {
				return err
			}
			continue
		}
		var config map[string]any
		if err := common.Unmarshal(raw, &config); err != nil {
			return fmt.Errorf("invalid thinking configuration: %w", err)
		}
	}
	return nil
}

func validateKimiK3Stop(stop any) error {
	if stop == nil {
		return nil
	}
	var sequences []string
	switch value := stop.(type) {
	case string:
		sequences = []string{value}
	case []string:
		sequences = value
	default:
		converted, err := common.Any2Type[[]string](stop)
		if err != nil {
			return fmt.Errorf("stop must be a string or an array of strings")
		}
		sequences = converted
	}
	if len(sequences) > kimiK3MaxStopCount {
		return fmt.Errorf("stop supports at most %d sequences for kimi-k3", kimiK3MaxStopCount)
	}
	for _, sequence := range sequences {
		if sequence == "" || len([]byte(sequence)) > kimiK3MaxStopBytes {
			return fmt.Errorf("each stop sequence must contain 1 to %d UTF-8 bytes for kimi-k3", kimiK3MaxStopBytes)
		}
	}
	return nil
}

func validateKimiK3ToolChoice(choice any) error {
	if choice == nil {
		return nil
	}
	if value, ok := choice.(string); ok {
		switch value {
		case "auto", "none", "required":
			return nil
		default:
			return fmt.Errorf("tool_choice must be auto, none, required, or allowed_tools for kimi-k3")
		}
	}
	choiceMap, err := common.Any2Type[map[string]any](choice)
	if err != nil {
		return fmt.Errorf("invalid tool_choice")
	}
	if common.Interface2String(choiceMap["type"]) == "allowed_tools" {
		return nil
	}
	return fmt.Errorf("named function tool_choice is incompatible with kimi-k3 reasoning")
}

func validateKimiK3ClaudeToolChoice(choice any) error {
	if choice == nil {
		return nil
	}
	parsed, err := common.Any2Type[dto.ClaudeToolChoice](choice)
	if err != nil {
		return fmt.Errorf("invalid tool_choice")
	}
	switch parsed.Type {
	case "auto", "none", "any":
		return nil
	case "tool":
		return fmt.Errorf("named tool_choice is incompatible with kimi-k3 reasoning")
	default:
		return fmt.Errorf("tool_choice must be auto, none, or any for kimi-k3")
	}
}

func validateKimiK3ChatMedia(messages []dto.Message) error {
	for _, message := range messages {
		for _, part := range message.ParseContent() {
			switch part.Type {
			case dto.ContentTypeImageURL:
				image := part.GetImageMedia()
				if image == nil {
					return fmt.Errorf("invalid image_url content")
				}
				if err := validateKimiK3ImageURL(image.Url); err != nil {
					return err
				}
			case dto.ContentTypeVideoUrl:
				video := part.GetVideoUrl()
				if video == nil || !strings.HasPrefix(video.Url, "ms://") {
					return fmt.Errorf("kimi-k3 video input must use an uploaded ms:// file")
				}
			}
		}
	}
	return nil
}

func validateKimiK3ImageURL(value string) error {
	if strings.HasPrefix(value, "ms://") {
		return nil
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "data:image/svg+xml") {
		return fmt.Errorf("SVG images are not supported by kimi-k3")
	}
	if strings.HasPrefix(lower, "data:image/") && strings.Contains(lower, ";base64,") {
		return nil
	}
	return fmt.Errorf("kimi-k3 image input must use a base64 data URL or an uploaded ms:// file")
}

func validateKimiK3ClaudeMedia(request *dto.ClaudeRequest) error {
	for _, message := range request.Messages {
		if message.IsStringContent() {
			continue
		}
		parts, err := message.ParseContent()
		if err != nil {
			return fmt.Errorf("invalid Anthropic message content: %w", err)
		}
		for _, part := range parts {
			if part.Type != "image" && part.Type != "video" {
				continue
			}
			if part.Source == nil {
				return fmt.Errorf("%s source is required", part.Type)
			}
			if part.Type == "video" {
				if !strings.HasPrefix(part.Source.Url, "ms://") {
					return fmt.Errorf("kimi-k3 video input must use an uploaded ms:// file")
				}
				continue
			}
			if part.Source.Type == "base64" {
				if strings.EqualFold(part.Source.MediaType, "image/svg+xml") {
					return fmt.Errorf("SVG images are not supported by kimi-k3")
				}
				continue
			}
			if err := validateKimiK3ImageURL(part.Source.Url); err != nil {
				return err
			}
		}
	}
	return nil
}
