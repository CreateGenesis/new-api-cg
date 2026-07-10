package service

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

var ErrSimulatedModelCacheMemoryBudget = errors.New("simulated model cache memory budget is exhausted")

const (
	simulatedModelCacheDefaultResponseBufferMB = 8
	simulatedModelCacheMaxMatchWorkers         = 64
	simulatedModelCacheMaxResponseBufferMB     = 1024
	simulatedModelCacheFineMatchBytesPerWindow = 1536
)

const (
	SimulatedModelCacheBypassMemoryBudget     = "memory_budget"
	SimulatedModelCacheBypassWorkerQueueFull  = "worker_queue_full"
	SimulatedModelCacheBypassRequestCanceled  = "request_canceled"
	SimulatedModelCacheBypassRedisError       = "redis_error"
	SimulatedModelCacheBypassRedisUnavailable = "redis_unavailable"
	SimulatedModelCacheBypassPromptTooLarge   = "prompt_too_large"
	SimulatedModelCacheBypassMatchNotReady    = "match_not_ready"
	SimulatedModelCacheBypassResponseTooLarge = "response_too_large"
	SimulatedModelCacheBypassResponseBuffer   = "response_buffer_unavailable"
)

type simulatedModelCacheMemoryBudget struct {
	used atomic.Int64
}

type SimulatedModelCacheMemoryReservation struct {
	budget   *simulatedModelCacheMemoryBudget
	bytes    int64
	released atomic.Bool
}

func (b *simulatedModelCacheMemoryBudget) reserve(bytes int64, limit int64) *SimulatedModelCacheMemoryReservation {
	if bytes <= 0 {
		return &SimulatedModelCacheMemoryReservation{}
	}
	for {
		used := b.used.Load()
		if bytes > limit-used {
			return nil
		}
		if b.used.CompareAndSwap(used, used+bytes) {
			return &SimulatedModelCacheMemoryReservation{budget: b, bytes: bytes}
		}
	}
}

func (r *SimulatedModelCacheMemoryReservation) Release() {
	if r == nil || r.budget == nil || !r.released.CompareAndSwap(false, true) {
		return
	}
	r.budget.used.Add(-r.bytes)
}

type simulatedModelCacheRuntime struct {
	memoryBudget       *simulatedModelCacheMemoryBudget
	responseBufferSize int64
	workers            int
	poolOnce           sync.Once
	pool               *simulatedModelCacheMatchPool
}

var simulatedModelCacheRuntimeOnce sync.Once
var simulatedModelCacheRuntimeValue *simulatedModelCacheRuntime

func getSimulatedModelCacheRuntime() *simulatedModelCacheRuntime {
	simulatedModelCacheRuntimeOnce.Do(func() {
		workers := runtime.GOMAXPROCS(0)
		if workers > 8 {
			workers = 8
		}
		workers = common.GetEnvOrDefault("SIMULATED_MODEL_CACHE_MATCH_WORKERS", workers)
		if workers < 1 {
			workers = 1
		}
		if workers > simulatedModelCacheMaxMatchWorkers {
			workers = simulatedModelCacheMaxMatchWorkers
		}

		responseBufferMB := common.GetEnvOrDefault("SIMULATED_MODEL_CACHE_RESPONSE_BUFFER_MB", simulatedModelCacheDefaultResponseBufferMB)
		if responseBufferMB < 1 {
			responseBufferMB = simulatedModelCacheDefaultResponseBufferMB
		}
		if responseBufferMB > simulatedModelCacheMaxResponseBufferMB {
			responseBufferMB = simulatedModelCacheMaxResponseBufferMB
		}

		responseBufferBytes := int64(responseBufferMB) * 1024 * 1024
		simulatedModelCacheRuntimeValue = &simulatedModelCacheRuntime{
			memoryBudget:       &simulatedModelCacheMemoryBudget{},
			responseBufferSize: responseBufferBytes,
			workers:            workers,
		}
	})
	return simulatedModelCacheRuntimeValue
}

func ReserveSimulatedModelCacheMemory(bytes int64) *SimulatedModelCacheMemoryReservation {
	return getSimulatedModelCacheRuntime().memoryBudget.reserve(bytes, common.GetSimulatedModelCacheMemoryBudgetBytes())
}

func SimulatedModelCacheResponseBufferBytes() int64 {
	responseBufferBytes := getSimulatedModelCacheRuntime().responseBufferSize
	memoryBudgetBytes := common.GetSimulatedModelCacheMemoryBudgetBytes()
	if responseBufferBytes > memoryBudgetBytes {
		return memoryBudgetBytes
	}
	return responseBufferBytes
}

