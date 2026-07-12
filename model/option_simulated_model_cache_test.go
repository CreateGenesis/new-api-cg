package model

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleConfigUpdateAppliesSimulatedModelCacheMemoryBudgetImmediately(t *testing.T) {
	original := common.GetSimulatedModelCacheMemoryBudgetMB()
	t.Cleanup(func() {
		require.True(t, handleConfigUpdate(
			"performance_setting.simulated_model_cache_memory_budget_mb",
			strconv.Itoa(original),
		))
	})

	handled := handleConfigUpdate(
		"performance_setting.simulated_model_cache_memory_budget_mb",
		"384",
	)

	require.True(t, handled)
	assert.Equal(t, 384, common.GetSimulatedModelCacheMemoryBudgetMB())
}

func TestHandleConfigUpdateAppliesSimulatedModelCacheEntryLimitImmediately(t *testing.T) {
	original := common.GetSimulatedModelCacheEntriesPerScope()
	t.Cleanup(func() {
		require.True(t, handleConfigUpdate(
			"performance_setting.simulated_model_cache_max_entries_per_scope",
			strconv.Itoa(original),
		))
	})

	handled := handleConfigUpdate(
		"performance_setting.simulated_model_cache_max_entries_per_scope",
		"40",
	)

	require.True(t, handled)
	assert.Equal(t, 40, common.GetSimulatedModelCacheEntriesPerScope())
}

func TestHandleConfigUpdateAppliesSimulatedModelCacheMinimumInputTokensImmediately(t *testing.T) {
	original := common.GetSimulatedModelCacheMinInputTokens()
	t.Cleanup(func() {
		require.True(t, handleConfigUpdate(
			"performance_setting.simulated_model_cache_min_input_tokens",
			strconv.Itoa(original),
		))
	})

	handled := handleConfigUpdate(
		"performance_setting.simulated_model_cache_min_input_tokens",
		"0",
	)

	require.True(t, handled)
	assert.Equal(t, 0, common.GetSimulatedModelCacheMinInputTokens())

	require.True(t, handleConfigUpdate(
		"performance_setting.simulated_model_cache_min_input_tokens",
		strconv.Itoa(common.SimulatedModelCacheMinimumInputTokensDefault),
	))
	assert.Equal(t, common.SimulatedModelCacheMinimumInputTokensDefault, common.GetSimulatedModelCacheMinInputTokens())
}
