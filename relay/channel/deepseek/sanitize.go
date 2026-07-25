package deepseek

import (
	"encoding/json"
	"math/big"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

const (
	deepSeekV4MaxTokens = 393216
	deepSeekV4MaxTopK   = 99
)

type RequestSanitizationResult struct {
	MaxTokenFields int
	TopKFields     int
	SchemaFields   int
}

func (r RequestSanitizationResult) Changed() bool {
	return r.MaxTokenFields > 0 || r.TopKFields > 0 || r.SchemaFields > 0
}

func SanitizeV4RequestJSON(jsonData []byte, info *relaycommon.RelayInfo) ([]byte, RequestSanitizationResult, error) {
	result := RequestSanitizationResult{}
	if !shouldSanitizeV4Request(info) {
		return jsonData, result, nil
	}

	var request map[string]json.RawMessage
	if err := common.Unmarshal(jsonData, &request); err != nil {
		return nil, result, err
	}

	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		if capNumericField(request, field, deepSeekV4MaxTokens) {
			result.MaxTokenFields++
		}
	}
	if capNumericField(request, "top_k", deepSeekV4MaxTopK) {
		result.TopKFields++
	}

	if tools, ok := request["tools"]; ok {
		sanitizedTools, changedFields, err := sanitizeTools(tools)
		if err != nil {
			return nil, result, err
		}
		if changedFields > 0 {
			request["tools"] = sanitizedTools
			result.SchemaFields = changedFields
		}
	}

	if !result.Changed() {
		return jsonData, result, nil
	}

	sanitized, err := common.Marshal(request)
	if err != nil {
		return nil, result, err
	}
	return sanitized, result, nil
}

func shouldSanitizeV4Request(info *relaycommon.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil ||
		info.ChannelType != constant.ChannelTypeDeepSeek ||
		!info.ChannelOtherSettings.DeepSeekV4RequestSanitization {
		return false
	}

	modelName := info.UpstreamModelName
	if modelName == "" {
		modelName = info.OriginModelName
	}
	return strings.HasPrefix(strings.ToLower(modelName), "deepseek-v4-")
}

func capNumericField(object map[string]json.RawMessage, field string, limit int64) bool {
	raw, ok := object[field]
	if !ok || common.GetJsonType(raw) != "number" {
		return false
	}

	value, _, err := big.ParseFloat(strings.TrimSpace(string(raw)), 10, 256, big.ToNearestEven)
	if err != nil || value.Cmp(big.NewFloat(float64(limit))) <= 0 {
		return false
	}

	object[field], _ = common.Marshal(limit)
	return true
}

func sanitizeTools(raw json.RawMessage) (json.RawMessage, int, error) {
	if common.GetJsonType(raw) != "array" {
		return raw, 0, nil
	}

	var tools []json.RawMessage
	if err := common.Unmarshal(raw, &tools); err != nil {
		return nil, 0, err
	}

	changedFields := 0
	for index, toolRaw := range tools {
		if common.GetJsonType(toolRaw) != "object" {
			continue
		}

		var tool map[string]json.RawMessage
		if err := common.Unmarshal(toolRaw, &tool); err != nil {
			return nil, 0, err
		}

		toolChanged := false
		if inputSchema, ok := tool["input_schema"]; ok {
			sanitized, count, err := sanitizeSchema(inputSchema)
			if err != nil {
				return nil, 0, err
			}
			if count > 0 {
				tool["input_schema"] = sanitized
				changedFields += count
				toolChanged = true
			}
		}

		if functionRaw, ok := tool["function"]; ok && common.GetJsonType(functionRaw) == "object" {
			var function map[string]json.RawMessage
			if err := common.Unmarshal(functionRaw, &function); err != nil {
				return nil, 0, err
			}
			if parameters, ok := function["parameters"]; ok {
				sanitized, count, err := sanitizeSchema(parameters)
				if err != nil {
					return nil, 0, err
				}
				if count > 0 {
					function["parameters"] = sanitized
					functionRaw, err = common.Marshal(function)
					if err != nil {
						return nil, 0, err
					}
					tool["function"] = functionRaw
					changedFields += count
					toolChanged = true
				}
			}
		}

		if toolChanged {
			encoded, err := common.Marshal(tool)
			if err != nil {
				return nil, 0, err
			}
			tools[index] = encoded
		}
	}

	if changedFields == 0 {
		return raw, 0, nil
	}
	encoded, err := common.Marshal(tools)
	if err != nil {
		return nil, 0, err
	}
	return encoded, changedFields, nil
}

func sanitizeSchema(raw json.RawMessage) (json.RawMessage, int, error) {
	switch common.GetJsonType(raw) {
	case "object":
		var object map[string]json.RawMessage
		if err := common.Unmarshal(raw, &object); err != nil {
			return nil, 0, err
		}

		changedFields := 0
		for key, value := range object {
			if key == "required" {
				sanitized, changed, err := sanitizeRequired(value)
				if err != nil {
					return nil, 0, err
				}
				if changed {
					changedFields++
					if sanitized == nil {
						delete(object, key)
					} else {
						object[key] = sanitized
					}
				}
				continue
			}

			sanitized, count, err := sanitizeSchema(value)
			if err != nil {
				return nil, 0, err
			}
			if count > 0 {
				object[key] = sanitized
				changedFields += count
			}
		}

		if changedFields == 0 {
			return raw, 0, nil
		}
		encoded, err := common.Marshal(object)
		if err != nil {
			return nil, 0, err
		}
		return encoded, changedFields, nil

	case "array":
		var values []json.RawMessage
		if err := common.Unmarshal(raw, &values); err != nil {
			return nil, 0, err
		}
		changedFields := 0
		for index, value := range values {
			sanitized, count, err := sanitizeSchema(value)
			if err != nil {
				return nil, 0, err
			}
			if count > 0 {
				values[index] = sanitized
				changedFields += count
			}
		}
		if changedFields == 0 {
			return raw, 0, nil
		}
		encoded, err := common.Marshal(values)
		if err != nil {
			return nil, 0, err
		}
		return encoded, changedFields, nil
	default:
		return raw, 0, nil
	}
}

func sanitizeRequired(raw json.RawMessage) (json.RawMessage, bool, error) {
	if common.GetJsonType(raw) != "array" {
		return nil, true, nil
	}

	var required []json.RawMessage
	if err := common.Unmarshal(raw, &required); err != nil {
		return nil, false, err
	}
	filtered := make([]json.RawMessage, 0, len(required))
	for _, item := range required {
		if common.GetJsonType(item) == "string" {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == len(required) {
		return raw, false, nil
	}
	encoded, err := common.Marshal(filtered)
	if err != nil {
		return nil, false, err
	}
	return encoded, true, nil
}
