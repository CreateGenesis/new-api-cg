package deepseek

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deepSeekV4SanitizationInfo(model string, enabled bool) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: model,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeDeepSeek,
			UpstreamModelName: model,
			ChannelOtherSettings: dto.ChannelOtherSettings{
				DeepSeekV4RequestSanitization: enabled,
			},
		},
	}
}

func TestSanitizeV4RequestJSONClaudeRequest(t *testing.T) {
	input := []byte(`{
		"model":"deepseek-v4-pro",
		"max_tokens":500000,
		"top_k":100,
		"tools":[
			{
				"name":"read_lints",
				"input_schema":{
					"type":"object",
					"required":null,
					"properties":{
						"paths":{
							"type":"array",
							"items":{"type":"string","required":"invalid"}
						}
					}
				}
			},
			{
				"name":"valid_tool",
				"input_schema":{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}
			}
		]
	}`)

	out, result, err := SanitizeV4RequestJSON(input, deepSeekV4SanitizationInfo("deepseek-v4-pro-max", true))
	require.NoError(t, err)
	assert.Equal(t, RequestSanitizationResult{MaxTokenFields: 1, TopKFields: 1, SchemaFields: 2}, result)

	var body map[string]any
	require.NoError(t, common.Unmarshal(out, &body))
	assert.Equal(t, float64(deepSeekV4MaxTokens), body["max_tokens"])
	assert.Equal(t, float64(deepSeekV4MaxTopK), body["top_k"])

	tools := body["tools"].([]any)
	readLintsSchema := tools[0].(map[string]any)["input_schema"].(map[string]any)
	assert.NotContains(t, readLintsSchema, "required")
	nestedItems := readLintsSchema["properties"].(map[string]any)["paths"].(map[string]any)["items"].(map[string]any)
	assert.NotContains(t, nestedItems, "required")
	validSchema := tools[1].(map[string]any)["input_schema"].(map[string]any)
	assert.Equal(t, []any{"path"}, validSchema["required"])
}

func TestSanitizeV4RequestJSONOpenAIRequest(t *testing.T) {
	input := []byte(`{
		"model":"deepseek-v4-flash",
		"max_tokens":393216,
		"max_completion_tokens":999999,
		"top_k":99,
		"tools":[{
			"type":"function",
			"function":{
				"name":"lookup",
				"parameters":{
					"type":"object",
					"required":["query",null,7],
					"properties":{"query":{"type":"string"}}
				}
			}
		}]
	}`)

	out, result, err := SanitizeV4RequestJSON(input, deepSeekV4SanitizationInfo("deepseek-v4-flash-none", true))
	require.NoError(t, err)
	assert.Equal(t, RequestSanitizationResult{MaxTokenFields: 1, SchemaFields: 1}, result)

	var body map[string]any
	require.NoError(t, common.Unmarshal(out, &body))
	assert.Equal(t, float64(deepSeekV4MaxTokens), body["max_tokens"])
	assert.Equal(t, float64(deepSeekV4MaxTokens), body["max_completion_tokens"])
	assert.Equal(t, float64(deepSeekV4MaxTopK), body["top_k"])

	tools := body["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	parameters := function["parameters"].(map[string]any)
	assert.Equal(t, []any{"query"}, parameters["required"])
}

func TestSanitizeV4RequestJSONOnlyAppliesToEnabledDeepSeekV4Channels(t *testing.T) {
	input := []byte(`{"model":"deepseek-v4-pro","max_tokens":500000,"top_k":100}`)

	tests := []struct {
		name string
		info *relaycommon.RelayInfo
	}{
		{name: "setting disabled", info: deepSeekV4SanitizationInfo("deepseek-v4-pro", false)},
		{name: "non v4 model", info: deepSeekV4SanitizationInfo("deepseek-chat", true)},
		{
			name: "non deepseek channel",
			info: &relaycommon.RelayInfo{
				OriginModelName: "deepseek-v4-pro",
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       constant.ChannelTypeOpenAI,
					UpstreamModelName: "deepseek-v4-pro",
					ChannelOtherSettings: dto.ChannelOtherSettings{
						DeepSeekV4RequestSanitization: true,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, result, err := SanitizeV4RequestJSON(input, tt.info)
			require.NoError(t, err)
			assert.False(t, result.Changed())
			assert.Equal(t, input, out)
		})
	}
}
