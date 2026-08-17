package relayconvert

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

const (
	deepSeekV4OfficialMaxTokens = uint(393216)
	deepSeekV4OfficialMaxStops  = 16
)

var deepSeekV4OfficialEfforts = map[string]bool{
	"none": true, "low": true, "medium": true, "high": true, "max": true, "xhigh": true,
}

func NormalizeDeepSeekV4ChatRequest(request *dto.GeneralOpenAIRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if err := validateDeepSeekV4MaxTokens("max_tokens", request.MaxTokens, true); err != nil {
		return err
	}
	request.MaxCompletionTokens = nil
	if err := validateDeepSeekV4Sampling(request.Temperature, request.TopP); err != nil {
		return err
	}
	if err := validateDeepSeekV4Penalties(request.PresencePenalty, request.FrequencyPenalty); err != nil {
		return err
	}
	if request.N != nil && *request.N != 1 {
		return fmt.Errorf("n must be 1 for DeepSeek V4 Chat Completions")
	}
	if request.StreamOptions != nil && (request.Stream == nil || !*request.Stream) {
		return fmt.Errorf("stream_options requires stream=true for DeepSeek V4 Chat Completions")
	}
	if request.TopLogProbs != nil && (request.LogProbs == nil || !*request.LogProbs) {
		return fmt.Errorf("logprobs must be true when top_logprobs is used for DeepSeek V4 Chat Completions")
	}
	if request.TopLogProbs != nil && (*request.TopLogProbs < 0 || *request.TopLogProbs > 20) {
		return fmt.Errorf("top_logprobs must be between 0 and 20 for DeepSeek V4 Chat Completions")
	}
	if err := validateDeepSeekV4Stop(request.Stop); err != nil {
		return err
	}
	if err := validateDeepSeekV4Effort(request.ReasoningEffort, true); err != nil {
		return err
	}

	thinkingType, explicitThinking, err := deepSeekV4ThinkingType(request.THINKING)
	if err != nil {
		return err
	}
	if !explicitThinking {
		thinkingType = "enabled"
		if request.ReasoningEffort == "none" {
			thinkingType = "disabled"
		}
		request.THINKING, err = kitutil.Marshal(dto.Thinking{Type: thinkingType})
		if err != nil {
			return err
		}
	}

	if request.ResponseFormat != nil {
		switch request.ResponseFormat.Type {
		case "text":
		case "json_object":
			encoded, marshalErr := kitutil.Marshal(request.Messages)
			if marshalErr != nil {
				return marshalErr
			}
			if !strings.Contains(strings.ToLower(string(encoded)), "json") {
				return fmt.Errorf("messages must contain the word json when response_format is json_object")
			}
		case "json_schema":
			return fmt.Errorf("response_format type json_schema is unavailable for DeepSeek V4 Chat Completions")
		case "":
			return fmt.Errorf("response_format.type is required for DeepSeek V4 Chat Completions")
		default:
			return fmt.Errorf("unsupported response_format.type %q for DeepSeek V4 Chat Completions", request.ResponseFormat.Type)
		}
	}
	return nil
}

func NormalizeDeepSeekV4ResponsesRequest(request *dto.OpenAIResponsesRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if err := validateDeepSeekV4MaxTokens("max_output_tokens", request.MaxOutputTokens, true); err != nil {
		return err
	}
	if err := validateDeepSeekV4Sampling(request.Temperature, request.TopP); err != nil {
		return err
	}
	if err := validateDeepSeekV4Penalties(request.PresencePenalty, request.FrequencyPenalty); err != nil {
		return err
	}
	if request.Reasoning == nil {
		request.Reasoning = &dto.Reasoning{}
	}
	return validateDeepSeekV4Effort(request.Reasoning.Effort, true)
}

