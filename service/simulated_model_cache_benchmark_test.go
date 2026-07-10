package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

var simulatedModelCacheBenchmarkRatio float64

// Manual pprof scenario:
// go test ./service -run '^$' -bench BenchmarkSimulatedModelCacheFingerprintMatch100x250KRunes8Concurrent -benchtime=1x -memprofile simulated-cache.mem.pprof -cpuprofile simulated-cache.cpu.pprof
func BenchmarkSimulatedModelCacheFingerprintMatch100x250KRunes8Concurrent(b *testing.B) {
	ctx := context.Background()
	base := strings.Repeat("abcd", simulatedModelCacheMaxFingerprintRunes/4)
	current, err := buildSimulatedModelCachePromptFingerprint(ctx, base)
	if err != nil {
		b.Fatal(err)
	}
	matcher := newSimulatedModelCacheFingerprintMatcher(current)
	candidates := make([]simulatedModelCachePromptFingerprint, 100)
	for index := range candidates {
		suffix := fmt.Sprintf("%08d", index)
		prompt := base[:len(base)-len(suffix)] + suffix
		candidates[index], err = buildSimulatedModelCachePromptFingerprint(ctx, prompt)
		if err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		var waitGroup sync.WaitGroup
		results := [8]float64{}
		waitGroup.Add(8)
		for worker := 0; worker < 8; worker++ {
			go func(workerIndex int) {
				defer waitGroup.Done()
				best := float64(0)
				for _, candidate := range candidates {
					ratio := matcher.match(ctx, candidate)
					if ratio > best {
						best = ratio
					}
				}
				results[workerIndex] = best
			}(worker)
		}
		waitGroup.Wait()
		simulatedModelCacheBenchmarkRatio = results[0]
	}
}

func BenchmarkSimulatedModelCacheFineFingerprintMatch100x1024Runes8Concurrent(b *testing.B) {
	ctx := context.Background()
	base := strings.Repeat("abcd", simulatedModelCacheFineFingerprintMaxRunes/4)
	current, err := buildSimulatedModelCachePromptFingerprint(ctx, base)
	if err != nil {
		b.Fatal(err)
	}
	matcher := newSimulatedModelCacheFingerprintMatcher(current)
	candidates := make([]simulatedModelCachePromptFingerprint, 100)
	for index := range candidates {
		suffix := fmt.Sprintf("%08d", index)
		prompt := base[:len(base)-len(suffix)] + suffix
		candidates[index], err = buildSimulatedModelCachePromptFingerprint(ctx, prompt)
		if err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		var waitGroup sync.WaitGroup
		results := [8]float64{}
		waitGroup.Add(8)
		for worker := 0; worker < 8; worker++ {
			go func(workerIndex int) {
				defer waitGroup.Done()
				best := float64(0)
				for _, candidate := range candidates {
					ratio := matcher.match(ctx, candidate)
					if ratio > best {
						best = ratio
					}
				}
				results[workerIndex] = best
			}(worker)
		}
		waitGroup.Wait()
		simulatedModelCacheBenchmarkRatio = results[0]
	}
}
