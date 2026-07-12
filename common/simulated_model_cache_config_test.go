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

func TestSetSimulatedModelCacheEntriesPerScopeNormalizesBounds(t *testing.T) {
	original := GetSimulatedModelCacheEntriesPerScope()
	t.Cleanup(func() {
		SetSimulatedModelCacheEntriesPerScope(original)
	})

	assert.Equal(t, 250, SetSimulatedModelCacheEntriesPerScope(250))
	assert.Equal(t, 250, GetSimulatedModelCacheEntriesPerScope())
	assert.Equal(t, SimulatedModelCacheDefaultEntriesPerScope, SetSimulatedModelCacheEntriesPerScope(0))
	assert.Equal(t, SimulatedModelCacheMaxEntriesPerScope, SetSimulatedModelCacheEntriesPerScope(SimulatedModelCacheMaxEntriesPerScope+1))
}

func TestIsValidSimulatedModelCacheEntriesPerScope(t *testing.T) {
	assert.False(t, IsValidSimulatedModelCacheEntriesPerScope(0))
	assert.True(t, IsValidSimulatedModelCacheEntriesPerScope(1))
	assert.True(t, IsValidSimulatedModelCacheEntriesPerScope(SimulatedModelCacheMaxEntriesPerScope))
	assert.False(t, IsValidSimulatedModelCacheEntriesPerScope(SimulatedModelCacheMaxEntriesPerScope+1))
}

func TestSetSimulatedModelCacheMinInputTokensNormalizesBounds(t *testing.T) {
	original := GetSimulatedModelCacheMinInputTokens()
	t.Cleanup(func() {
		SetSimulatedModelCacheMinInputTokens(original)
	})

	assert.Equal(t, 0, SetSimulatedModelCacheMinInputTokens(0))
	assert.Equal(t, 0, GetSimulatedModelCacheMinInputTokens())
	assert.Equal(t, SimulatedModelCacheMinimumInputTokensDefault, SetSimulatedModelCacheMinInputTokens(-1))
	assert.Equal(t, SimulatedModelCacheMinimumInputTokensMax, SetSimulatedModelCacheMinInputTokens(SimulatedModelCacheMinimumInputTokensMax+1))
}

func TestIsValidSimulatedModelCacheMinInputTokens(t *testing.T) {
	assert.True(t, IsValidSimulatedModelCacheMinInputTokens(0))
	assert.True(t, IsValidSimulatedModelCacheMinInputTokens(SimulatedModelCacheMinimumInputTokensDefault))
	assert.True(t, IsValidSimulatedModelCacheMinInputTokens(SimulatedModelCacheMinimumInputTokensMax))
	assert.False(t, IsValidSimulatedModelCacheMinInputTokens(-1))
	assert.False(t, IsValidSimulatedModelCacheMinInputTokens(SimulatedModelCacheMinimumInputTokensMax+1))
}