func NormalizeDeepSeekV4ClaudeRequest(request *dto.ClaudeRequest) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}
	if err := validateDeepSeekV4MaxTokens("max_tokens", request.MaxTokens, false); err != nil {
		return err
	}
	if err := validateDeepSeekV4Sampling(request.Temperature, request.TopP); err != nil {
		return err
	}
	if len(request.StopSequences) > deepSeekV4OfficialMaxStops {
		return fmt.Errorf("stop_sequences must contain at most %d strings for DeepSeek V4 Anthropic Messages", deepSeekV4OfficialMaxStops)
	}
	if request.Thinking == nil {
		request.Thinking = &dto.Thinking{Type: "enabled"}
	} else {
		switch request.Thinking.Type {
		case "enabled", "adaptive", "disabled":
		default:
			return fmt.Errorf("thinking.type must be enabled, adaptive, or disabled for DeepSeek V4 Anthropic Messages")
		}
	}

	if len(request.OutputConfig) == 0 || string(request.OutputConfig) == "null" {
		return nil
	}
	if kitutil.GetJsonType(request.OutputConfig) != "object" {
		return fmt.Errorf("output_config must be an object for DeepSeek V4 Anthropic Messages")
	}
	var outputConfig dto.OutputConfigForEffort
	if err := kitutil.Unmarshal(request.OutputConfig, &outputConfig); err != nil {
		return fmt.Errorf("invalid output_config: %w", err)
	}
	return validateDeepSeekV4Effort(outputConfig.Effort, false)
}

func ApplyDeepSeekV4ClaudeControlsToChat(source *dto.ClaudeRequest, target *dto.GeneralOpenAIRequest) error {
	if source == nil || target == nil {
		return fmt.Errorf("request is nil")
	}
	if source.Thinking != nil {
		thinking, err := kitutil.Marshal(source.Thinking)
		if err != nil {
			return err
		}
		target.THINKING = thinking
	}
	if effort := source.GetEfforts(); effort != "" {
		target.ReasoningEffort = effort
	}
	return nil
}

func ApplyDeepSeekV4ChatControlsToClaude(source *dto.GeneralOpenAIRequest, target *dto.ClaudeRequest) error {
	if source == nil || target == nil {
		return fmt.Errorf("request is nil")
	}
	thinkingType, _, err := deepSeekV4ThinkingType(source.THINKING)
	if err != nil {
		return err
	}
	if thinkingType == "" {
		thinkingType = "enabled"
		if source.ReasoningEffort == "none" {
			thinkingType = "disabled"
		}
	}
	target.Thinking = &dto.Thinking{Type: thinkingType}
	if thinkingType == "disabled" || source.ReasoningEffort == "" || source.ReasoningEffort == "none" {
		target.OutputConfig = nil
		return nil
	}
	target.OutputConfig, err = kitutil.Marshal(dto.OutputConfigForEffort{Effort: source.ReasoningEffort})
	return err
}

func ApplyDeepSeekV4ResponsesControlsToClaude(source *dto.OpenAIResponsesRequest, target *dto.ClaudeRequest) error {
	if source == nil || target == nil {
		return fmt.Errorf("request is nil")
	}
	effort := ""
	if source.Reasoning != nil {
		effort = source.Reasoning.Effort
	}
	if effort == "none" {
		target.Thinking = &dto.Thinking{Type: "disabled"}
		target.OutputConfig = nil
		return nil
	}
	target.Thinking = &dto.Thinking{Type: "enabled"}
	if effort == "" {
		return nil
	}
	var err error
	target.OutputConfig, err = kitutil.Marshal(dto.OutputConfigForEffort{Effort: effort})
	return err
}

