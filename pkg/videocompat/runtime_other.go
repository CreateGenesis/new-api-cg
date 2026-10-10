//go:build !linux

package videocompat

import (
	"context"
	"io"
)

var ffmpegBinary, ffprobeBinary, prlimitBinary string

func samplePlatform(s *SystemSample)                   {}
func runtimeChildStats(pids []int) (int64, int, int64) { return 0, 0, 0 }
func runMedia(ctx context.Context, c Config, binary string, args []string, read func(io.Reader) error) (string, error) {
	return "", failure(503, "video_runtime_unavailable", "video preprocessing is available on Linux only")
}
