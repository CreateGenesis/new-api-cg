package videocompat

import (
	"runtime"
	"sync"
	"time"
)

type SystemSample struct {
	SampledAt        time.Time `json:"sampled_at"`
	Supported        bool      `json:"supported"`
	CPUAvailable     bool      `json:"cpu_available"`
	MemoryAvailable  bool      `json:"memory_available"`
	DiskAvailable    bool      `json:"disk_available"`
	CPUPercent       float64   `json:"cpu_percent"`
	MemoryPercent    float64   `json:"memory_percent"`
	EffectiveCPUs    float64   `json:"effective_cpus"`
	MemoryUsedBytes  uint64    `json:"memory_used_bytes"`
	MemoryLimitBytes uint64    `json:"memory_limit_bytes"`
	DiskFreeBytes    uint64    `json:"disk_free_bytes"`
	FFmpegAvailable  bool      `json:"ffmpeg_available"`
	FFprobeAvailable bool      `json:"ffprobe_available"`
	PrlimitAvailable bool      `json:"prlimit_available"`
}

var sampler struct {
	sync.Mutex
	sample        SystemSample
	previousCPU   uint64
	previousTime  time.Time
	previousTotal uint64
	previousIdle  uint64
}

func SampleSystem() SystemSample {
	sampler.Lock()
	defer sampler.Unlock()
	if !sampler.sample.SampledAt.IsZero() && time.Since(sampler.sample.SampledAt) < time.Second {
		return sampler.sample
	}
	s := SystemSample{SampledAt: time.Now(), EffectiveCPUs: float64(runtime.NumCPU())}
	samplePlatform(&s)
	sampler.sample = s
	return s
}
func GetSystemSample() SystemSample { return SampleSystem() }
