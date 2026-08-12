package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func clearPreferredOwnerTables(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)
}

func TestGetPreferredModelOwnerChannelTypesReportsMoonshotForKimiK3OfficialCompatibility(t *testing.T) {
	const modelName = "client-k3"

	tests := []struct {
		name          string
		settings      dto.ChannelOtherSettings
		modelMapping  map[string]string
		expectedOwner int
	}{
		{
			name:          "enabled exact mapping",
			settings:      dto.ChannelOtherSettings{KimiK3OfficialCompatibility: true},
			modelMapping:  map[string]string{"client-k3": "intermediate", "intermediate": "kimi-k3"},
			expectedOwner: constant.ChannelTypeMoonshot,
		},
		{
			name:          "compatibility disabled",
			modelMapping:  map[string]string{"client-k3": "kimi-k3"},
			expectedOwner: constant.ChannelTypeAnthropic,
		},
		{
			name:          "coding model is not exact k3",
			settings:      dto.ChannelOtherSettings{KimiK3OfficialCompatibility: true},
			modelMapping:  map[string]string{"client-k3": "kimi-for-coding"},
			expectedOwner: constant.ChannelTypeAnthropic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearPreferredOwnerTables(t)
			insertPreferredOwnerCandidate(t, 1, modelName, "default", constant.ChannelTypeAnthropic, 0, 0, common.ChannelStatusEnabled, true)
			settings, err := common.Marshal(tt.settings)
			require.NoError(t, err)
			mapping, err := common.Marshal(tt.modelMapping)
			require.NoError(t, err)
			require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 1).Updates(map[string]any{
				"settings":      string(settings),
				"model_mapping": string(mapping),
			}).Error)

			owners, err := GetPreferredModelOwnerChannelTypes([]string{modelName}, []string{"default"})
			require.NoError(t, err)
			require.Equal(t, tt.expectedOwner, owners[modelName])
		})
	}
}

func insertPreferredOwnerCandidate(
	t *testing.T,
	channelID int,
	modelName string,
	group string,
	channelType int,
	priority int64,
	weight uint,
	channelStatus int,
	abilityEnabled bool,
) {
	t.Helper()
	require.NoError(t, DB.Create(&Channel{
		Id:     channelID,
		Type:   channelType,
		Key:    fmt.Sprintf("key-%d", channelID),
		Status: channelStatus,
		Name:   fmt.Sprintf("channel-%d", channelID),
	}).Error)
	require.NoError(t, DB.Create(&Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: channelID,
		Enabled:   abilityEnabled,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func TestGetPreferredModelOwnerChannelTypes(t *testing.T) {
	const modelName = "gpt-5.4"

	tests := []struct {
		name     string
		setup    func(t *testing.T)
		groups   []string
		expected int
		found    bool
	}{
		{
			name: "openai only",
			setup: func(t *testing.T) {
				insertPreferredOwnerCandidate(t, 1, modelName, "default", constant.ChannelTypeOpenAI, 0, 0, common.ChannelStatusEnabled, true)
			},
			groups:   []string{"default"},
			expected: constant.ChannelTypeOpenAI,
			found:    true,
		},
		{
			name: "codex only",
			setup: func(t *testing.T) {
				insertPreferredOwnerCandidate(t, 1, modelName, "default", constant.ChannelTypeCodex, 0, 0, common.ChannelStatusEnabled, true)
			},
			groups:   []string{"default"},
			expected: constant.ChannelTypeCodex,
			found:    true,
		},
		{
			name: "priority wins",
			setup: func(t *testing.T) {
				insertPreferredOwnerCandidate(t, 1, modelName, "default", constant.ChannelTypeOpenAI, 1, 100, common.ChannelStatusEnabled, true)
				insertPreferredOwnerCandidate(t, 2, modelName, "default", constant.ChannelTypeCodex, 2, 0, common.ChannelStatusEnabled, true)
			},
			groups:   []string{"default"},
			expected: constant.ChannelTypeCodex,
			found:    true,
		},
		{
			name: "weight wins when priority is equal",
			setup: func(t *testing.T) {
				insertPreferredOwnerCandidate(t, 1, modelName, "default", constant.ChannelTypeOpenAI, 1, 10, common.ChannelStatusEnabled, true)
				insertPreferredOwnerCandidate(t, 2, modelName, "default", constant.ChannelTypeCodex, 1, 20, common.ChannelStatusEnabled, true)
			},
			groups:   []string{"default"},
			expected: constant.ChannelTypeCodex,
			found:    true,
		},
		{
			name: "channel id stabilizes exact ties",
			setup: func(t *testing.T) {
				insertPreferredOwnerCandidate(t, 2, modelName, "default", constant.ChannelTypeCodex, 1, 10, common.ChannelStatusEnabled, true)
				insertPreferredOwnerCandidate(t, 1, modelName, "default", constant.ChannelTypeOpenAI, 1, 10, common.ChannelStatusEnabled, true)
			},
			groups:   []string{"default"},
			expected: constant.ChannelTypeOpenAI,
			found:    true,
		},
		{
			name: "group filter excludes other groups",
			setup: func(t *testing.T) {
				insertPreferredOwnerCandidate(t, 1, modelName, "vip", constant.ChannelTypeCodex, 10, 100, common.ChannelStatusEnabled, true)
				insertPreferredOwnerCandidate(t, 2, modelName, "default", constant.ChannelTypeOpenAI, 1, 0, common.ChannelStatusEnabled, true)
			},
			groups:   []string{"default"},
			expected: constant.ChannelTypeOpenAI,
			found:    true,
		},
		{
			name: "disabled candidates are ignored",
			setup: func(t *testing.T) {
				insertPreferredOwnerCandidate(t, 1, modelName, "default", constant.ChannelTypeCodex, 10, 100, common.ChannelStatusEnabled, false)
				insertPreferredOwnerCandidate(t, 2, modelName, "default", constant.ChannelTypeOpenAI, 1, 0, common.ChannelStatusManuallyDisabled, true)
			},
			groups: []string{"default"},
			found:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearPreferredOwnerTables(t)
			tt.setup(t)

			owners, err := GetPreferredModelOwnerChannelTypes([]string{modelName}, tt.groups)
			require.NoError(t, err)

			got, ok := owners[modelName]
			require.Equal(t, tt.found, ok)
			if tt.found {
				require.Equal(t, tt.expected, got)
			}
		})
	}
}
