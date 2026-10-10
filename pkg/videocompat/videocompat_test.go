package videocompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/jpeg"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSamplingAndAdaptiveContracts(t *testing.T) {
	assert.Equal(t, []int{0, 5, 9}, indices(10, 3))
	assert.Equal(t, []int{3, 2}, allocateFrames([]int{6, 6}, 5))
	m := &manifest{Width: 1920, Height: 1080, Duration: big.NewRat(1, 1), Frames: make([]sourceFrame, 30)}
	assert.Equal(t, 8, desiredFrames(m, 80))
	w, h := outputDimensions(m, 1024, 25)
	assert.Equal(t, 512, w)
	assert.Equal(t, 288, h)
	base := dto.DefaultVideoCompatibilitySettings()
	base.Enabled = true
	c := DefaultConfig().AdaptiveConfig
	light := adaptiveSettings(base, c, 1, 1)
	assert.Equal(t, 6.0, light.FPS)
	assert.Equal(t, 192, light.MaxFrames)
	assert.Equal(t, 50, light.PixelPercent)
	assert.Equal(t, 75, light.JPEGQuality)
	base.FPS = .1
	base.MaxFrames = 2
	base.PixelPercent = 1
	base.JPEGQuality = 1
	assert.Equal(t, base, adaptiveSettings(base, c, 2, 2))
	for _, raw := range []string{`{"messages":[{"role":"user","content":"plain"}],"tools":[{"type":"video_url"}]}`, `{"messages":[{"role":"assistant","tool_calls":[{"function":{"arguments":"video_url"}}]}]}`, `{"Messages":[{"role":"user","Content":[{"type":"VIDEO_URL","video_url":"data:video/mp4;base64,AAAA"}]}]}`} {
		found, e := HasVideo([]byte(raw), "chat")
		require.NoError(t, e)
		assert.False(t, found)
	}
	remote := []byte(`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":"https://example.com/v.mp4"}]}]}`)
	s, e := NewSession(context.Background(), remote, "chat")
	require.NoError(t, e)
	defer s.Close()
	native, e := s.Variant(dto.VideoCompatibilitySettings{})
	require.NoError(t, e)
	assert.Equal(t, remote, native)
	_, e = s.Variant(dto.VideoCompatibilitySettings{Enabled: true, FPS: 1, MaxFrames: 1, MaxSide: 256, PixelPercent: 100, JPEGQuality: 100})
	var mediaErr *Error
	require.ErrorAs(t, e, &mediaErr)
	assert.Equal(t, 400, mediaErr.StatusCode)
}

type observedStorage struct {
	common.BodyStorage
	reads atomic.Int32
}

func (s *observedStorage) Bytes() ([]byte, error) { s.reads.Add(1); return s.BodyStorage.Bytes() }

