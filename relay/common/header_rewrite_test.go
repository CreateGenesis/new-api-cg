package common

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaderRewritePrecedenceAndPlaceholders(t *testing.T) {
	original := operation_setting.HeaderRewritePresets2JSONString()
	t.Cleanup(func() {
		require.NoError(t, operation_setting.UpdateHeaderRewritePresetsByJSONString(original))
	})
	require.NoError(t, operation_setting.UpdateHeaderRewritePresetsByJSONString(`{
		"test": {
			"name": "Test",
			"remove": ["X-Source-*"],
			"set": {"User-Agent": "preset/1", "X-Trace": "{client_header:X-Trace|request_id}"}
		}
	}`))

	headers := http.Header{
		"User-Agent":   []string{"adapter/1"},
		"X-Source-App": []string{"sdk"},
	}
	err := ResolveAndApplyHeaderRewrite(headers, HeaderRewriteInput{
		ChannelSetting: dto.ChannelSettings{HeaderRewrite: &types.ChannelHeaderRewriteSettings{
			PresetID: "test",
			HeaderRewriteRule: types.HeaderRewriteRule{
				Set: map[string]string{"User-Agent": "channel/1"},
			},
		}},
		LegacyOverride:    map[string]interface{}{"User-Agent": "legacy/1"},
		IncomingHeaders:   http.Header{"X-Trace": []string{"client-trace"}},
		RequestID:         "request-1",
		AllowClientHeader: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "legacy/1", headers.Get("User-Agent"))
	assert.Equal(t, "client-trace", headers.Get("X-Trace"))
	assert.Empty(t, headers.Get("X-Source-App"))
}

func TestHeaderRewriteRequestIDFallback(t *testing.T) {
	headers := http.Header{}
	err := ResolveAndApplyHeaderRewrite(headers, HeaderRewriteInput{
		ChannelSetting: dto.ChannelSettings{HeaderRewrite: &types.ChannelHeaderRewriteSettings{
			HeaderRewriteRule: types.HeaderRewriteRule{Set: map[string]string{
				"Session-Id": "{client_header:Session-Id|request_id}",
				"Thread-Id":  "{request_id}",
			}},
		}},
		RequestID: "request-2",
	})
	require.NoError(t, err)
	assert.Equal(t, "request-2", headers.Get("Session-Id"))
	assert.Equal(t, "request-2", headers.Get("Thread-Id"))
}

func TestHeaderRewriteUserAgentRemovalSuppressesDefault(t *testing.T) {
	headers := http.Header{"user-agent": []string{"adapter/1"}}
	ApplyHeaderMutationPlan(headers, HeaderMutationPlan{
		ChannelRule: types.HeaderRewriteRule{Remove: []string{"User-Agent"}},
	})
	assert.NotContains(t, headers, "user-agent")
	value, exists := headers["User-Agent"]
	assert.True(t, exists)
	assert.Empty(t, value)
}

func TestHeaderRewriteSetReplacesNonCanonicalHeaderKey(t *testing.T) {
	headers := http.Header{"x-test": []string{"adapter"}}
	ApplyHeaderMutationPlan(headers, HeaderMutationPlan{
		ChannelRule: types.HeaderRewriteRule{Set: map[string]string{"X-Test": "channel"}},
	})
	assert.NotContains(t, headers, "x-test")
	assert.Equal(t, []string{"channel"}, headers.Values("X-Test"))
}

func TestHeaderRewriteLegacyExplicitWinsAfterChannelRemove(t *testing.T) {
	headers := http.Header{"X-Test": []string{"adapter"}}
	err := ResolveAndApplyHeaderRewrite(headers, HeaderRewriteInput{
		ChannelSetting: dto.ChannelSettings{HeaderRewrite: &types.ChannelHeaderRewriteSettings{
			HeaderRewriteRule: types.HeaderRewriteRule{Remove: []string{"X-Test"}},
		}},
		LegacyOverride: map[string]interface{}{"X-Test": "legacy"},
	})
	require.NoError(t, err)
	assert.Equal(t, "legacy", headers.Get("X-Test"))
}

func TestHeaderRewriteLegacyNormalization(t *testing.T) {
	plan, err := ResolveHeaderMutationPlan(HeaderRewriteInput{
		LegacyOverride: map[string]interface{}{
			"*":              true,
			"  X-Explicit  ": "explicit",
			"   ":            123,
		},
		IncomingHeaders: http.Header{
			"X-Trace": []string{"  incoming  "},
			"X-Empty": []string{"   "},
		},
		AllowClientHeader: true,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"x-trace": "incoming"}, plan.LegacyPassthrough)
	assert.Equal(t, map[string]string{"x-explicit": "explicit"}, plan.LegacyExplicit)
}

func TestHeaderRewriteLegacyClientPlaceholderWithoutRequestContextIsIgnored(t *testing.T) {
	headers := http.Header{}
	err := ResolveAndApplyHeaderRewrite(headers, HeaderRewriteInput{
		LegacyOverride: map[string]interface{}{
			"X-Client-Trace": "{client_header:X-Trace}",
		},
	})
	require.NoError(t, err)
	assert.Empty(t, headers.Get("X-Client-Trace"))
}
