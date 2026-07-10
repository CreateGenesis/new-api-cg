package common

import "sync/atomic"

const (
	SimulatedModelCacheMinMemoryBudgetMB     = 1
	SimulatedModelCacheDefaultMemoryBudgetMB = 1024
	SimulatedModelCacheMaxMemoryBudgetMB     = 1024 * 1024
)

var simulatedModelCacheMemoryBudgetMB atomic.Int64

func init() {
	SetSimulatedModelCacheMemoryBudgetMB(GetEnvOrDefault(
		"SIMULATED_MODEL_CACHE_MEMORY_BUDGET_MB",
		SimulatedModelCacheDefaultMemoryBudgetMB,
	))
}

func IsValidSimulatedModelCacheMemoryBudgetMB(value int) bool {
	return value >= SimulatedModelCacheMinMemoryBudgetMB && value <= SimulatedModelCacheMaxMemoryBudgetMB
}

func SetSimulatedModelCacheMemoryBudgetMB(value int) int {
	if value < SimulatedModelCacheMinMemoryBudgetMB {
		value = SimulatedModelCacheDefaultMemoryBudgetMB
	}
	if value > SimulatedModelCacheMaxMemoryBudgetMB {
		value = SimulatedModelCacheMaxMemoryBudgetMB
	}
	simulatedModelCacheMemoryBudgetMB.Store(int64(value))
	return value
}

func GetSimulatedModelCacheMemoryBudgetMB() int {
	return int(simulatedModelCacheMemoryBudgetMB.Load())
}

func GetSimulatedModelCacheMemoryBudgetBytes() int64 {
	return simulatedModelCacheMemoryBudgetMB.Load() * 1024 * 1024
}
