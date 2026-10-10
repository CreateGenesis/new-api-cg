// video-capacity exercises real FFmpeg workers and verifies that request-local
// memory, disk and pool slots return to zero after each round.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/videocompat"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

type roundResult struct {
	Tier                   string                         `json:"tier"`
	Round                  int                            `json:"round"`
	Requests               int                            `json:"requests"`
	Successes              int                            `json:"successes"`
	Errors                 []string                       `json:"errors,omitempty"`
	ElapsedSeconds         float64                        `json:"elapsed_seconds"`
	Throughput             float64                        `json:"throughput_requests_per_second"`
	P50MS                  int64                          `json:"p50_ms"`
	P95MS                  int64                          `json:"p95_ms"`
	PeakRunning            int                            `json:"peak_running"`
	PeakChildren           int                            `json:"peak_actual_media_children"`
	PeakChildRSSBytes      int64                          `json:"peak_combined_child_rss_bytes"`
	PeakWorkerRSSBytes     int64                          `json:"peak_sampled_worker_rss_bytes"`
	PeakProcessRSSBytes    int64                          `json:"peak_process_rss_bytes"`
	ProcessRSSAfterGCBytes int64                          `json:"process_rss_after_explicit_gc_bytes,omitempty"`
	PeakBufferBytes        int64                          `json:"peak_buffer_bytes"`
	PeakDiskBytes          int64                          `json:"peak_disk_bytes"`
	ChildCPUSeconds        float64                        `json:"child_cpu_seconds"`
	ChildAverageCores      float64                        `json:"child_average_cpu_cores"`
	ChildCPUPercent        float64                        `json:"child_cpu_percent_of_effective_cpus"`
	SystemCPUAvailable     bool                           `json:"system_cpu_available"`
	SystemCPUMean          float64                        `json:"system_cpu_percent_mean"`
	SystemCPUPeak          float64                        `json:"system_cpu_percent_peak"`
	OutputFrames           uint64                         `json:"output_frames"`
	JPEGBytes              int64                          `json:"jpeg_bytes"`
	BodyBytes              int64                          `json:"body_bytes"`
	Before                 videocompat.PoolStats          `json:"before"`
	Retained               videocompat.PoolStats          `json:"retained"`
	Drained                videocompat.PoolStats          `json:"drained"`
	Settings               dto.VideoCompatibilitySettings `json:"settings"`
}

func processRSS() int64 {
	data, _ := os.ReadFile("/proc/self/status")
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			fields := strings.Fields(value)
			if len(fields) > 0 {
				n, _ := strconv.ParseInt(fields[0], 10, 64)
				return n * 1024
			}
		}
	}
	return 0
}