func TestRealWorkerCancellationQueueAndHotConfig(t *testing.T) {
	video := fixtureVideo(t, 10)
	path := filepath.Join(t.TempDir(), "loop.mkv")
	require.NoError(t, os.WriteFile(path, video, 0600))
	original := GetConfig()
	config := DefaultConfig()
	config.MaxConcurrency = 1
	config.QueueCapacity = 1
	config.QueueBytesMB = 1
	config.RequestTimeoutSeconds = 2
	require.NoError(t, SetConfig(config))
	defer func() { require.NoError(t, SetConfig(original)) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, admittedConfig, e := globalPool.admit(ctx, 0)
	require.NoError(t, e)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		t0 := time.Now()
		_, err := runMedia(ctx, config, ffmpegBinary, []string{"-hide_banner", "-nostdin", "-v", "error", "-re", "-stream_loop", "-1", "-threads", "1", "-i", path, "-map", "0:v:0", "-an", "-c:v", "copy", "-f", "null", "pipe:1"}, func(r io.Reader) error { close(started); _, e := io.Copy(io.Discard, r); return e })
		globalPool.finish(t0, err, admittedConfig)
		done <- err
	}()
	<-started
	settings := dto.DefaultVideoCompatibilitySettings()
	settings.Enabled = true
	settings.FPS = 1
	settings.MaxFrames = 1
	large := []byte(`{"pad":"` + strings.Repeat("a", int(MiB)) + `","messages":[{"role":"user","content":[{"type":"video_url","video_url":"data:video/x-matroska;base64,` + base64.StdEncoding.EncodeToString(video) + `"}]}]}`)
	largeStorage := &observedStorage{BodyStorage: common.NewMemoryBodyStorage(large)}
	largeSession, e := NewStorageSession(context.Background(), largeStorage, "chat")
	require.NoError(t, e)
	_, e = largeSession.Variant(settings)
	var local *Error
	require.ErrorAs(t, e, &local)
	assert.Equal(t, "video_queue_full", local.Code)
	assert.Zero(t, largeStorage.reads.Load())
	largeSession.Close()
	largeStorage.Close()
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":"data:video/x-matroska;base64,` + base64.StdEncoding.EncodeToString(video) + `"}]}]}`)
	queueContext, queueCancel := context.WithCancel(context.Background())
	storage := &observedStorage{BodyStorage: common.NewMemoryBodyStorage(raw)}
	queued, e := NewStorageSession(queueContext, storage, "chat")
	require.NoError(t, e)
	queueDone := make(chan error, 1)
	go func() { _, e := queued.Variant(settings); queueDone <- e }()
	require.Eventually(t, func() bool { return Stats().Queued == 1 }, time.Second, 5*time.Millisecond)
	assert.Zero(t, storage.reads.Load())
	queueCancel()
	require.ErrorAs(t, <-queueDone, &local)
	assert.Equal(t, "video_cancelled", local.Code)
	queued.Close()
	storage.Close()
	assert.Zero(t, Stats().QueuedBytes)
	timed, e := NewStorageSession(context.Background(), common.NewMemoryBodyStorage(raw), "chat")
	require.NoError(t, e)
	config.RequestTimeoutSeconds = 1
	require.NoError(t, SetConfig(config))
	_, e = timed.Variant(settings)
	require.ErrorAs(t, e, &local)
	assert.Equal(t, "video_processing_timeout", local.Code)
	timed.Close()
	assert.Zero(t, Stats().Queued)
	hot, e := NewStorageSession(context.Background(), common.NewMemoryBodyStorage(raw), "chat")
	require.NoError(t, e)
	config.RequestTimeoutSeconds = 5
	require.NoError(t, SetConfig(config))
	hotDone := make(chan error, 1)
	go func() { _, e := hot.Variant(settings); hotDone <- e }()
	require.Eventually(t, func() bool { return Stats().Queued == 1 }, time.Second, 5*time.Millisecond)
	config.MaxConcurrency = 2
	require.NoError(t, SetConfig(config))
	require.NoError(t, <-hotDone)
	hot.Close()
	cancel()
	require.ErrorAs(t, <-done, &local)
	assert.Equal(t, "video_cancelled", local.Code)
	assert.Zero(t, Stats().Running)
	assert.Zero(t, Stats().Queued)
	assert.Zero(t, Stats().CurrentChildRSSBytes)
	assert.Zero(t, Stats().BufferBytes)
	assert.Zero(t, Stats().DiskBytes)
}

