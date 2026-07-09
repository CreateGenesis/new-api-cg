package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelValidateSettingsAcceptsStatusCodeRetryDefaults(t *testing.T) {
	channel := &Channel{
		OtherSettings: `{"status_code_retry":{"enabled":true}}`,
	}

	require.NoError(t, channel.ValidateSettings())
}

func TestChannelValidateSettingsRejectsInvalidStatusCodeRetryRules(t *testing.T) {
	channel := &Channel{
		OtherSettings: `{"status_code_retry":{"enabled":true,"status_codes":"99,600"}}`,
	}

	err := channel.ValidateSettings()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status_code_retry.status_codes")
}

func TestChannelMatchesInputTokenRouting(t *testing.T) {
	tests := []struct {
		name            string
		settings        string
		estimatedTokens *int
		want            bool
	}{
		{
			name:            "nil estimate ignores routing",
			settings:        `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimatedTokens: nil,
			want:            true,
		},
		{
			name:            "unconfigured channel remains eligible",
			settings:        `{}`,
			estimatedTokens: intPtr(12000),
			want:            true,
		},
		{
			name:            "below or equal max matches",
			settings:        `{"input_token_routing":{"enabled":true,"max_tokens":8000}}`,
			estimatedTokens: intPtr(8000),
			want:            true,
		},
		{
			name:            "above max does not match",
			settings:        `{"input_token_routing":{"enabled":true,"max_tokens":8000}}`,
			estimatedTokens: intPtr(8001),
			want:            false,
		},
		{
			name:            "above or equal min matches",
			settings:        `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimatedTokens: intPtr(9000),
			want:            true,
		},
		{
			name:            "below min does not match",
			settings:        `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimatedTokens: intPtr(8000),
			want:            false,
		},
		{
			name:            "inside bounded range matches",
			settings:        `{"input_token_routing":{"enabled":true,"min_tokens":4001,"max_tokens":8000}}`,
			estimatedTokens: intPtr(6000),
			want:            true,
		},
		{
			name:            "outside bounded range does not match",
			settings:        `{"input_token_routing":{"enabled":true,"min_tokens":4001,"max_tokens":8000}}`,
			estimatedTokens: intPtr(9000),
			want:            false,
		},
		{
			name:            "inside any configured range matches",
			settings:        `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":0,"max_tokens":200},{"min_tokens":5000000,"max_tokens":1000000}]}}`,
			estimatedTokens: intPtr(1500000),
			want:            true,
		},
		{
			name:            "outside all configured ranges does not match",
			settings:        `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":0,"max_tokens":200},{"min_tokens":1000000,"max_tokens":5000000}]}}`,
			estimatedTokens: intPtr(500000),
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{OtherSettings: tt.settings}

			assert.Equal(t, tt.want, channel.MatchesInputTokenRouting(tt.estimatedTokens))
		})
	}
}

func intPtr(value int) *int {
	return &value
}