type SimulatedModelCachePartialMatchResult struct {
	Match SimulatedModelCachePartialMatch
	Err   error
}

type SimulatedModelCachePartialMatchHandle struct {
	result <-chan SimulatedModelCachePartialMatchResult
	cancel context.CancelFunc
}

func (h *SimulatedModelCachePartialMatchHandle) TryResult() (SimulatedModelCachePartialMatchResult, bool) {
	if h == nil || h.result == nil {
		return SimulatedModelCachePartialMatchResult{}, false
	}
	select {
	case result := <-h.result:
		return result, true
	default:
		return SimulatedModelCachePartialMatchResult{}, false
	}
}

func (h *SimulatedModelCachePartialMatchHandle) Cancel() {
	if h != nil && h.cancel != nil {
		h.cancel()
	}
}

type simulatedModelCacheMatchJob struct {
	ctx         context.Context
	req         SimulatedModelCachePartialMatchRequest
	result      chan<- SimulatedModelCachePartialMatchResult
	reservation *SimulatedModelCacheMemoryReservation
}

type simulatedModelCacheMatchPool struct {
	queue chan simulatedModelCacheMatchJob
}

func (p *simulatedModelCacheMatchPool) trySubmit(job simulatedModelCacheMatchJob) bool {
	select {
	case p.queue <- job:
		return true
	default:
		return false
	}
}

func newSimulatedModelCacheMatchPool(workers int) *simulatedModelCacheMatchPool {
	if workers < 1 {
		workers = 1
	}
	pool := &simulatedModelCacheMatchPool{
		queue: make(chan simulatedModelCacheMatchJob, workers*2),
	}
	for i := 0; i < workers; i++ {
		go pool.worker()
	}
	return pool
}

func (p *simulatedModelCacheMatchPool) worker() {
	for job := range p.queue {
		result := SimulatedModelCachePartialMatchResult{}
		if err := job.ctx.Err(); err != nil {
			result.Match.BypassReason = SimulatedModelCacheBypassRequestCanceled
			result.Err = err
		} else {
			result.Match, result.Err = runSimulatedModelCachePartialMatch(job.ctx, job.req)
		}
		job.reservation.Release()
		select {
		case job.result <- result:
		case <-job.ctx.Done():
		}
	}
}

func SubmitSimulatedModelCachePartialMatch(ctx context.Context, req SimulatedModelCachePartialMatchRequest) (*SimulatedModelCachePartialMatchHandle, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, SimulatedModelCacheBypassRequestCanceled
	}
	estimatedBytes := estimateSimulatedModelCacheCurrentMatchBytes(req.PromptText)
	reservation := ReserveSimulatedModelCacheMemory(estimatedBytes)
	if reservation == nil {
		return nil, SimulatedModelCacheBypassMemoryBudget
	}

	runtimeConfig := getSimulatedModelCacheRuntime()
	runtimeConfig.poolOnce.Do(func() {
		runtimeConfig.pool = newSimulatedModelCacheMatchPool(runtimeConfig.workers)
	})
	matchCtx, cancel := context.WithCancel(ctx)
	result := make(chan SimulatedModelCachePartialMatchResult, 1)
	job := simulatedModelCacheMatchJob{
		ctx:         matchCtx,
		result:      result,
		reservation: reservation,
	}
	req.currentMemoryReserved = true
	job.req = req
	if runtimeConfig.pool.trySubmit(job) {
		return &SimulatedModelCachePartialMatchHandle{result: result, cancel: cancel}, ""
	}
	cancel()
	reservation.Release()
	return nil, SimulatedModelCacheBypassWorkerQueueFull
}

func estimateSimulatedModelCacheCurrentMatchBytes(prompt string) int64 {
	runeCount := utf8.RuneCountInString(prompt)
	chunkCount := runeCount/simulatedModelCacheFingerprintMinRunes + 1
	estimatedBytes := int64(len(prompt)) + int64(chunkCount)*768 + 64*1024
	if runeCount >= simulatedModelCacheFineFingerprintWindowRunes && runeCount <= simulatedModelCacheFineFingerprintMaxRunes {
		windowCount := runeCount - simulatedModelCacheFineFingerprintWindowRunes + 1
		estimatedBytes += int64(windowCount) * (simulatedModelCacheFineFingerprintHashBytes + simulatedModelCacheFineMatchBytesPerWindow)
	}
	return estimatedBytes
}