func fixtureVideo(t *testing.T, frames int) []byte {
	t.Helper()
	if ffmpegBinary == "" || ffprobeBinary == "" || prlimitBinary == "" {
		t.Skip("Linux ffmpeg/ffprobe/prlimit required")
	}
	raw := make([]byte, frames*64*32*3)
	for frame := range frames {
		for pixel := range 64 * 32 {
			raw[(frame*64*32+pixel)*3] = byte(20 + frame*8)
		}
	}
	path := filepath.Join(t.TempDir(), "source.mkv")
	cmd := exec.Command(ffmpegBinary, "-hide_banner", "-v", "error", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", "64x32", "-r", "10", "-i", "pipe:0", "-an", "-c:v", "ffv1", "-threads", "1", "-y", path)
	cmd.Stdin = bytes.NewReader(raw)
	output, e := cmd.CombinedOutput()
	require.NoError(t, e, string(output))
	video, e := os.ReadFile(path)
	require.NoError(t, e)
	return video
}

func TestRealMediaProtocolAndCacheContracts(t *testing.T) {
	video := fixtureVideo(t, 10)
	encoded := base64.StdEncoding.EncodeToString(video)
	settings := dto.DefaultVideoCompatibilitySettings()
	settings.Enabled = true
	settings.FPS = 3
	settings.MaxFrames = 3
	settings.MaxSide = 256
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			var raw []byte
			switch protocol {
			case "chat":
				raw = []byte(`{"model":"x","vendor":{"precise":1e0},"messages":[{"role":"user","content":[{"type":"text","text":"before"},{"type":"video_url","video_url":{"url":"data:video/x-matroska;base64,` + encoded + `"},"cache_control":{"type":"ephemeral"}},{"type":"text","text":"after"}]}]}`)
			case "responses":
				raw = []byte(`{"model":"x","vendor":{"precise":1e0},"input":[{"role":"user","content":[{"type":"input_video","video_url":"data:video/x-matroska;base64,` + encoded + `","cache_control":{"type":"ephemeral"}}]}]}`)
			case "messages":
				raw = []byte(`{"model":"x","vendor":{"precise":1e0},"messages":[{"role":"user","content":[{"type":"video","source":{"type":"base64","media_type":"video/x-matroska","data":"` + encoded + `"},"cache_control":{"type":"ephemeral"}}]}]}`)
			}
			found, e := HasVideo(raw, protocol)
			require.NoError(t, e)
			require.True(t, found)
			storage := common.NewMemoryBodyStorage(raw)
			defer storage.Close()
			before := Stats()
			s, e := NewStorageSession(context.Background(), storage, protocol)
			require.NoError(t, e)
			body, e := s.Variant(settings)
			require.NoError(t, e)
			assert.True(t, gjson.ValidBytes(body))
			assert.Contains(t, string(body), `"precise":1e0`)
			assert.NotContains(t, string(body), `"type":"video`)
			assert.Contains(t, string(body), "00:00:00.500")
			assert.Contains(t, string(body), "00:00:00.900")
			root := "messages.0.content"
			if protocol == "responses" {
				root = "input.0.content"
			}
			images := 0
			for _, part := range gjson.GetBytes(body, root).Array() {
				data := ""
				switch part.Get("type").String() {
				case "image_url":
					data = strings.TrimPrefix(part.Get("image_url.url").String(), "data:image/jpeg;base64,")
				case "input_image":
					data = strings.TrimPrefix(part.Get("image_url").String(), "data:image/jpeg;base64,")
				case "image":
					data = part.Get("source.data").String()
				}
				if data == "" {
					continue
				}
				jpegBytes, e := base64.StdEncoding.DecodeString(data)
				require.NoError(t, e)
				image, e := jpeg.Decode(bytes.NewReader(jpegBytes))
				require.NoError(t, e)
				red, _, _, _ := image.At(32, 16).RGBA()
				expected := []int{20, 60, 92}
				assert.InDelta(t, expected[images], int(red>>8), 5)
				assert.Equal(t, images == 2, part.Get("cache_control").Exists())
				if images == 2 {
					assert.Equal(t, `{"type":"ephemeral"}`, part.Get("cache_control").Raw)
				}
				images++
			}
			assert.Equal(t, 3, images)
			assert.Equal(t, 1, strings.Count(string(body), `"cache_control"`))
			cached, e := s.Variant(settings)
			require.NoError(t, e)
			assert.Equal(t, body, cached)
			assert.Equal(t, before.DecodeCacheMisses+1, Stats().DecodeCacheMisses)
			s.Close()
			s.Close()
			assert.Equal(t, before.BufferBytes, Stats().BufferBytes)
			assert.Equal(t, before.DiskBytes, Stats().DiskBytes)
			mixedRaw := []byte(strings.NewReplacer(`"messages":`, `"Messages":`, `"input":`, `"Input":`, `"content":`, `"Content":`).Replace(string(raw)))
			mixed, e := NewSession(context.Background(), mixedRaw, protocol)
			require.NoError(t, e)
			mixedBody, e := mixed.Variant(settings)
			require.NoError(t, e)
			assert.Contains(t, string(mixedBody), `"precise":1e0`)
			assert.Contains(t, string(mixedBody), `"Content":`)
			assert.NotContains(t, string(mixedBody), `"type":"video`)
			envelope := "Messages.0.Content"
			if protocol == "responses" {
				envelope = "Input.0.Content"
			}
			images = 0
			gjson.GetBytes(mixedBody, envelope).ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "image_url" || item.Get("type").String() == "input_image" || item.Get("type").String() == "image" {
					images++
				}
				return true
			})
			assert.Equal(t, 3, images)
			mixed.Close()
			assert.Equal(t, before.BufferBytes, Stats().BufferBytes)
			assert.Equal(t, before.DiskBytes, Stats().DiskBytes)
		})
	}
}