func NormalizeDeepSeekV4RequestJSON(data []byte, format types.RelayFormat) ([]byte, error) {
	switch format {
	case types.RelayFormatOpenAI:
		var request dto.GeneralOpenAIRequest
		if err := kitutil.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		if err := NormalizeDeepSeekV4ChatRequest(&request); err != nil {
			return nil, err
		}
		return kitutil.Marshal(request)
	case types.RelayFormatOpenAIResponses:
		var request dto.OpenAIResponsesRequest
		if err := kitutil.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		if err := NormalizeDeepSeekV4ResponsesRequest(&request); err != nil {
			return nil, err
		}
		return kitutil.Marshal(request)
	case types.RelayFormatClaude:
		var request dto.ClaudeRequest
		if err := kitutil.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		if err := NormalizeDeepSeekV4ClaudeRequest(&request); err != nil {
			return nil, err
		}
		return kitutil.Marshal(request)
	default:
		return data, nil
	}
}

func validateDeepSeekV4MaxTokens(name string, value *uint, enforceUpperBound bool) error {
	if value == nil {
		return nil
	}
	if *value == 0 || (enforceUpperBound && *value > deepSeekV4OfficialMaxTokens) {
		return fmt.Errorf("%s must be between 1 and %d for DeepSeek V4", name, deepSeekV4OfficialMaxTokens)
	}
	return nil
}

func validateDeepSeekV4Sampling(temperature *float64, topP *float64) error {
	if temperature != nil && (*temperature < 0 || *temperature > 2) {
		return fmt.Errorf("temperature must be between 0 and 2 for DeepSeek V4")
	}
	if topP != nil && (*topP <= 0 || *topP > 1) {
		return fmt.Errorf("top_p must be greater than 0 and at most 1 for DeepSeek V4")
	}
	return nil
}

func validateDeepSeekV4Penalties(presencePenalty *float64, frequencyPenalty *float64) error {
	if presencePenalty != nil && (*presencePenalty < -2 || *presencePenalty > 2) {
		return fmt.Errorf("presence_penalty must be between -2 and 2 for DeepSeek V4")
	}
	if frequencyPenalty != nil && (*frequencyPenalty < -2 || *frequencyPenalty > 2) {
		return fmt.Errorf("frequency_penalty must be between -2 and 2 for DeepSeek V4")
	}
	return nil
}

func validateDeepSeekV4Effort(effort string, allowNone bool) error {
	if effort == "" {
		return nil
	}
	if !deepSeekV4OfficialEfforts[effort] || (!allowNone && effort == "none") {
		if allowNone {
			return fmt.Errorf("reasoning effort must be none, low, medium, high, max, or xhigh for DeepSeek V4")
		}
		return fmt.Errorf("reasoning effort must be low, medium, high, max, or xhigh for DeepSeek V4 Anthropic Messages")
	}
	return nil
}

func validateDeepSeekV4Stop(stop any) error {
	if stop == nil {
		return nil
	}
	switch value := stop.(type) {
	case string:
		return nil
	case []string:
		if len(value) > deepSeekV4OfficialMaxStops {
			return fmt.Errorf("stop must contain at most %d strings for DeepSeek V4 Chat Completions", deepSeekV4OfficialMaxStops)
		}
		return nil
	case []any:
		if len(value) > deepSeekV4OfficialMaxStops {
			return fmt.Errorf("stop must contain at most %d strings for DeepSeek V4 Chat Completions", deepSeekV4OfficialMaxStops)
		}
		for _, item := range value {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("stop must be a string or an array of strings for DeepSeek V4 Chat Completions")
			}
		}
		return nil
	default:
		return fmt.Errorf("stop must be a string or an array of strings for DeepSeek V4 Chat Completions")
	}
}

func deepSeekV4ThinkingType(raw []byte) (string, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	if kitutil.GetJsonType(raw) != "object" {
		return "", true, fmt.Errorf("thinking must be an object for DeepSeek V4")
	}
	var thinking dto.Thinking
	if err := kitutil.Unmarshal(raw, &thinking); err != nil {
		return "", true, fmt.Errorf("invalid thinking: %w", err)
	}
	switch thinking.Type {
	case "enabled", "adaptive", "disabled":
		return thinking.Type, true, nil
	default:
		return "", true, fmt.Errorf("thinking.type must be enabled, adaptive, or disabled for DeepSeek V4")
	}
}
