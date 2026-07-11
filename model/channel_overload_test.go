package model

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAndNormalizeChannelInfoOverloadProtection(t *testing.T) {
	tests := []struct {
		name    string
		info    ChannelInfo
		wantErr bool
	}{
		{name: "disabled zero thresholds", info: ChannelInfo{}},
		{name: "enabled channel limit", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{Enabled: true, RequestsPerSecond: 1}}},
		{name: "enabled key tpm only", info: ChannelInfo{IsMultiKey: true, MultiKeyOverloadProtection: OverloadProtection{Enabled: true, TokensPerMinute: 1}}},
		{name: "maximum key tpm", info: ChannelInfo{IsMultiKey: true, MultiKeyOverloadProtection: OverloadProtection{Enabled: true, TokensPerMinute: math.MaxInt64}}},
		{name: "custom recovery", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{Enabled: true, RequestsPerSecond: 1, RecoverySeconds: 30}}},
		{name: "enabled all zero", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{Enabled: true}}, wantErr: true},
		{name: "negative", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{RequestsPerMinute: -1}}, wantErr: true},
		{name: "negative key tpm", info: ChannelInfo{IsMultiKey: true, MultiKeyOverloadProtection: OverloadProtection{TokensPerMinute: -1}}, wantErr: true},
		{name: "negative recovery", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{RecoverySeconds: -1}}, wantErr: true},
		{name: "recovery above limit", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{RecoverySeconds: MaxOverloadRecoverySeconds + 1}}, wantErr: true},
		{name: "above int32", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{ConcurrentRequests: math.MaxInt32 + 1}}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAndNormalizeChannelInfo(&test.info)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if test.name == "custom recovery" {
				assert.Equal(t, 30, test.info.ChannelOverloadProtection.RecoverySeconds)
			} else {
				assert.Equal(t, DefaultOverloadRecoverySeconds, test.info.ChannelOverloadProtection.RecoverySeconds)
			}
			assert.Equal(t, DefaultOverloadRecoverySeconds, test.info.MultiKeyOverloadProtection.RecoverySeconds)
		})
	}
}

func TestChannelInfoScanDefaultsMissingRecoverySeconds(t *testing.T) {
	var info ChannelInfo
	require.NoError(t, info.Scan([]byte(`{"is_multi_key":true,"multi_key_overload_protection":{"enabled":true,"tokens_per_minute":100}}`)))
	assert.Equal(t, DefaultOverloadRecoverySeconds, info.ChannelOverloadProtection.RecoverySeconds)
	assert.Equal(t, DefaultOverloadRecoverySeconds, info.MultiKeyOverloadProtection.RecoverySeconds)
}

func TestChannelOverloadTokensPerMinuteIsNormalizedToZero(t *testing.T) {
	info := ChannelInfo{
		ChannelOverloadProtection: OverloadProtection{
			Enabled:           true,
			RequestsPerMinute: 1,
			TokensPerMinute:   500,
		},
	}

	require.NoError(t, ValidateAndNormalizeChannelInfo(&info))
	assert.Zero(t, info.ChannelOverloadProtection.TokensPerMinute)
}

func TestSingleKeyChannelDisablesMultiKeyOverloadProtection(t *testing.T) {
	info := ChannelInfo{
		MultiKeyOverloadProtection: OverloadProtection{Enabled: true, RequestsPerSecond: 1},
	}

	require.NoError(t, ValidateAndNormalizeChannelInfo(&info))
	assert.False(t, info.MultiKeyOverloadProtection.Enabled)
}