func TestNestedToolVideoAndRequestAllocation(t *testing.T) {
	video := fixtureVideo(t, 20)
	encoded := base64.StdEncoding.EncodeToString(video)
	part := `{"type":"video_url","video_url":"data:video/x-matroska;base64,` + encoded + `"}`
	raw := []byte(`{"messages":[{"role":"user","content":[` + part + `,` + part + `]}]}`)
	s, e := NewSession(context.Background(), raw, "chat")
	require.NoError(t, e)
	defer s.Close()
	settings := dto.DefaultVideoCompatibilitySettings()
	settings.Enabled = true
	settings.FPS = 3
	settings.MaxFrames = 5
	body, e := s.Variant(settings)
	require.NoError(t, e)
	assert.Equal(t, 5, strings.Count(string(body), `"type":"image_url"`))
	assert.Contains(t, string(body), "frame 3/3")
	assert.Contains(t, string(body), "frame 2/2")
	assert.Len(t, s.sources, 1)
	settings.MaxFrames = 1
	minimum, e := s.Variant(settings)
	require.NoError(t, e)
	assert.Equal(t, 2, strings.Count(string(minimum), `"type":"image_url"`))
	settings.MaxFrames = 5
	nested := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"video","source":{"type":"base64","media_type":"video/x-matroska","data":"` + encoded + `"}}]}]}]}`)
	tools, e := NewSession(context.Background(), nested, "messages")
	require.NoError(t, e)
	defer tools.Close()
	converted, e := tools.Variant(settings)
	require.NoError(t, e)
	assert.True(t, gjson.ValidBytes(converted))
	assert.Equal(t, "x", gjson.GetBytes(converted, "messages.0.content.0.tool_use_id").String())
	assert.Equal(t, 5, strings.Count(string(converted), `"type":"image"`))
}

func TestBorrowedInputStorageBudgetAndOwnership(t *testing.T) {
	config := GetConfig()
	limited := config
	limited.TempDiskMB = 16
	require.NoError(t, SetConfig(limited))
	defer func() { require.NoError(t, SetConfig(config)) }()
	baseline := Stats()
	raw := []byte(`{"messages":[]}`)
	for _, disk := range []bool{false, true} {
		var storage common.BodyStorage
		var e error
		if disk {
			storage, e = common.NewFileBodyStorage(bytes.NewReader(raw), int64(len(raw)))
			require.NoError(t, e)
		} else {
			storage = common.NewMemoryBodyStorage(raw)
		}
		s, e := NewStorageSession(context.Background(), storage, "chat")
		require.NoError(t, e)
		if disk {
			assert.Equal(t, baseline.DiskBytes+int64(len(raw)), Stats().DiskBytes)
		} else {
			assert.Equal(t, baseline.BufferBytes+int64(len(raw)), Stats().BufferBytes)
		}
		s.Close()
		assert.Equal(t, baseline.DiskBytes, Stats().DiskBytes)
		assert.Equal(t, baseline.BufferBytes, Stats().BufferBytes)
		borrowed, e := storage.Bytes()
		require.NoError(t, e)
		assert.Equal(t, raw, borrowed)
		require.NoError(t, storage.Close())
	}
	oversize, e := common.NewFileBodyStorage(io.LimitReader(&zeroReader{}, 16*MiB+1), 16*MiB+1)
	require.NoError(t, e)
	defer oversize.Close()
	_, e = NewStorageSession(context.Background(), oversize, "chat")
	var local *Error
	require.ErrorAs(t, e, &local)
	assert.Equal(t, 503, local.StatusCode)
	assert.Equal(t, "video_resource_busy", local.Code)
	assert.Equal(t, baseline.DiskBytes, Stats().DiskBytes)
	reader, e := oversize.NewReader()
	require.NoError(t, e)
	require.NoError(t, reader.Close())
}

type zeroReader struct{}

func (*zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestConfigSnapshotVersionConsistency(t *testing.T) {
	original := GetConfig()
	defer func() { require.NoError(t, SetConfig(original)) }()
	baseline := Stats()
	require.NoError(t, SetConfig(original))
	assert.Equal(t, baseline.ConfigVersion, Stats().ConfigVersion)
	config := original
	config.MaxConcurrency = 33
	require.NoError(t, SetConfig(config))
	initial := Stats()
	if original.MaxConcurrency != 33 {
		assert.Equal(t, baseline.ConfigVersion+1, initial.ConfigVersion)
	}
	var group sync.WaitGroup
	configErrors := make(chan error, 1)
	group.Go(func() {
		for range 100 {
			config.MaxConcurrency = 34
			if e := SetConfig(config); e != nil {
				configErrors <- e
				return
			}
			config.MaxConcurrency = 33
			if e := SetConfig(config); e != nil {
				configErrors <- e
				return
			}
		}
		configErrors <- nil
	})
	for range 500 {
		snapshot := Stats()
		expected := 33
		if (snapshot.ConfigVersion-initial.ConfigVersion)%2 != 0 {
			expected = 34
		}
		assert.Equal(t, expected, snapshot.Config.MaxConcurrency)
	}
	group.Wait()
	require.NoError(t, <-configErrors)
}

func TestRealMediaResourceRejections(t *testing.T) {
	if ffmpegBinary == "" || ffprobeBinary == "" || prlimitBinary == "" {
		t.Skip("Linux ffmpeg/ffprobe/prlimit required")
	}
	for _, test := range []struct{ name, source, frames, code string }{
		{"duration", "color=s=16x16:r=1", "301", "video_duration_limit"},
		{"source_frames", "color=s=16x16:r=100", "20001", "video_source_frames_limit"},
		{"source_pixels", "color=s=4098x4098:r=1", "1", "video_resolution_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.mkv")
			cmd := exec.Command(ffmpegBinary, "-hide_banner", "-v", "error", "-f", "lavfi", "-i", test.source, "-frames:v", test.frames, "-an", "-c:v", "ffv1", "-threads", "1", "-y", path)
			output, e := cmd.CombinedOutput()
			require.NoError(t, e, string(output))
			video, e := os.ReadFile(path)
			require.NoError(t, e)
			raw := []byte(`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":"data:video/x-matroska;base64,` + base64.StdEncoding.EncodeToString(video) + `"}]}]}`)
			baseline := Stats()
			s, e := NewSession(context.Background(), raw, "chat")
			require.NoError(t, e)
			settings := dto.DefaultVideoCompatibilitySettings()
			settings.Enabled = true
			_, e = s.Variant(settings)
			var local *Error
			require.ErrorAs(t, e, &local)
			assert.Equal(t, 413, local.StatusCode)
			assert.Equal(t, test.code, local.Code)
			s.Close()
			assert.Equal(t, baseline.BufferBytes, Stats().BufferBytes)
			assert.Equal(t, baseline.DiskBytes, Stats().DiskBytes)
		})
	}
}

