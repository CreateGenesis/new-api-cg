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
