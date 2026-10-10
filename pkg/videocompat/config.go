// Package videocompat provides bounded, request-local inline video preprocessing.
package videocompat

import (
	"fmt"
	"math"
	"sync/atomic"
)

const MiB int64 = 1 << 20

type AdaptiveLevel struct {
	PixelMultiplierPercent  int `json:"pixel_multiplier_percent"`
	FPSMultiplierPercent    int `json:"fps_multiplier_percent"`
	FramesMultiplierPercent int `json:"frames_multiplier_percent"`
	JPEGQualityCap          int `json:"jpeg_quality_cap"`
	CPUThreshold            int `json:"cpu_threshold"`
	MemoryThreshold         int `json:"memory_threshold"`
	RunningThreshold        int `json:"running_threshold"`
	QueueThreshold          int `json:"queue_threshold"`
}

type AdaptiveConfig struct {
	Light               AdaptiveLevel `json:"light"`
	Heavy               AdaptiveLevel `json:"heavy"`
	MinimumFPS          float64       `json:"minimum_fps"`
	MinimumFrames       int           `json:"minimum_frames"`
	MinimumPixelPercent int           `json:"minimum_pixel_percent"`
	MinimumJPEGQuality  int           `json:"minimum_jpeg_quality"`
	RecoverySeconds     int           `json:"recovery_seconds"`
}

type Config struct {
	MaxConcurrency        int            `json:"max_concurrency"`
	QueueCapacity         int            `json:"queue_capacity"`
	QueueBytesMB          int            `json:"queue_bytes_mb"`
	QueueWaitSeconds      int            `json:"queue_wait_seconds"`
	RequestTimeoutSeconds int            `json:"request_timeout_seconds"`
	FFmpegThreads         int            `json:"ffmpeg_threads"`
	WorkerMemoryMB        int            `json:"worker_memory_mb"`
	BufferMemoryMB        int            `json:"buffer_memory_mb"`
	TempDiskMB            int            `json:"temp_disk_mb"`
	AdaptiveEnabled       bool           `json:"adaptive_enabled"`
	AdaptiveConfig        AdaptiveConfig `json:"adaptive_config"`
}

func DefaultConfig() Config {
	return Config{MaxConcurrency: 32, QueueCapacity: 128, QueueBytesMB: 512, QueueWaitSeconds: 15, RequestTimeoutSeconds: 60, FFmpegThreads: 1, WorkerMemoryMB: 1024, BufferMemoryMB: 2048, TempDiskMB: 4096,
		AdaptiveConfig: AdaptiveConfig{Light: AdaptiveLevel{50, 75, 75, 75, 70, 75, 75, 25}, Heavy: AdaptiveLevel{25, 50, 50, 50, 85, 85, 90, 50}, MinimumFPS: .5, MinimumFrames: 16, MinimumPixelPercent: 25, MinimumJPEGQuality: 40, RecoverySeconds: 30}}
}

func (c Config) Validate() error {
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 256 || c.QueueCapacity < 0 || c.QueueCapacity > 4096 || c.QueueBytesMB < 1 || c.QueueBytesMB > 65536 || c.QueueWaitSeconds < 1 || c.QueueWaitSeconds > 120 || c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 300 || c.FFmpegThreads < 1 || c.FFmpegThreads > 16 || c.WorkerMemoryMB < 256 || c.WorkerMemoryMB > 8192 || c.BufferMemoryMB < 64 || c.BufferMemoryMB > 65536 || c.TempDiskMB < 16 || c.TempDiskMB > 1048576 {
		return fmt.Errorf("invalid video processing resource limits")
	}
	for _, l := range []AdaptiveLevel{c.AdaptiveConfig.Light, c.AdaptiveConfig.Heavy} {
		for _, n := range []int{l.PixelMultiplierPercent, l.FPSMultiplierPercent, l.FramesMultiplierPercent, l.JPEGQualityCap, l.CPUThreshold, l.MemoryThreshold, l.RunningThreshold, l.QueueThreshold} {
			if n < 1 || n > 100 {
				return fmt.Errorf("adaptive percentages must be 1–100")
			}
		}
	}
	a := c.AdaptiveConfig
	if math.IsNaN(a.MinimumFPS) || math.IsInf(a.MinimumFPS, 0) || a.MinimumFPS < .1 || a.MinimumFPS > 8 || math.Abs(a.MinimumFPS*10-math.Round(a.MinimumFPS*10)) > 1e-8 || a.MinimumFrames < 1 || a.MinimumFrames > 512 || a.MinimumPixelPercent < 1 || a.MinimumPixelPercent > 100 || a.MinimumJPEGQuality < 1 || a.MinimumJPEGQuality > 100 || a.RecoverySeconds < 1 || a.RecoverySeconds > 300 {
		return fmt.Errorf("invalid adaptive minimum or recovery settings")
	}
	if a.Heavy.CPUThreshold < a.Light.CPUThreshold || a.Heavy.MemoryThreshold < a.Light.MemoryThreshold || a.Heavy.RunningThreshold < a.Light.RunningThreshold || a.Heavy.QueueThreshold < a.Light.QueueThreshold || a.Heavy.PixelMultiplierPercent > a.Light.PixelMultiplierPercent || a.Heavy.FPSMultiplierPercent > a.Light.FPSMultiplierPercent || a.Heavy.FramesMultiplierPercent > a.Light.FramesMultiplierPercent || a.Heavy.JPEGQualityCap > a.Light.JPEGQualityCap {
		return fmt.Errorf("heavy adaptive level must be stronger than light")
	}
	return nil
}

type configSnapshot struct {
	Config  Config
	Version uint64
}

var currentConfig atomic.Pointer[configSnapshot]

func init()             { currentConfig.Store(&configSnapshot{Config: DefaultConfig(), Version: 1}) }
func GetConfig() Config { return currentConfig.Load().Config }
func SetConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	globalPool.mu.Lock()
	defer globalPool.mu.Unlock()
	previous := currentConfig.Load()
	if previous.Config == c {
		return nil
	}
	currentConfig.Store(&configSnapshot{Config: c, Version: previous.Version + 1})
	globalPool.signal()
	return nil
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *Error) Error() string                       { return e.Message }
func failure(status int, code, message string) error { return &Error{status, code, message} }
