package common

import "sync/atomic"

const (
	SimulatedModelCacheMinMemoryBudgetMB     = 1
	SimulatedModelCacheDefaultMemoryBudgetMB = 1024
	SimulatedModelCacheMaxMemoryBudgetMB     = 1024 * 1024

	SimulatedModelCacheMinEntriesPerScope     = 1
	SimulatedModelCacheDefaultEntriesPerScope = 100
	SimulatedModelCacheMaxEntriesPerScope     = 5000

	SimulatedModelCacheMinimumInputTokensMin     = 0
	SimulatedModelCacheMinimumInputTokensDefault = 128
	SimulatedModelCacheMinimumInputTokensMax     = 1000000
)

var simulatedModelCacheMemoryBudgetMB atomic.Int64
var simulatedModelCacheEntriesPerScope atomic.Int64
var simulatedModelCacheMinInputTokens atomic.Int64

func init() {
	SetSimulatedModelCacheMemoryBudgetMB(GetEnvOrDefault(
		"SIMULATED_MODEL_CACHE_MEMORY_BUDGET_MB",
		SimulatedModelCacheDefaultMemoryBudgetMB,
	))
	SetSimulatedModelCacheEntriesPerScope(SimulatedModelCacheDefaultEntriesPerScope)
	SetSimulatedModelCacheMinInputTokens(SimulatedModelCacheMinimumInputTokensDefault)
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

func IsValidSimulatedModelCacheEntriesPerScope(value int) bool {
	return value >= SimulatedModelCacheMinEntriesPerScope && value <= SimulatedModelCacheMaxEntriesPerScope
}

func SetSimulatedModelCacheEntriesPerScope(value int) int {
	if value < SimulatedModelCacheMinEntriesPerScope {
		value = SimulatedModelCacheDefaultEntriesPerScope
	}
	if value > SimulatedModelCacheMaxEntriesPerScope {
		value = SimulatedModelCacheMaxEntriesPerScope
	}
	simulatedModelCacheEntriesPerScope.Store(int64(value))
	return value
}

func GetSimulatedModelCacheEntriesPerScope() int {
	return int(simulatedModelCacheEntriesPerScope.Load())
}

func IsValidSimulatedModelCacheMinInputTokens(value int) bool {
	return value >= SimulatedModelCacheMinimumInputTokensMin && value <= SimulatedModelCacheMinimumInputTokensMax
}

func SetSimulatedModelCacheMinInputTokens(value int) int {
	if value < SimulatedModelCacheMinimumInputTokensMin {
		value = SimulatedModelCacheMinimumInputTokensDefault
	}
	if value > SimulatedModelCacheMinimumInputTokensMax {
		value = SimulatedModelCacheMinimumInputTokensMax
	}
	simulatedModelCacheMinInputTokens.Store(int64(value))
	return value
}

func GetSimulatedModelCacheMinInputTokens() int {
	return int(simulatedModelCacheMinInputTokens.Load())
}
