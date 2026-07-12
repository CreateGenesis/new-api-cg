package performance_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
)

func TestUpdateAndSyncAppliesSimulatedModelCacheMemoryBudgetImmediately(t *testing.T) {
	original := performanceSetting
	t.Cleanup(func() {
		performanceSetting = original
		UpdateAndSync()
	})

	performanceSetting.SimulatedModelCacheMemoryBudgetMB = 256
	UpdateAndSync()
	assert.Equal(t, 256, common.GetSimulatedModelCacheMemoryBudgetMB())

	performanceSetting.SimulatedModelCacheMemoryBudgetMB = 512
	UpdateAndSync()
	assert.Equal(t, 512, common.GetSimulatedModelCacheMemoryBudgetMB())
}

func TestUpdateAndSyncAppliesSimulatedModelCacheEntryLimitImmediately(t *testing.T) {
	original := performanceSetting
	t.Cleanup(func() {
		performanceSetting = original
		UpdateAndSync()
	})

	performanceSetting.SimulatedModelCacheMaxEntriesPerScope = 25
	UpdateAndSync()
	assert.Equal(t, 25, common.GetSimulatedModelCacheEntriesPerScope())

	performanceSetting.SimulatedModelCacheMaxEntriesPerScope = 50
	UpdateAndSync()
	assert.Equal(t, 50, common.GetSimulatedModelCacheEntriesPerScope())
}

func TestUpdateAndSyncAppliesSimulatedModelCacheMinimumInputTokensImmediately(t *testing.T) {
	original := performanceSetting
	t.Cleanup(func() {
		performanceSetting = original
		UpdateAndSync()
	})

	performanceSetting.SimulatedModelCacheMinInputTokens = 0
	UpdateAndSync()
	assert.Equal(t, 0, common.GetSimulatedModelCacheMinInputTokens())

	performanceSetting.SimulatedModelCacheMinInputTokens = common.SimulatedModelCacheMinimumInputTokensDefault
	UpdateAndSync()
	assert.Equal(t, common.SimulatedModelCacheMinimumInputTokensDefault, common.GetSimulatedModelCacheMinInputTokens())

	performanceSetting.SimulatedModelCacheMinInputTokens = -1
	UpdateAndSync()
	assert.Equal(t, common.SimulatedModelCacheMinimumInputTokensDefault, common.GetSimulatedModelCacheMinInputTokens())
	assert.Equal(t, common.SimulatedModelCacheMinimumInputTokensDefault, performanceSetting.SimulatedModelCacheMinInputTokens)
}
