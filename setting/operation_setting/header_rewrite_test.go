package operation_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHeaderRewritePresetsJSONString(t *testing.T) {
	presets, err := ParseHeaderRewritePresetsJSONString(`{
		"custom": {
			"name": "Custom CLI",
			"remove": ["X-Stainless-*"],
			"set": {
				"User-Agent": "custom/1.0",
				"X-Request-Id": "{client_header:X-Request-Id|request_id}"
			}
		}
	}`)
	require.NoError(t, err)
	assert.Equal(t, "Custom CLI", presets["custom"].Name)
	assert.Equal(t, "custom/1.0", presets["custom"].Set["User-Agent"])
}

func TestParseHeaderRewritePresetsJSONStringRejectsNull(t *testing.T) {
	_, err := ParseHeaderRewritePresetsJSONString("null")
	require.Error(t, err)
}

func TestValidateHeaderRewriteRuleRejectsProtectedHeaders(t *testing.T) {
	tests := []types.HeaderRewriteRule{
		{Remove: []string{"Authorization"}},
		{Remove: []string{"X-Amz-*"}},
		{Remove: []string{"X-Amz-Custom-*"}},
		{Remove: []string{"X-Am*-Custom"}},
		{Remove: []string{"Sec-WebSocket-Custom-*"}},
		{Remove: []string{"X-*"}},
		{Set: map[string]string{"Content-Length": "10"}},
		{Set: map[string]string{"Authorization": "Bearer test"}},
		{Set: map[string]string{"Origin": "https://example.com"}},
		{Set: map[string]string{"Sec-WebSocket-Custom": "test"}},
	}
	for _, rule := range tests {
		assert.Error(t, ValidateHeaderRewriteRule(rule))
	}
	assert.NoError(t, ValidateHeaderRewriteRule(types.HeaderRewriteRule{
		Remove: []string{"X-Custom-*"},
		Set:    map[string]string{"X-Client-Name": "custom"},
	}))
}

func TestValidateHeaderRewriteRuleRejectsCaseInsensitiveDuplicateSetKeys(t *testing.T) {
	assert.Error(t, ValidateHeaderRewriteRule(types.HeaderRewriteRule{
		Set: map[string]string{
			"User-Agent": "first",
			"user-agent": "second",
		},
	}))
}

func TestValidateHeaderRewriteRuleRejectsUnsafeValues(t *testing.T) {
	assert.Error(t, ValidateHeaderRewriteRule(types.HeaderRewriteRule{
		Set: map[string]string{"X-Test": "ok\r\ninjected: true"},
	}))
	assert.Error(t, ValidateHeaderRewriteRule(types.HeaderRewriteRule{
		Set: map[string]string{"X-Test": "{api_key}"},
	}))
}

func TestUpdateHeaderRewritePresetsUsesImmutableSnapshot(t *testing.T) {
	original := HeaderRewritePresets2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateHeaderRewritePresetsByJSONString(original))
	})

	require.NoError(t, UpdateHeaderRewritePresetsByJSONString(`{
		"custom": {"name": "Custom", "set": {"User-Agent": "custom/1.0"}}
	}`))
	preset, ok := GetHeaderRewritePreset("custom")
	require.True(t, ok)
	preset.Set["User-Agent"] = "mutated"

	stored, ok := GetHeaderRewritePreset("custom")
	require.True(t, ok)
	assert.Equal(t, "custom/1.0", stored.Set["User-Agent"])
}
