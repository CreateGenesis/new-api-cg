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
		{name: "enabled all zero", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{Enabled: true}}, wantErr: true},
		{name: "negative", info: ChannelInfo{ChannelOverloadProtection: OverloadProtection{RequestsPerMinute: -1}}, wantErr: true},
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
		})
	}
}

func TestSingleKeyChannelDisablesMultiKeyOverloadProtection(t *testing.T) {
	info := ChannelInfo{
		MultiKeyOverloadProtection: OverloadProtection{Enabled: true, RequestsPerSecond: 1},
	}

	require.NoError(t, ValidateAndNormalizeChannelInfo(&info))
	assert.False(t, info.MultiKeyOverloadProtection.Enabled)
}
