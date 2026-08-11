package channel

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateChannelRequestModeBlocksDisabledProductionRequests(t *testing.T) {
	tests := []struct {
		name      string
		mode      types.RequestMode
		settings  dto.ChannelOtherSettings
		errorCode types.ErrorCode
	}{
		{
			name: "stream", mode: types.RequestModeStream,
			settings: dto.ChannelOtherSettings{DisableStream: true}, errorCode: types.ErrorCodeChannelStreamDisabled,
		},
		{
			name: "non-stream", mode: types.RequestModeNonStream,
			settings: dto.ChannelOtherSettings{DisableNonStream: true}, errorCode: types.ErrorCodeChannelNonStreamDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChannelRequestMode(&relaycommon.RelayInfo{
				ClientRequestMode: tt.mode,
				ChannelMeta:       &relaycommon.ChannelMeta{ChannelOtherSettings: tt.settings},
			})
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, tt.errorCode, apiErr.GetErrorCode())
		})
	}
}

func TestValidateChannelRequestModeBypassesChannelTestsAndUnknownMode(t *testing.T) {
	settings := dto.ChannelOtherSettings{DisableStream: true, DisableNonStream: true}
	assert.NoError(t, ValidateChannelRequestMode(&relaycommon.RelayInfo{
		ClientRequestMode: types.RequestModeStream,
	}))
	assert.NoError(t, ValidateChannelRequestMode(&relaycommon.RelayInfo{
		IsChannelTest: true, ClientRequestMode: types.RequestModeStream,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelOtherSettings: settings},
	}))
	assert.NoError(t, ValidateChannelRequestMode(&relaycommon.RelayInfo{
		ClientRequestMode: types.RequestModeUnknown,
		ChannelMeta:       &relaycommon.ChannelMeta{ChannelOtherSettings: settings},
	}))
}
