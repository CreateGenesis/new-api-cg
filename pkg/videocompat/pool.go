package videocompat

import (
	"context"
	"sync"
	"time"
)

type PoolStats struct {
	PeakRunning               int          `json:"peak_running"`
	CurrentChildren           int          `json:"current_children"`
	PeakChildren              int          `json:"peak_children"`
	Timeouts                  uint64       `json:"timeouts"`
	Cancelled                 uint64       `json:"cancelled"`
	OutputFrames              uint64       `json:"output_frames"`
	DecodeCacheHits           uint64       `json:"decode_cache_hits"`
	DecodeCacheMisses         uint64       `json:"decode_cache_misses"`
	FrameCacheHits            uint64       `json:"frame_cache_hits"`
	FrameCacheMisses          uint64       `json:"frame_cache_misses"`
	Timing5m                  TimingWindow `json:"timing_5m"`
	CompletedPerMinute        int          `json:"completed_per_minute"`
	ConfigVersion             uint64       `json:"config_version"`
	Running                   int          `json:"running"`
	Queued                    int          `json:"queued"`
	QueuedBytes               int64        `json:"queued_bytes"`
	BufferBytes               int64        `json:"buffer_bytes"`
	DiskBytes                 int64        `json:"disk_bytes"`
	Accepted                  uint64       `json:"accepted"`
	Rejected                  uint64       `json:"rejected"`
	Completed                 uint64       `json:"completed"`
	Failed                    uint64       `json:"failed"`
	CacheHits                 uint64       `json:"cache_hits"`
	QueueWaitMilliseconds     int64        `json:"queue_wait_milliseconds"`
	ProcessingMilliseconds    int64        `json:"processing_milliseconds"`
	ChildCPUSeconds           float64      `json:"child_cpu_seconds"`
	PeakChildRSSBytes         int64        `json:"peak_child_rss_bytes"`
	CurrentChildRSSBytes      int64        `json:"current_child_rss_bytes"`
	PeakCombinedChildRSSBytes int64        `json:"peak_combined_child_rss_bytes"`
	GeneratedJPEGBytes        int64        `json:"generated_jpeg_bytes"`
	AdaptiveTier              int          `json:"adaptive_tier"`
	AdaptiveLight             uint64       `json:"adaptive_light"`
	AdaptiveHeavy             uint64       `json:"adaptive_heavy"`
	System                    SystemSample `json:"system"`
	Config                    Config       `json:"config"`
}

type TimingQuantiles struct {
	P50MS int64 `json:"p50_ms"`
	P95MS int64 `json:"p95_ms"`
}
type TimingWindow struct {
	Queue   TimingQuantiles `json:"queue"`
	Decode  TimingQuantiles `json:"decode"`
	Extract TimingQuantiles `json:"extract"`
	Total   TimingQuantiles `json:"total"`
}

// Fixed time buckets keep all observations in the five-minute window without
// per-request metrics allocations. Quantiles report the bucket's upper bound.
var timingBounds = [...]int64{0, 1, 2, 3, 4, 5, 8, 10, 15, 20, 25, 30, 40, 50, 75, 100, 150, 200, 250, 300, 400, 500, 750, 1000, 1500, 2000, 2500, 3000, 4000, 5000, 7500, 10000, 15000, 20000, 25000, 30000, 40000, 50000, 60000, 120000, 180000, 240000, 300000}

type timingSlot struct {
	second    int64
	counts    [4][len(timingBounds)]uint64
	completed uint64
}

type ticket struct{ bytes int64 }
type pool struct {
	mu           sync.Mutex
	changed      chan struct{}
	queue        []*ticket
	stats        PoolStats
	recovery     time.Time
	timings      [300]timingSlot
	children     map[int]struct{}
	activeLimits map[Config]int
}

var globalPool = pool{changed: make(chan struct{})}

func (p *pool) signal() { close(p.changed); p.changed = make(chan struct{}) }
func (p *pool) ready(c Config) bool {
	for limits, count := range p.activeLimits {
		if count > 0 && (limits.BufferMemoryMB > c.BufferMemoryMB || limits.TempDiskMB > c.TempDiskMB) {
			return false
		}
	}
	return p.stats.Running < c.MaxConcurrency && p.stats.BufferBytes <= int64(c.BufferMemoryMB)*MiB && p.stats.DiskBytes <= int64(c.TempDiskMB)*MiB
}
func (p *pool) recordTier(tier int) {
	if tier == 1 {
		p.stats.AdaptiveLight++
	}
	if tier == 2 {
		p.stats.AdaptiveHeavy++
	}
}

