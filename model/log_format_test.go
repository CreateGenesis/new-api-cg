package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/require"
)

// TestFormatUserLogsStripsQuotaSaturation verifies the admin-only quota
// saturation marker (nested under other.admin_info) is removed for non-admin
// log views, since formatUserLogs strips the whole admin_info object.
func TestFormatUserLogsStripsQuotaSaturation(t *testing.T) {
	other := common.MapToJsonStr(map[string]interface{}{
		"model_price": 0.004,
		"admin_info": map[string]interface{}{
			"quota_saturation": map[string]interface{}{
				"op":      "QuotaFromDecimal",
				"kind":    "overflow",
				"clamped": common.MaxQuota,
			},
		},
	})
	logs := []*Log{{Other: other}}

	formatUserLogs(logs, 0)

	parsed, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	_, hasAdminInfo := parsed["admin_info"]
	require.False(t, hasAdminInfo, "admin_info (and nested quota_saturation) must be stripped for non-admin views")
	// Non-admin billing fields remain visible.
	require.Contains(t, parsed, "model_price")
}

func TestFormatUserLogsRedactsModelMapping(t *testing.T) {
	other := common.MapToJsonStr(map[string]interface{}{
		"model_ratio":         1.5,
		"is_model_mapped":     true,
		"upstream_model_name": "xopglm52",
	})
	logs := []*Log{{
		ModelName: "glm-5.2",
		Other:     other,
	}}

	formatUserLogs(logs, 0)

	require.Equal(t, "glm-5.2", logs[0].ModelName)
	parsed, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	require.NotContains(t, parsed, "is_model_mapped")
	require.NotContains(t, parsed, "upstream_model_name")
	require.Contains(t, parsed, "model_ratio")
}

func TestFormatUserLogsRedactsErrorDiagnostics(t *testing.T) {
	other := common.MapToJsonStr(map[string]interface{}{
		"error_type":   "openai_error",
		"error_code":   "bad_response_status_code",
		"status_code":  429,
		"channel_id":   12,
		"channel_name": "primary",
		"channel_type": 1,
		"request_path": "/v1/chat/completions",
		"admin_info": map[string]interface{}{
			"upstream_status_code": 429,
			"upstream_response":    `{"error":{"message":"rate limited"}}`,
		},
	})
	logs := []*Log{{
		Type:              LogTypeError,
		Content:           "status_code=429, rate limited",
		ChannelId:         12,
		ChannelName:       "primary",
		UpstreamRequestId: "upstream-request-id",
		Other:             other,
	}}

	formatUserLogs(logs, 0)

	require.Equal(t, "rate limited", logs[0].Content)
	require.Zero(t, logs[0].ChannelId)
	require.Empty(t, logs[0].ChannelName)
	require.Empty(t, logs[0].UpstreamRequestId)
	parsed, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	for _, key := range []string{
		"error_type",
		"error_code",
		"status_code",
		"channel_id",
		"channel_name",
		"channel_type",
		"request_path",
		"admin_info",
	} {
		require.NotContains(t, parsed, key)
	}
}
