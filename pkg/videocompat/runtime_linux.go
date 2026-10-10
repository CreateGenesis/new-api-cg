//go:build linux

package videocompat

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ffmpegBinary, ffprobeBinary, prlimitBinary string

func binaryPath(name string) string {
	p, e := exec.LookPath(name)
	if e != nil {
		return ""
	}
	p, e = filepath.Abs(p)
	if e != nil {
		return ""
	}
	return p
}
func init() {
	ffmpegBinary = binaryPath("ffmpeg")
	ffprobeBinary = binaryPath("ffprobe")
	prlimitBinary = binaryPath("prlimit")
}

func cgroupFile(name string) []byte {
	if b, e := os.ReadFile(filepath.Join("/sys/fs/cgroup", name)); e == nil {
		return b
	}
	b, _ := os.ReadFile("/proc/self/cgroup")
	for line := range strings.SplitSeq(string(b), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			b, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/"), name))
			return b
		}
	}
	return nil
}

func cgroupV1File(controller, name string) []byte {
	groups, _ := os.ReadFile("/proc/self/cgroup")
	groupPath := ""
	for line := range strings.SplitSeq(string(groups), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 {
			for candidate := range strings.SplitSeq(fields[1], ",") {
				if candidate == controller {
					groupPath = fields[2]
					break
				}
			}
		}
	}
	if groupPath == "" {
		return nil
	}
	mounts, _ := os.ReadFile("/proc/self/mountinfo")
	for line := range strings.SplitSeq(string(mounts), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		mount, properties := strings.Fields(before), strings.Fields(after)
		if len(mount) < 5 || len(properties) < 3 || properties[0] != "cgroup" {
			continue
		}
		for option := range strings.SplitSeq(properties[2], ",") {
			if option != controller {
				continue
			}
			relative := groupPath
			if mount[3] != "/" {
				if groupPath == mount[3] {
					relative = "/"
				} else if path, ok := strings.CutPrefix(groupPath, mount[3]+"/"); ok {
					relative = path
				} else {
					continue
				}
			}
			data, _ := os.ReadFile(filepath.Join(mount[4], strings.TrimPrefix(relative, "/"), name))
			return data
		}
	}
	return nil
}

func samplePlatform(s *SystemSample) {
	s.Supported = true
	s.FFmpegAvailable = ffmpegBinary != ""
	s.FFprobeAvailable = ffprobeBinary != ""
	s.PrlimitAvailable = prlimitBinary != ""
	quota := strings.Fields(string(cgroupFile("cpu.max")))
	if len(quota) != 2 {
		quota = []string{strings.TrimSpace(string(cgroupV1File("cpu", "cpu.cfs_quota_us"))), strings.TrimSpace(string(cgroupV1File("cpu", "cpu.cfs_period_us")))}
	}
	if len(quota) == 2 && quota[0] != "max" {
		q, qe := strconv.ParseFloat(quota[0], 64)
		p, pe := strconv.ParseFloat(quota[1], 64)
		if qe == nil && pe == nil && q > 0 && p > 0 {
			s.EffectiveCPUs = min(s.EffectiveCPUs, q/p)
		}
	}
	var usage uint64
	for line := range strings.SplitSeq(string(cgroupFile("cpu.stat")), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			usage, _ = strconv.ParseUint(v, 10, 64)
		}
	}
	if usage == 0 {
		if ns, e := strconv.ParseUint(strings.TrimSpace(string(cgroupV1File("cpuacct", "cpuacct.usage"))), 10, 64); e == nil {
			usage = ns / 1000
		}
	}
	if usage > 0 {
		if !sampler.previousTime.IsZero() && usage >= sampler.previousCPU {
			s.CPUPercent = min(100, float64(usage-sampler.previousCPU)/(time.Since(sampler.previousTime).Seconds()*1e6*s.EffectiveCPUs)*100)
			s.CPUAvailable = true
		}
		sampler.previousCPU = usage
		sampler.previousTime = time.Now()
	} else {
		b, _ := os.ReadFile("/proc/stat")
		fields := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
		if len(fields) >= 5 {
			var total, idle uint64
			for i, v := range fields[1:] {
				n, _ := strconv.ParseUint(v, 10, 64)
				total += n
				if i == 3 || i == 4 {
					idle += n
				}
			}
			if total > sampler.previousTotal && sampler.previousTotal > 0 {
				s.CPUPercent = 100 * (1 - float64(idle-sampler.previousIdle)/float64(total-sampler.previousTotal))
				s.CPUAvailable = true
			}
			sampler.previousTotal = total
			sampler.previousIdle = idle
		}
	}
	used, ue := strconv.ParseUint(strings.TrimSpace(string(cgroupFile("memory.current"))), 10, 64)
	limit, le := strconv.ParseUint(strings.TrimSpace(string(cgroupFile("memory.max"))), 10, 64)
	if ue != nil || le != nil {
		used, ue = strconv.ParseUint(strings.TrimSpace(string(cgroupV1File("memory", "memory.usage_in_bytes"))), 10, 64)
		limit, le = strconv.ParseUint(strings.TrimSpace(string(cgroupV1File("memory", "memory.limit_in_bytes"))), 10, 64)
	}
	if ue == nil && le == nil && limit > 0 && limit < 1<<60 {
		s.MemoryUsedBytes = used
		s.MemoryLimitBytes = limit
		s.MemoryPercent = min(100, float64(used)/float64(limit)*100)
		s.MemoryAvailable = true
	} else {
		b, _ := os.ReadFile("/proc/meminfo")
		var total, available uint64
		for line := range strings.SplitSeq(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			n, _ := strconv.ParseUint(f[1], 10, 64)
			if f[0] == "MemTotal:" {
				total = n * 1024
			}
			if f[0] == "MemAvailable:" {
				available = n * 1024
			}
		}
		if total > 0 && available <= total {
			s.MemoryLimitBytes = total
			s.MemoryUsedBytes = total - available
			s.MemoryPercent = float64(total-available) / float64(total) * 100
			s.MemoryAvailable = true
		}
	}
	var disk syscall.Statfs_t
	if syscall.Statfs(os.TempDir(), &disk) == nil {
		s.DiskAvailable = true
		s.DiskFreeBytes = disk.Bavail * uint64(disk.Bsize)
	}
}