func TestRealNativePatchBudgetIndependentOfOutputPixels(t *testing.T) {
	if ffmpegBinary == "" || ffprobeBinary == "" || prlimitBinary == "" {
		t.Skip("Linux ffmpeg/ffprobe/prlimit required")
	}
	path := filepath.Join(t.TempDir(), "source.mp4")
	cmd := exec.Command(ffmpegBinary, "-hide_banner", "-v", "error", "-f", "lavfi", "-i", "color=s=3840x2160:r=8", "-frames:v", "80", "-an", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", "-y", path)
	output, e := cmd.CombinedOutput()
	require.NoError(t, e, string(output))
	video, e := os.ReadFile(path)
	require.NoError(t, e)
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":"data:video/mp4;base64,` + base64.StdEncoding.EncodeToString(video) + `"}]}]}`)
	t.Run("running_worker_survives_budget_reduction", func(t *testing.T) {
		original := GetConfig()
		config := original
		config.BufferMemoryMB = 256
		require.NoError(t, SetConfig(config))
		defer func() { require.NoError(t, SetConfig(original)) }()
		largeRaw := append([]byte(`{"pad":"`+strings.Repeat("a", 34*int(MiB))+`",`), raw[1:]...)
		storage := common.NewMemoryBodyStorage(largeRaw)
		defer storage.Close()
		session, e := NewStorageSession(context.Background(), storage, "chat")
		require.NoError(t, e)
		defer session.Close()
		settings := dto.DefaultVideoCompatibilitySettings()
		settings.Enabled, settings.MaxFrames, settings.MaxSide = true, 1, 256
		done := make(chan error, 1)
		go func() { _, e := session.VariantStorage(settings); done <- e }()
		require.Eventually(t, func() bool { return Stats().CurrentChildren > 0 }, 2*time.Second, time.Millisecond)
		config.BufferMemoryMB = 64
		require.NoError(t, SetConfig(config))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		queued := make(chan error, 1)
		go func() {
			began := time.Now()
			_, admitted, e := globalPool.admit(ctx, 0)
			if e == nil {
				globalPool.finish(began, nil, admitted)
			}
			queued <- e
		}()
		require.Eventually(t, func() bool { return Stats().Queued == 1 }, time.Second, time.Millisecond)
		require.NoError(t, <-done)
		assert.Equal(t, 1, Stats().Queued)
		session.Close()
		require.NoError(t, <-queued)
		assert.Zero(t, Stats().BufferBytes)
		assert.Zero(t, Stats().DiskBytes)
	})
	s, e := NewSession(context.Background(), raw, "chat")
	require.NoError(t, e)
	defer s.Close()
	settings := dto.DefaultVideoCompatibilitySettings()
	settings.Enabled = true
	large, e := s.Variant(settings)
	require.NoError(t, e)
	settings.PixelPercent = 1
	small, e := s.Variant(settings)
	require.NoError(t, e)
	assert.Equal(t, 39, strings.Count(string(large), `"type":"image_url"`))
	assert.Equal(t, 39, strings.Count(string(small), `"type":"image_url"`))
}

func TestRealConvertedBodyRespectsInboundLimit(t *testing.T) {
	video := fixtureVideo(t, 10)
	part := `{"type":"video_url","video_url":"data:video/x-matroska;base64,` + base64.StdEncoding.EncodeToString(video) + `"}`
	original := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	defer func() { constant.MaxRequestBodyMB = original }()
	settings := dto.DefaultVideoCompatibilitySettings()
	settings.Enabled = true
	settings.FPS, settings.MaxFrames = 8, 8
	for _, padding := range []int{0, int(MiB) - int(base64.StdEncoding.EncodedLen(len(video))) - 512} {
		raw := []byte(`{"pad":"` + strings.Repeat("a", padding) + `","messages":[{"role":"user","content":[` + part + `]}]}`)
		require.Less(t, int64(len(raw)), MiB)
		session, e := NewSession(context.Background(), raw, "chat")
		require.NoError(t, e)
		storage, e := session.VariantStorage(settings)
		if padding == 0 {
			require.NoError(t, e)
			assert.LessOrEqual(t, storage.Size(), MiB)
		} else {
			var local *Error
			require.ErrorAs(t, e, &local)
			assert.Equal(t, 413, local.StatusCode)
			assert.Equal(t, "video_body_limit", local.Code)
		}
		session.Close()
	}
}
