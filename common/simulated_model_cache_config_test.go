package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetSimulatedModelCacheMemoryBudgetMBNormalizesBounds(t *testing.T) {
	original := GetSimulatedModelCacheMemoryBudgetMB()
	t.Cleanup(func() {
		SetSimulatedModelCacheMemoryBudgetMB(original)
	})

	assert.Equal(t, 512, SetSimulatedModelCacheMemoryBudgetMB(512))
	assert.Equal(t, int64(512*1024*1024), GetSimulatedModelCacheMemoryBudgetBytes())
	assert.Equal(t, SimulatedModelCacheDefaultMemoryBudgetMB, SetSimulatedModelCacheMemoryBudgetMB(0))
	assert.Equal(t, SimulatedModelCacheMaxMemoryBudgetMB, SetSimulatedModelCacheMemoryBudgetMB(SimulatedModelCacheMaxMemoryBudgetMB+1))
}

func TestIsValidSimulatedModelCacheMemoryBudgetMB(t *testing.T) {
	assert.False(t, IsValidSimulatedModelCacheMemoryBudgetMB(0))
	assert.True(t, IsValidSimulatedModelCacheMemoryBudgetMB(1))
	assert.True(t, IsValidSimulatedModelCacheMemoryBudgetMB(SimulatedModelCacheMaxMemoryBudgetMB))
	assert.False(t, IsValidSimulatedModelCacheMemoryBudgetMB(SimulatedModelCacheMaxMemoryBudgetMB+1))
}