func (p *pool) admit(ctx context.Context, size int64, frozen ...Config) (int, Config, error) {
	started := time.Now()
	c := GetConfig()
	t := &ticket{size}
	timer := time.NewTimer(time.Duration(c.QueueWaitSeconds) * time.Second)
	defer timer.Stop()
	p.mu.Lock()
	c = GetConfig()
	if e := ctx.Err(); e != nil {
		p.mu.Unlock()
		if e == context.DeadlineExceeded {
			return 0, c, failure(504, "video_processing_timeout", "video preprocessing timed out")
		}
		return 0, c, failure(499, "video_cancelled", "video processing cancelled")
	}
	if len(p.queue) == 0 && p.ready(c) {
		p.stats.Running++
		p.stats.PeakRunning = max(p.stats.PeakRunning, p.stats.Running)
		p.stats.Accepted++
		tier := p.pressure(c)
		p.recordLocked("queue", time.Since(started))
		if len(frozen) > 0 {
			c = frozen[0]
		}
		if p.activeLimits == nil {
			p.activeLimits = make(map[Config]int)
		}
		p.activeLimits[c]++
		p.mu.Unlock()
		return tier, c, nil
	}
	if len(p.queue) >= c.QueueCapacity || size > int64(c.QueueBytesMB)*MiB-p.stats.QueuedBytes {
		p.stats.Rejected++
		p.mu.Unlock()
		return 0, c, failure(503, "video_queue_full", "video processing queue is full")
	}
	p.queue = append(p.queue, t)
	p.stats.Queued = len(p.queue)
	p.stats.QueuedBytes += size
	for {
		c = GetConfig()
		if p.queue[0] == t && p.ready(c) {
			p.queue = p.queue[1:]
			p.stats.Queued = len(p.queue)
			p.stats.QueuedBytes -= size
			p.stats.Running++
			p.stats.PeakRunning = max(p.stats.PeakRunning, p.stats.Running)
			p.stats.Accepted++
			p.stats.QueueWaitMilliseconds += time.Since(started).Milliseconds()
			p.recordLocked("queue", time.Since(started))
			tier := p.pressure(c)
			p.signal()
			if len(frozen) > 0 {
				c = frozen[0]
			}
			if p.activeLimits == nil {
				p.activeLimits = make(map[Config]int)
			}
			p.activeLimits[c]++
			p.mu.Unlock()
			return tier, c, nil
		}
		changed := p.changed
		p.mu.Unlock()
		var err error
		select {
		case <-changed:
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				err = failure(504, "video_processing_timeout", "video preprocessing timed out")
			} else {
				err = failure(499, "video_cancelled", "video processing cancelled")
			}
		case <-timer.C:
			err = failure(503, "video_queue_timeout", "video processing queue wait timed out")
		}
		p.mu.Lock()
		if err != nil {
			for i, q := range p.queue {
				if q == t {
					p.queue = append(p.queue[:i], p.queue[i+1:]...)
					break
				}
			}
			p.stats.Queued = len(p.queue)
			p.stats.QueuedBytes -= size
			p.stats.Rejected++
			if mediaError, ok := err.(*Error); ok {
				if mediaError.Code == "video_queue_timeout" || mediaError.Code == "video_processing_timeout" {
					p.stats.Timeouts++
				}
				if mediaError.Code == "video_cancelled" {
					p.stats.Cancelled++
				}
			}
			p.signal()
			p.mu.Unlock()
			return 0, c, err
		}
	}
}

func (p *pool) finish(start time.Time, err error, limits Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats.Running--
	p.activeLimits[limits]--
	if p.activeLimits[limits] <= 0 {
		delete(p.activeLimits, limits)
	}
	p.stats.ProcessingMilliseconds += time.Since(start).Milliseconds()
	if err == nil {
		p.stats.Completed++
		p.recordLocked("completed", 0)
	} else {
		p.stats.Failed++
		if e, ok := err.(*Error); ok {
			if e.Code == "video_processing_timeout" {
				p.stats.Timeouts++
			}
			if e.Code == "video_cancelled" {
				p.stats.Cancelled++
			}
		}
	}
	p.recordLocked("total", time.Since(start))
	p.signal()
}
func (p *pool) reserve(memory bool, bytes int64) error {
	return p.reserveWithConfig(memory, bytes, GetConfig())
}
func (p *pool) reserveWithConfig(memory bool, bytes int64, c Config) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	used, limit := p.stats.DiskBytes, int64(c.TempDiskMB)*MiB
	if memory {
		used, limit = p.stats.BufferBytes, int64(c.BufferMemoryMB)*MiB
	}
	if bytes > 0 && bytes > limit-used {
		return failure(503, "video_resource_busy", "video processing shared byte budget exhausted")
	}
	if memory {
		p.stats.BufferBytes = max(0, used+bytes)
	} else {
		p.stats.DiskBytes = max(0, used+bytes)
	}
	if bytes < 0 {
		p.signal()
	}
	return nil
}

