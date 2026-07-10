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
