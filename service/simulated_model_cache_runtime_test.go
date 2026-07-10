package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimulatedModelCacheMemoryBudgetRejectsOvercommit(t *testing.T) {
	budget := &simulatedModelCacheMemoryBudget{}
	first := budget.reserve(60, 100)
	require.NotNil(t, first)
	assert.Nil(t, budget.reserve(41, 100))

	first.Release()
	second := budget.reserve(100, 100)
	require.NotNil(t, second)
	second.Release()
	assert.Equal(t, int64(0), budget.used.Load())
}

func TestSimulatedModelCacheMemoryBudgetUsesLatestLimit(t *testing.T) {
	budget := &simulatedModelCacheMemoryBudget{}
	first := budget.reserve(80, 100)
	require.NotNil(t, first)
	assert.Nil(t, budget.reserve(1, 50), "lower limits must apply to new reservations immediately")

	first.Release()
	second := budget.reserve(150, 200)
	require.NotNil(t, second, "raised limits must apply without recreating the runtime")
	second.Release()
}

func TestReserveSimulatedModelCacheMemoryUsesHotUpdatedSetting(t *testing.T) {
	original := common.GetSimulatedModelCacheMemoryBudgetMB()
	t.Cleanup(func() {
		common.SetSimulatedModelCacheMemoryBudgetMB(original)
	})

	common.SetSimulatedModelCacheMemoryBudgetMB(1)
	assert.Nil(t, ReserveSimulatedModelCacheMemory(2*1024*1024))

	common.SetSimulatedModelCacheMemoryBudgetMB(2)
	reservation := ReserveSimulatedModelCacheMemory(2 * 1024 * 1024)
	require.NotNil(t, reservation)
	reservation.Release()
}

func TestSimulatedModelCacheMatchPoolRejectsFullQueue(t *testing.T) {
	pool := &simulatedModelCacheMatchPool{queue: make(chan simulatedModelCacheMatchJob, 1)}
	job := simulatedModelCacheMatchJob{ctx: context.Background()}

	assert.True(t, pool.trySubmit(job))
	assert.False(t, pool.trySubmit(job))
}
