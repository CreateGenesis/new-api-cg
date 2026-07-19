package dto

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamInterruptionBillingSettingsJSONCompatibility(t *testing.T) {
	tests := []struct {
		name string
		json string
		mode StreamInterruptionBillingMode
	}{
		{name: "missing stays disabled", json: `{}`},
		{name: "input only", json: `{"stream_interruption_billing":{"mode":"input_only_free"}}`, mode: StreamInterruptionBillingModeInputOnlyFree},
		{name: "all interrupted", json: `{"stream_interruption_billing":{"mode":"all_interrupted_free"}}`, mode: StreamInterruptionBillingModeAllInterruptedFree},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var settings ChannelOtherSettings
			require.NoError(t, common.UnmarshalJsonStr(tt.json, &settings))
			if tt.mode == "" {
				assert.Nil(t, settings.StreamInterruptionBilling)
				return
			}
			require.NotNil(t, settings.StreamInterruptionBilling)
			assert.Equal(t, tt.mode, settings.StreamInterruptionBilling.Mode)
			require.NoError(t, settings.StreamInterruptionBilling.Validate())

			encoded, err := common.Marshal(settings)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), string(tt.mode))
		})
	}
}

func TestAdvancedCustomValidateResponsesToChatConverterPath(t *testing.T) {
	valid := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions,
			},
		},
	}
	require.NoError(t, valid.Validate())

	tests := []struct {
		name         string
		incomingPath string
	}{
		{name: "chat completions", incomingPath: "/v1/chat/completions"},
		{name: "responses compact", incomingPath: "/v1/responses/compact"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdvancedCustomConfig{
				Routes: []AdvancedCustomRoute{
					{
						IncomingPath: tt.incomingPath,
						UpstreamPath: "/v1/chat/completions",
						Converter:    AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions,
					},
				},
			}
			err := config.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "converter does not match incoming_path")
		})
	}
}