func (p *pool) pressure(c Config) int {
	if !c.AdaptiveEnabled {
		p.stats.AdaptiveTier = 0
		p.recovery = time.Time{}
		return 0
	}
	sample := SampleSystem()
	run := 100 * p.stats.Running / c.MaxConcurrency
	queue := 0
	if c.QueueCapacity > 0 {
		queue = 100 * len(p.queue) / c.QueueCapacity
	}
	a := c.AdaptiveConfig
	candidate := 0
	for i, l := range []AdaptiveLevel{a.Light, a.Heavy} {
		if (sample.CPUAvailable && sample.CPUPercent >= float64(l.CPUThreshold)) || (sample.MemoryAvailable && sample.MemoryPercent >= float64(l.MemoryThreshold)) || run >= l.RunningThreshold || queue >= l.QueueThreshold {
			candidate = i + 1
		}
	}
	if candidate > p.stats.AdaptiveTier {
		p.stats.AdaptiveTier = candidate
		p.recovery = time.Time{}
	}
	if candidate < p.stats.AdaptiveTier {
		l := a.Light
		if p.stats.AdaptiveTier == 2 {
			l = a.Heavy
		}
		clear := (!sample.CPUAvailable || sample.CPUPercent < float64(max(0, l.CPUThreshold-10))) && (!sample.MemoryAvailable || sample.MemoryPercent < float64(max(0, l.MemoryThreshold-10))) && run < max(0, l.RunningThreshold-10) && queue < max(0, l.QueueThreshold-10)
		if clear {
			if p.recovery.IsZero() {
				p.recovery = time.Now()
			}
			if time.Since(p.recovery) >= time.Duration(a.RecoverySeconds)*time.Second {
				p.stats.AdaptiveTier = candidate
				p.recovery = time.Time{}
			}
		} else {
			p.recovery = time.Time{}
		}
	} else {
		p.recovery = time.Time{}
	}
	tier := p.stats.AdaptiveTier
	return tier
}

func (p *pool) recordLocked(phase string, d time.Duration) {
	now := time.Now().Unix()
	slot := &p.timings[now%300]
	if slot.second != now {
		*slot = timingSlot{second: now}
	}
	if phase == "completed" {
		slot.completed++
		return
	}
	index := 0
	switch phase {
	case "decode":
		index = 1
	case "extract":
		index = 2
	case "total":
		index = 3
	}
	bucket := len(timingBounds) - 1
	for i, bound := range timingBounds {
		if d.Milliseconds() <= bound {
			bucket = i
			break
		}
	}
	slot.counts[index][bucket]++
}
func (p *pool) recordPhase(phase string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recordLocked(phase, d)
}
func histogramQuantiles(counts [len(timingBounds)]uint64) TimingQuantiles {
	var total uint64
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return TimingQuantiles{}
	}
	p50, p95 := (total*50+99)/100, (total*95+99)/100
	var accumulated uint64
	out := TimingQuantiles{P50MS: -1}
	for i, n := range counts {
		accumulated += n
		if out.P50MS < 0 && accumulated >= p50 {
			out.P50MS = timingBounds[i]
		}
		if accumulated >= p95 {
			out.P95MS = timingBounds[i]
			break
		}
	}
	return out
}
func Stats() PoolStats {
	sample := SampleSystem()
	globalPool.mu.Lock()
	defer globalPool.mu.Unlock()
	globalPool.pressure(GetConfig())
	pids := make([]int, 0, len(globalPool.children))
	for pid := range globalPool.children {
		pids = append(pids, pid)
	}
	combinedRSS, childCount, workerRSS := runtimeChildStats(pids)
	globalPool.stats.CurrentChildRSSBytes, globalPool.stats.CurrentChildren = combinedRSS, childCount
	globalPool.stats.PeakChildRSSBytes = max(globalPool.stats.PeakChildRSSBytes, workerRSS)
	globalPool.stats.PeakChildren = max(globalPool.stats.PeakChildren, globalPool.stats.CurrentChildren)
	globalPool.stats.PeakCombinedChildRSSBytes = max(globalPool.stats.PeakCombinedChildRSSBytes, globalPool.stats.CurrentChildRSSBytes)
	s := globalPool.stats
	s.System = sample
	snapshot := currentConfig.Load()
	s.Config = snapshot.Config
	s.ConfigVersion = snapshot.Version
	now := time.Now().Unix()
	var counts [4][len(timingBounds)]uint64
	for _, slot := range globalPool.timings {
		if slot.second <= now-300 {
			continue
		}
		for i, phase := range slot.counts {
			for j, n := range phase {
				counts[i][j] += n
			}
		}
		if slot.second > now-60 {
			s.CompletedPerMinute += int(slot.completed)
		}
	}
	s.Timing5m = TimingWindow{histogramQuantiles(counts[0]), histogramQuantiles(counts[1]), histogramQuantiles(counts[2]), histogramQuantiles(counts[3])}
	return s
}