func main() {
	fixture := flag.String("fixture", "", "video file; empty generates a temporary 10s 1280x720 30fps H264 testsrc2 fixture")
	tier := flag.String("tier", "all", "fixed quality profile: all, normal, light, heavy; adaptive switching is disabled")
	rounds := flag.Int("rounds", 3, "rounds per quality profile")
	concurrency := flag.Int("concurrency", 32, "simultaneous requests and configured media pool size (1-256)")
	output := flag.String("output", "", "optional aggregate JSON report path; JSON is also printed to stdout")
	full := flag.Bool("require-full-concurrency", true, "fail unless every round observes all requests and FFmpeg/ffprobe children running simultaneously")
	gc := flag.Bool("gc-after-round", false, "also report RSS after explicit runtime.GC; not a normal reclamation measurement")
	flag.Parse()
	if *rounds < 1 || *concurrency < 1 || *concurrency > 256 || !slices.Contains([]string{"all", "normal", "light", "heavy"}, *tier) {
		fmt.Fprintln(os.Stderr, "invalid rounds, concurrency or tier")
		os.Exit(1)
	}
	if *fixture == "" {
		dir, e := os.MkdirTemp("", "new-api-video-capacity-")
		if e != nil {
			panic(e)
		}
		defer os.RemoveAll(dir)
		*fixture = filepath.Join(dir, "720p30-10s.mp4")
		cmd := exec.Command("ffmpeg", "-hide_banner", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-t", "10", "-an", "-c:v", "libx264", "-crf", "22", "-preset", "veryfast", "-threads", "1", "-y", *fixture)
		if log, e := cmd.CombinedOutput(); e != nil {
			panic(fmt.Errorf("generate fixture: %w: %s", e, log))
		}
	}
	data, e := os.ReadFile(*fixture)
	if e != nil {
		panic(e)
	}
	raw := []byte(`{"model":"capacity-test","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + base64.StdEncoding.EncodeToString(data) + `"}}]}]}`)
	config := videocompat.DefaultConfig()
	config.MaxConcurrency = *concurrency
	if e = videocompat.SetConfig(config); e != nil {
		panic(e)
	}
	tiers := []string{*tier}
	if *tier == "all" {
		tiers = []string{"normal", "light", "heavy"}
	}
	all := make([]roundResult, 0, *rounds*len(tiers))
	valid := true
	for _, profile := range tiers {
		settings := dto.DefaultVideoCompatibilitySettings()
		settings.Enabled = true
		if profile == "light" {
			settings.FPS, settings.MaxFrames, settings.PixelPercent, settings.JPEGQuality = 6, 192, 50, 75
		}
		if profile == "heavy" {
			settings.FPS, settings.MaxFrames, settings.PixelPercent, settings.JPEGQuality = 4, 128, 25, 50
		}
		for round := range *rounds {
			r := roundResult{Tier: profile, Round: round + 1, Requests: *concurrency, Settings: settings, Before: videocompat.Stats()}
			sessions := make([]*videocompat.Session, *concurrency)
			storages := make([]common.BodyStorage, *concurrency)
			latencies := make([]int64, *concurrency)
			errors := make([]error, *concurrency)
			sizes := make([]int64, *concurrency)
			for i := range *concurrency {
				storages[i] = common.NewMemoryBodyStorage(bytes.Clone(raw))
				sessions[i], e = videocompat.NewStorageSession(context.Background(), storages[i], "chat")
				if e != nil {
					panic(e)
				}
			}
			start := make(chan struct{})
			done := make(chan struct{})
			var group sync.WaitGroup
			for i := range *concurrency {
				group.Go(func() {
					<-start
					began := time.Now()
					body, err := sessions[i].VariantStorage(settings)
					latencies[i], errors[i] = time.Since(began).Milliseconds(), err
					if err == nil {
						sizes[i] = body.Size()
					}
				})
			}
			began := time.Now()
			close(start)
			go func() { group.Wait(); close(done) }()
			ticker := time.NewTicker(50 * time.Millisecond)
			var cpuSampleAt time.Time
			cpuSamples := 0
			running := true
			for running {
				select {
				case <-done:
					running = false
				case <-ticker.C:
					s := videocompat.Stats()
					r.PeakRunning = max(r.PeakRunning, s.Running)
					r.PeakChildren = max(r.PeakChildren, s.CurrentChildren)
					r.PeakChildRSSBytes = max(r.PeakChildRSSBytes, s.CurrentChildRSSBytes)
					_, _, workerRSS := sampledWorkerMemory()
					r.PeakWorkerRSSBytes = max(r.PeakWorkerRSSBytes, workerRSS)
					r.PeakProcessRSSBytes = max(r.PeakProcessRSSBytes, processRSS())
					r.PeakBufferBytes = max(r.PeakBufferBytes, s.BufferBytes)
					r.PeakDiskBytes = max(r.PeakDiskBytes, s.DiskBytes)
					if s.System.CPUAvailable && s.System.SampledAt != cpuSampleAt {
						cpuSampleAt = s.System.SampledAt
						cpuSamples++
						cpuMean := s.System.CPUPercent
						r.SystemCPUMean += cpuMean
						r.SystemCPUPeak = max(r.SystemCPUPeak, cpuMean)
					}
				}
			}
			ticker.Stop()
			r.ElapsedSeconds = time.Since(began).Seconds()
			r.Retained = videocompat.Stats()
			r.ChildCPUSeconds = r.Retained.ChildCPUSeconds - r.Before.ChildCPUSeconds
			r.ChildAverageCores = r.ChildCPUSeconds / r.ElapsedSeconds
			r.ChildCPUPercent = 100 * r.ChildAverageCores / r.Retained.System.EffectiveCPUs
			if cpuSamples > 0 {
				r.SystemCPUAvailable = true
				r.SystemCPUMean /= float64(cpuSamples)
			}
			r.OutputFrames = r.Retained.OutputFrames - r.Before.OutputFrames
			r.JPEGBytes = r.Retained.GeneratedJPEGBytes - r.Before.GeneratedJPEGBytes
			r.PeakBufferBytes = max(r.PeakBufferBytes, r.Retained.BufferBytes)
			r.PeakDiskBytes = max(r.PeakDiskBytes, r.Retained.DiskBytes)
			for i, err := range errors {
				if err != nil {
					r.Errors = append(r.Errors, fmt.Sprintf("%d: %v", i, err))
				} else {
					r.Successes++
					r.BodyBytes += sizes[i]
				}
			}
			slices.Sort(latencies)
			r.P50MS, r.P95MS = latencies[(*concurrency*50+99)/100-1], latencies[(*concurrency*95+99)/100-1]
			r.Throughput = float64(r.Successes) / r.ElapsedSeconds
			for i := range *concurrency {
				sessions[i].Close()
				_ = storages[i].Close()
			}
			r.Drained = videocompat.Stats()
			if *gc {
				sessions, storages = nil, nil
				runtime.GC()
				r.ProcessRSSAfterGCBytes = processRSS()
			}
			all = append(all, r)
			fmt.Fprintf(os.Stderr, "%s round %d: %d/%d succeeded, %.2fs, P95 %dms, peak media children %d\n", profile, round+1, r.Successes, r.Requests, r.ElapsedSeconds, r.P95MS, r.PeakChildren)
			if r.Successes != *concurrency || (*full && (r.PeakRunning != *concurrency || r.PeakChildren != *concurrency)) || r.Drained.Running != 0 || r.Drained.Queued != 0 || r.Drained.BufferBytes != 0 || r.Drained.DiskBytes != 0 || r.Drained.CurrentChildren != 0 {
				valid = false
			}
		}
	}
	version, _ := exec.Command("ffmpeg", "-version").Output()
	cpuInfo, _ := os.ReadFile("/proc/cpuinfo")
	cpuModel := ""
	for line := range strings.SplitSeq(string(cpuInfo), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "model name" {
			cpuModel = strings.TrimSpace(value)
			break
		}
	}
	limits := make(map[string]string)
	for _, name := range []string{"cpu.max", "cpuset.cpus.effective", "memory.max", "memory.current"} {
		content, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", name))
		limits[name] = strings.TrimSpace(string(content))
	}
	report, e := common.Marshal(map[string]any{"passed": valid, "fixture": *fixture, "input_bytes": len(data), "runtime": runtime.Version(), "cpu_model": cpuModel, "cgroup": limits, "ffmpeg_version": strings.SplitN(string(version), "\n", 2)[0], "system_cpu_scope": "container or host, including other workloads; RSS peaks sampled every 50 ms", "adaptive_enabled": false, "config": config, "rounds": all})
	if e != nil {
		panic(e)
	}
	if *output != "" {
		if e = os.WriteFile(*output, report, 0600); e != nil {
			panic(e)
		}
	}
	fmt.Println(string(report))
	if !valid {
		os.Exit(2)
	}
}

// The pool's peak is cumulative across rounds. Sample the current process tree
// for a per-round single-worker peak without using inherited ru_maxrss.
func sampledWorkerMemory() (int64, int, int64) {
	entries, _ := os.ReadDir("/proc")
	var total, peak int64
	count := 0
	for _, entry := range entries {
		pid, e := strconv.Atoi(entry.Name())
		if e != nil || pid == os.Getpid() {
			continue
		}
		status, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		fields := strings.Fields(string(status))
		if len(fields) < 2 || (fields[1] != "ffmpeg" && fields[1] != "ffprobe") {
			continue
		}
		parent := 0
		for i, field := range fields {
			if field == "PPid:" && i+1 < len(fields) {
				parent, _ = strconv.Atoi(fields[i+1])
			}
		}
		if parent != os.Getpid() {
			continue
		}
		for i, field := range fields {
			if field == "VmRSS:" && i+1 < len(fields) {
				n, _ := strconv.ParseInt(fields[i+1], 10, 64)
				rss := n * 1024
				total += rss
				peak = max(peak, rss)
				count++
			}
		}
	}
	return total, count, peak
}