func runMedia(ctx context.Context, c Config, binary string, args []string, read func(io.Reader) error) (string, error) {
	parentContext := ctx
	if binary == "" || prlimitBinary == "" {
		return "", failure(503, "video_runtime_unavailable", "video processing requires ffmpeg, ffprobe and prlimit on Linux")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limits := []string{"--as=" + strconv.FormatInt(int64(c.WorkerMemoryMB)*MiB, 10), "--cpu=" + strconv.Itoa(c.RequestTimeoutSeconds+1), "--nofile=64", "--core=0", "--", binary}
	limits = append(limits, args...)
	cmd := exec.CommandContext(ctx, prlimitBinary, limits...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out, e := cmd.StdoutPipe()
	if e != nil {
		return "", e
	}
	stderr, e := cmd.StderrPipe()
	if e != nil {
		return "", e
	}
	if e = cmd.Start(); e != nil {
		return "", failure(503, "video_runtime_unavailable", "video media worker could not start")
	}
	globalPool.mu.Lock()
	if globalPool.children == nil {
		globalPool.children = make(map[int]struct{})
	}
	globalPool.children[cmd.Process.Pid] = struct{}{}
	globalPool.mu.Unlock()
	type logResult struct {
		data []byte
		err  error
	}
	logs := make(chan logResult, 1)
	go func() {
		b, e := io.ReadAll(io.LimitReader(stderr, MiB+1))
		if len(b) > int(MiB) {
			e = failure(413, "video_stderr_limit", "video media diagnostics exceeded budget")
			cancel()
		}
		logs <- logResult{b, e}
	}()
	readErr := read(out)
	if readErr != nil {
		cancel()
	}
	log := <-logs
	waitErr := cmd.Wait()
	globalPool.mu.Lock()
	delete(globalPool.children, cmd.Process.Pid)
	if cmd.ProcessState != nil {
		globalPool.stats.ChildCPUSeconds += cmd.ProcessState.UserTime().Seconds() + cmd.ProcessState.SystemTime().Seconds()
	}
	globalPool.mu.Unlock()
	if parentContext.Err() != nil {
		if parentContext.Err() == context.DeadlineExceeded {
			return string(log.data), failure(504, "video_processing_timeout", "video preprocessing timed out")
		}
		return string(log.data), failure(499, "video_cancelled", "video preprocessing cancelled")
	}
	if readErr != nil {
		return string(log.data), readErr
	}
	if log.err != nil {
		return string(log.data), log.err
	}
	if ctx.Err() != nil {
		return string(log.data), failure(504, "video_processing_timeout", "video preprocessing timed out or was cancelled")
	}
	if waitErr != nil {
		return string(log.data), failure(422, "video_decode_failed", "video media decoding failed")
	}
	return string(log.data), nil
}

func runtimeChildStats(pids []int) (int64, int, int64) {
	var bytes int64
	var peakWorker int64
	count := 0
	for _, pid := range pids {
		comm, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
		name := strings.TrimSpace(string(comm))
		if e != nil || (name != "ffmpeg" && name != "ffprobe") {
			continue
		}
		data, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
		if e != nil {
			continue
		}
		fields := strings.Fields(string(data))
		if len(fields) < 2 {
			continue
		}
		pages, e := strconv.ParseInt(fields[1], 10, 64)
		if e == nil && pages >= 0 {
			rss := pages * int64(os.Getpagesize())
			bytes += rss
			peakWorker = max(peakWorker, rss)
			if pages > 0 {
				count++
			}
		}
	}
	return bytes, count, peakWorker
}
