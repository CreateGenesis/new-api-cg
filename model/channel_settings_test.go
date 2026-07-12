package model

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
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
		name      string
		settings  string
		estimates *dto.InputTokenEstimates
		want      bool
	}{
		{
			name:      "nil estimate ignores routing",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimates: nil,
			want:      true,
		},
		{
			name:      "unconfigured channel remains eligible",
			settings:  `{}`,
			estimates: inputTokenEstimates(12000, 12000),
			want:      true,
		},
		{
			name:      "below or equal max matches",
			settings:  `{"input_token_routing":{"enabled":true,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(8000, 8000),
			want:      true,
		},
		{
			name:      "above max does not match",
			settings:  `{"input_token_routing":{"enabled":true,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(8001, 8001),
			want:      false,
		},
		{
			name:      "above or equal min matches",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimates: inputTokenEstimates(9000, 9000),
			want:      true,
		},
		{
			name:      "below min does not match",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimates: inputTokenEstimates(8000, 8000),
			want:      false,
		},
		{
			name:      "inside bounded range matches",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":4001,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(6000, 6000),
			want:      true,
		},
		{
			name:      "outside bounded range does not match",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":4001,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(9000, 9000),
			want:      false,
		},
		{
			name:      "inside any configured range matches",
			settings:  `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":0,"max_tokens":200},{"min_tokens":5000000,"max_tokens":1000000}]}}`,
			estimates: inputTokenEstimates(1500000, 1500000),
			want:      true,
		},
		{
			name:      "outside all configured ranges does not match",
			settings:  `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":0,"max_tokens":200},{"min_tokens":1000000,"max_tokens":5000000}]}}`,
			estimates: inputTokenEstimates(500000, 500000),
			want:      false,
		},
		{
			name:      "default and glm modes can coexist",
			settings:  `{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"ranges":[{"min_tokens":200001,"max_tokens":500000}]}}`,
			estimates: inputTokenEstimates(520000, 350000),
			want:      true,
		},
		{
			name:      "default mode ignores glm estimate",
			settings:  `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":200001,"max_tokens":500000}]}}`,
			estimates: inputTokenEstimates(520000, 350000),
			want:      false,
		},
		{
			name:      "open ended range includes lower boundary",
			settings:  `{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"ranges":[{"min_tokens":500000,"max_tokens":0}]}}`,
			estimates: inputTokenEstimates(100, 500000),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{OtherSettings: tt.settings}

			assert.Equal(t, tt.want, channel.MatchesInputTokenRouting(tt.estimates))
		})
	}
}

func inputTokenEstimates(defaultTokens int, glm52Tokens int) *dto.InputTokenEstimates {
	return &dto.InputTokenEstimates{Default: defaultTokens, GLM52: glm52Tokens}
}
