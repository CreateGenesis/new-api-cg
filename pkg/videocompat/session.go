package videocompat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/tidwall/gjson"
)

type videoBlock struct {
	encoded, mime string
	invalid       error
	source        *videoSource
}
type videoSource struct {
	path, hash string
	manifest   *manifest
	frames     map[string]imageFrame
}
type contentBlock struct {
	raw   string
	video int
	child *contentArray
	node  gjson.Result
}
type contentArray struct {
	node   gjson.Result
	blocks []contentBlock
}

type Session struct {
	mu                                           sync.Mutex
	ctx                                          context.Context
	cancel                                       context.CancelFunc
	mediaContext                                 context.Context
	mediaCancel                                  context.CancelFunc
	raw                                          []byte
	storage                                      common.BodyStorage
	protocol                                     string
	arrays                                       []*contentArray
	videos                                       []videoBlock
	sources                                      map[string]*videoSource
	variants                                     map[string][]byte
	storageVariants                              map[string]common.BodyStorage
	config                                       Config
	tier                                         int
	frozen, closed, prepared, parsed             bool
	invalid, prepareError                        error
	tempDir                                      string
	memoryBytes, diskBytes, jpegBytes, bodyBytes int64
}

// NewStorageSession borrows storage and reads it only after media pool admission.
func NewStorageSession(ctx context.Context, storage common.BodyStorage, protocol string) (*Session, error) {
	if storage == nil {
		return nil, failure(400, "video_body_missing", "video request body is unavailable")
	}
	if protocol != "chat" && protocol != "responses" && protocol != "messages" {
		return nil, failure(400, "video_protocol_unsupported", "unsupported video request protocol")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{ctx: ctx, cancel: cancel, storage: storage, protocol: protocol, sources: make(map[string]*videoSource), variants: make(map[string][]byte)}
	if storage.IsDisk() {
		if e := globalPool.reserve(false, storage.Size()); e != nil {
			cancel()
			return nil, e
		}
		s.diskBytes = storage.Size()
	} else if e := s.reserveMemory(storage.Size()); e != nil {
		cancel()
		return nil, e
	}
	return s, nil
}

func NewSession(ctx context.Context, raw []byte, protocol string) (*Session, error) {
	if protocol != "chat" && protocol != "responses" && protocol != "messages" {
		return nil, failure(400, "video_protocol_unsupported", "unsupported video request protocol")
	}
	if !gjson.ValidBytes(raw) {
		return nil, failure(400, "video_json_invalid", "invalid JSON request")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{ctx: ctx, cancel: cancel, raw: raw, protocol: protocol, sources: make(map[string]*videoSource), variants: make(map[string][]byte)}
	if e := s.reserveMemory(int64(len(raw))); e != nil {
		cancel()
		return nil, e
	}
	return s, nil
}

func (s *Session) initialize() error {
	if s.parsed {
		return nil
	}
	if !gjson.ValidBytes(s.raw) {
		return failure(400, "video_json_invalid", "invalid JSON request")
	}
	if e := s.reserveMemory(int64(len(s.raw))); e != nil {
		return e
	}
	s.parsed = true
	root := gjson.ParseBytes(s.raw)
	seen := false
	field := "messages"
	if s.protocol == "responses" {
		field = "input"
	}
	var items gjson.Result
	root.ForEach(func(k, v gjson.Result) bool {
		if strings.EqualFold(k.Str, field) {
			if seen {
				s.invalid = failure(400, "video_json_invalid", "duplicate request content field")
			}
			seen = true
			items = v
		}
		return true
	})
	if items.IsArray() {
		for _, item := range items.Array() {
			if s.protocol == "responses" && (item.Get("type").String() == "input_video" || item.Get("type").String() == "video_url") {
				s.videos = append(s.videos, videoBlock{})
				s.invalid = failure(400, "video_content_invalid", "Responses video blocks require a message content array")
				continue
			}
			content := envelopeField(item, "content")
			if !content.IsArray() && s.protocol == "responses" && item.Get("type").String() == "function_call_output" {
				content = envelopeField(item, "output")
			}
			if content.IsArray() {
				a := s.scanArray(content, 0)
				s.arrays = append(s.arrays, a)
			}
		}
	}
	return nil
}

// Typed request envelopes accept case-insensitive JSON field names. Preserve
// the original key and byte positions, and follow the decoder's last-key rule.
func envelopeField(node gjson.Result, name string) gjson.Result {
	var result gjson.Result
	node.ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.Str, name) {
			result = value
		}
		return true
	})
	return result
}

func HasVideo(raw []byte, protocol string) (bool, error) {
	s, e := NewSession(context.Background(), raw, protocol)
	if e != nil {
		return false, e
	}
	defer s.Close()
	if e = s.initialize(); e != nil {
		return false, e
	}
	return len(s.videos) > 0, nil
}

func (s *Session) scanArray(node gjson.Result, depth int) *contentArray {
	a := &contentArray{node: node}
	if depth > 8 {
		s.invalid = failure(400, "video_content_invalid", "video tool content nesting exceeds limit")
		return a
	}
	for _, part := range node.Array() {
		b := contentBlock{raw: part.Raw, video: -1, node: part}
		kind := part.Get("type").String()
		if kind == "video_url" || kind == "input_video" || kind == "video" {
			b.video = len(s.videos)
			v := videoBlock{}
			if s.protocol == "messages" && kind == "video" {
				source := part.Get("source")
				switch source.Get("type").String() {
				case "base64":
					v.mime = source.Get("media_type").String()
					v.encoded = source.Get("data").String()
				case "url":
					v.mime, v.encoded, v.invalid = inlineURI(source.Get("url").String())
				default:
					v.invalid = failure(400, "video_source_unsupported", "only inline base64 video sources are supported")
				}
			} else {
				url := part.Get("video_url")
				if url.IsObject() {
					url = url.Get("url")
				}
				if !url.Exists() {
					url = part.Get("url")
				}
				v.mime, v.encoded, v.invalid = inlineURI(url.String())
			}
			if v.invalid == nil && (!strings.HasPrefix(v.mime, "video/") || len(v.mime) > 100 || v.encoded == "") {
				v.invalid = failure(400, "video_source_invalid", "invalid inline base64 video")
			}
			s.videos = append(s.videos, v)
		} else if kind == "tool_result" {
			content := envelopeField(part, "content")
			if content.IsArray() {
				b.child = s.scanArray(content, depth+1)
			}
		}
		a.blocks = append(a.blocks, b)
	}
	return a
}

func inlineURI(value string) (string, string, error) {
	rest, ok := strings.CutPrefix(value, "data:")
	if !ok {
		return "", "", failure(400, "video_source_unsupported", "only inline base64 videos are supported")
	}
	mime, data, ok := strings.Cut(rest, ";base64,")
	if !ok {
		return "", "", failure(400, "video_source_invalid", "invalid base64 video data URI")
	}
	return mime, data, nil
}

func (s *Session) reserveMemory(n int64) error {
	if e := s.reserve(true, n); e != nil {
		return e
	}
	s.memoryBytes += n
	return nil
}
func (s *Session) reserve(memory bool, n int64) error {
	config := GetConfig()
	if s.frozen {
		config = s.config
	}
	return globalPool.reserveWithConfig(memory, n, config)
}
func (s *Session) releaseMemory(n int64) {
	n = min(n, s.memoryBytes)
	_ = globalPool.reserve(true, -n)
	s.memoryBytes -= n
}
func (s *Session) Close() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.mediaCancel != nil {
		s.mediaCancel()
	}
	if s.tempDir != "" {
		_ = os.RemoveAll(s.tempDir)
	}
	s.releaseMemory(s.memoryBytes)
	_ = globalPool.reserve(false, -s.diskBytes)
	s.diskBytes = 0
	s.variants = nil
	for _, storage := range s.storageVariants {
		_ = storage.Close()
	}
	s.storageVariants = nil
	s.sources = nil
	s.raw = nil
	s.arrays = nil
	s.videos = nil
	s.storage = nil
}

type diskWriter struct {
	s            *Session
	w            io.Writer
	requestBytes *int64
}

func (w *diskWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > 16*MiB-*w.requestBytes {
		return 0, failure(413, "video_decoded_bytes_limit", "decoded videos exceed 16 MiB request budget")
	}
	if e := w.s.reserve(false, int64(len(p))); e != nil {
		return 0, e
	}
	n, e := w.w.Write(p)
	w.s.diskBytes += int64(n)
	*w.requestBytes += int64(n)
	if n < len(p) {
		_ = globalPool.reserve(false, -int64(len(p)-n))
	}
	return n, e
}

func (s *Session) prepare() error {
	if s.prepared {
		return s.prepareError
	}
	s.prepared = true
	if s.invalid != nil {
		s.prepareError = s.invalid
		return s.prepareError
	}
	if len(s.videos) > 8 {
		s.prepareError = failure(413, "video_count_limit", "video request exceeds eight video blocks")
		return s.prepareError
	}
	for _, v := range s.videos {
		if v.invalid != nil {
			s.prepareError = v.invalid
			return v.invalid
		}
	}
	var e error
	s.tempDir, e = os.MkdirTemp("", "new-api-video-")
	if e != nil {
		s.prepareError = failure(503, "video_storage_unavailable", "video temporary storage is unavailable")
		return s.prepareError
	}
	decoded := int64(0)
	sourceFrames := 0
	for i := range s.videos {
		if e = s.mediaContext.Err(); e != nil {
			s.prepareError = failure(504, "video_processing_timeout", "video preprocessing timed out")
			if e == context.Canceled {
				s.prepareError = failure(499, "video_cancelled", "video preprocessing cancelled")
			}
			return s.prepareError
		}
		file, err := os.CreateTemp(s.tempDir, "source-")
		if err != nil {
			s.prepareError = failure(503, "video_storage_unavailable", "video temporary storage is unavailable")
			return s.prepareError
		}
		hash := sha256.New()
		writer := &diskWriter{s, file, &decoded}
		_, err = io.Copy(io.MultiWriter(writer, hash), base64.NewDecoder(base64.StdEncoding, strings.NewReader(s.videos[i].encoded)))
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			if _, ok := err.(*Error); !ok {
				err = failure(400, "video_base64_invalid", "invalid video base64 data")
			}
			s.prepareError = err
			return err
		}
		key := hex.EncodeToString(hash.Sum(nil))
		if previous, ok := s.sources[key]; ok {
			s.videos[i].source = previous
			size, _ := os.Stat(file.Name())
			if size != nil {
				_ = os.Remove(file.Name())
				s.diskBytes -= size.Size()
				_ = globalPool.reserve(false, -size.Size())
			}
			globalPool.mu.Lock()
			globalPool.stats.DecodeCacheHits++
			globalPool.mu.Unlock()
			continue
		}
		if e = s.reserveMemory(4 * MiB); e != nil {
			s.prepareError = e
			return e
		}
		started := time.Now()
		m, err := probe(s, file.Name(), maxSourceFrames-sourceFrames)
		globalPool.recordPhase("decode", time.Since(started))
		globalPool.mu.Lock()
		globalPool.stats.DecodeCacheMisses++
		globalPool.mu.Unlock()
		if err != nil {
			s.prepareError = err
			return err
		}
		sourceFrames += len(m.Frames)
		source := &videoSource{path: file.Name(), hash: key, manifest: m, frames: make(map[string]imageFrame)}
		s.sources[key] = source
		s.videos[i].source = source
	}
	return nil
}

func adaptiveSettings(base dto.VideoCompatibilitySettings, c AdaptiveConfig, tier, videos int) dto.VideoCompatibilitySettings {
	if tier == 0 {
		return base
	}
	l := c.Light
	if tier == 2 {
		l = c.Heavy
	}
	settings := base
	settings.PixelPercent = max(min(base.PixelPercent, c.MinimumPixelPercent), (base.PixelPercent*l.PixelMultiplierPercent+50)/100)
	settings.FPS = float64(max(min(int(math.Round(base.FPS*10)), int(math.Round(c.MinimumFPS*10))), (int(math.Round(base.FPS*10))*l.FPSMultiplierPercent+50)/100)) / 10
	settings.MaxFrames = max(videos, min(base.MaxFrames, max(min(base.MaxFrames, c.MinimumFrames), (base.MaxFrames*l.FramesMultiplierPercent+50)/100)))
	settings.JPEGQuality = min(base.JPEGQuality, max(min(base.JPEGQuality, c.MinimumJPEGQuality), l.JPEGQualityCap))
	return settings
}

func (s *Session) Variant(base dto.VideoCompatibilitySettings) (body []byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, failure(499, "video_cancelled", "video session is closed")
	}
	if !base.Enabled || (s.parsed && len(s.videos) == 0) {
		if s.raw == nil && s.storage != nil {
			return s.storage.Bytes()
		}
		return s.raw, nil
	}
	if err = base.Validate(); err != nil {
		return nil, failure(400, "video_settings_invalid", err.Error())
	}
	key := fmt.Sprintf("%.1f/%d/%d/%d/%d", base.FPS, base.MaxFrames, base.MaxSide, base.PixelPercent, base.JPEGQuality)
	if cached, ok := s.variants[key]; ok {
		globalPool.mu.Lock()
		globalPool.stats.CacheHits++
		globalPool.mu.Unlock()
		return cached, nil
	}
	admissionCtx := s.ctx
	if s.mediaContext == nil {
		s.mediaContext, s.mediaCancel = context.WithTimeout(s.ctx, time.Duration(GetConfig().RequestTimeoutSeconds)*time.Second)
	}
	if s.mediaContext != nil {
		admissionCtx = s.mediaContext
	}
	size := int64(len(s.raw))
	if s.storage != nil {
		size = s.storage.Size()
	}
	started := time.Now()
	var frozen []Config
	if s.frozen {
		frozen = []Config{s.config}
	}
	tier, config, e := globalPool.admit(admissionCtx, size, frozen...)
	if e != nil {
		return nil, e
	}
	defer func() { globalPool.finish(started, err, config) }()
	if !s.frozen {
		s.frozen = true
		s.tier = tier
		s.config = config
		globalPool.mu.Lock()
		globalPool.recordTier(tier)
		globalPool.mu.Unlock()
	}
	if s.raw == nil && s.storage != nil {
		if s.storage.IsDisk() {
			if err = s.reserveMemory(s.storage.Size()); err != nil {
				return nil, err
			}
		}
		raw, e := s.storage.Bytes()
		if e != nil {
			return nil, failure(400, "video_body_unavailable", "video request body could not be read")
		}
		s.raw = raw
	}
	if err = s.initialize(); err != nil {
		return nil, err
	}
	if s.invalid != nil {
		return nil, s.invalid
	}
	if len(s.videos) == 0 {
		return s.raw, nil
	}
	if err = s.prepare(); err != nil {
		return nil, err
	}
	settings := adaptiveSettings(base, s.config.AdaptiveConfig, s.tier, len(s.videos))
	desired := make([]int, len(s.videos))
	for i, v := range s.videos {
		desired[i] = desiredFrames(v.source.manifest, int(math.Round(settings.FPS*10)))
	}
	counts := allocateFrames(desired, max(settings.MaxFrames, len(s.videos)))
	videoFrames := make([][]imageFrame, len(s.videos))
	q := 2 + ((100-settings.JPEGQuality)*29+49)/99
	for i, v := range s.videos {
		source := v.source
		m := source.manifest
		w, h := outputDimensions(m, settings.MaxSide, settings.PixelPercent)
		selected := indices(len(m.Frames), counts[i])
		missing := make([]int, 0, len(selected))
		keys := make([]string, len(selected))
		for j, index := range selected {
			keys[j] = fmt.Sprintf("%d/%d/%d/%d", index, w, h, q)
			if _, ok := source.frames[keys[j]]; !ok {
				missing = append(missing, index)
				globalPool.mu.Lock()
				globalPool.stats.FrameCacheMisses++
				globalPool.mu.Unlock()
			} else {
				globalPool.mu.Lock()
				globalPool.stats.FrameCacheHits++
				globalPool.mu.Unlock()
			}
		}
		if len(missing) > 0 {
			phaseStart := time.Now()
			frames, e := extract(s, source.path, m, missing, w, h, q)
			globalPool.recordPhase("extract", time.Since(phaseStart))
			if e != nil {
				return nil, e
			}
			for j, index := range missing {
				source.frames[fmt.Sprintf("%d/%d/%d/%d", index, w, h, q)] = frames[j]
			}
		}
		videoFrames[i] = make([]imageFrame, len(selected))
		for j, k := range keys {
			videoFrames[i][j] = source.frames[k]
		}
	}
	body, err = s.rewrite(videoFrames)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > 128*MiB-s.bodyBytes {
		s.releaseMemory(int64(cap(body)))
		return nil, failure(413, "video_variant_limit", "converted request variants exceed request budget")
	}
	s.bodyBytes += int64(len(body))
	s.variants[key] = body
	globalPool.mu.Lock()
	for _, frames := range videoFrames {
		globalPool.stats.OutputFrames += uint64(len(frames))
	}
	globalPool.mu.Unlock()
	return body, nil
}

// VariantStorage keeps large transformed requests in files through upstream completion.
func (s *Session) VariantStorage(settings dto.VideoCompatibilitySettings) (common.BodyStorage, error) {
	key := fmt.Sprintf("%.1f/%d/%d/%d/%d", settings.FPS, settings.MaxFrames, settings.MaxSide, settings.PixelPercent, settings.JPEGQuality)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, failure(499, "video_cancelled", "video session is closed")
	}
	if cached := s.storageVariants[key]; cached != nil {
		s.mu.Unlock()
		globalPool.mu.Lock()
		globalPool.stats.CacheHits++
		globalPool.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()
	body, e := s.Variant(settings)
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, failure(499, "video_cancelled", "video session is closed")
	}
	if !settings.Enabled || len(s.videos) == 0 {
		if s.storage != nil {
			return s.storage, nil
		}
		return common.NewMemoryBodyStorage(body), nil
	}
	if s.storageVariants == nil {
		s.storageVariants = make(map[string]common.BodyStorage)
	}
	if cached := s.storageVariants[key]; cached != nil {
		return cached, nil
	}
	var storage common.BodyStorage
	if len(body) >= int(MiB) {
		if e = s.reserve(false, int64(len(body))); e != nil {
			return nil, e
		}
		storage, e = common.NewFileBodyStorage(bytes.NewReader(body), convertedBodyLimit())
		if e != nil {
			_ = globalPool.reserve(false, -int64(len(body)))
			return nil, failure(503, "video_storage_unavailable", "converted video request storage is unavailable")
		}
		s.diskBytes += int64(len(body))
		delete(s.variants, key)
		s.releaseMemory(int64(cap(body)))
	} else {
		storage = common.NewMemoryBodyStorage(body)
	}
	s.storageVariants[key] = storage
	return storage, nil
}

type chargedBuffer struct {
	s *Session
	bytes.Buffer
	charged int64
	limit   int64
}

func (b *chargedBuffer) Write(p []byte) (int, error) {
	if e := b.grow(len(p)); e != nil {
		return 0, e
	}
	return b.Buffer.Write(p)
}
func (b *chargedBuffer) grow(n int) error {
	if int64(n) > b.limit-int64(b.Len()) {
		return failure(413, "video_body_limit", "converted video request exceeds body budget")
	}
	if b.Cap()-b.Len() >= n {
		return nil
	}
	before := b.Cap()
	reservation := int64(2*(b.Len()+n) + 8192 - before)
	if e := b.s.reserveMemory(reservation); e != nil {
		return e
	}
	b.Buffer.Grow(n)
	actual := int64(b.Cap() - before)
	b.s.releaseMemory(reservation - actual)
	b.charged += actual
	return nil
}
func (b *chargedBuffer) text(text string) error {
	if e := b.grow(len(text)); e != nil {
		return e
	}
	_, e := b.Buffer.WriteString(text)
	return e
}

func (s *Session) rewrite(videoFrames [][]imageFrame) ([]byte, error) {
	arrays := append([]*contentArray(nil), s.arrays...)
	sort.Slice(arrays, func(i, j int) bool { return arrays[i].node.Index < arrays[j].node.Index })
	buffer := &chargedBuffer{s: s, limit: convertedBodyLimit()}
	position := 0
	for _, array := range arrays {
		start := array.node.Index
		end := start + len(array.node.Raw)
		if start < position || end > len(s.raw) {
			s.releaseMemory(buffer.charged)
			return nil, failure(400, "video_content_invalid", "invalid video content position")
		}
		if _, e := buffer.Write(s.raw[position:start]); e != nil {
			s.releaseMemory(buffer.charged)
			return nil, e
		}
		if e := s.renderArray(buffer, array, videoFrames); e != nil {
			s.releaseMemory(buffer.charged)
			return nil, e
		}
		position = end
	}
	if _, e := buffer.Write(s.raw[position:]); e != nil {
		s.releaseMemory(buffer.charged)
		return nil, e
	}
	return buffer.Bytes(), nil
}

func convertedBodyLimit() int64 {
	if constant.MaxRequestBodyMB <= 0 {
		return 64 * MiB
	}
	return int64(min(64, constant.MaxRequestBodyMB)) * MiB
}

func (s *Session) renderArray(buffer *chargedBuffer, array *contentArray, videoFrames [][]imageFrame) error {
	if e := buffer.text("["); e != nil {
		return e
	}
	first := true
	for _, block := range array.blocks {
		separator := func() error {
			if !first {
				return buffer.text(",")
			}
			first = false
			return nil
		}
		if block.video >= 0 {
			v := s.videos[block.video]
			frames := videoFrames[block.video]
			for i, frame := range frames {
				label := fmt.Sprintf("[Video %d; frame %d/%d; source time %s]", block.video+1, i+1, len(frames), timestamp(frame.PTS, v.source.manifest.TimeBase))
				if i == 0 {
					label += " Audio is not included."
				}
				text := map[string]any{"type": "text", "text": label}
				if s.protocol == "responses" {
					text["type"] = "input_text"
				}
				t, e := common.Marshal(text)
				if e != nil {
					return e
				}
				if e = separator(); e != nil {
					return e
				}
				if _, e = buffer.Write(t); e != nil {
					return e
				}
				if e = separator(); e != nil {
					return e
				}
				prefix, suffix := `{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,`, `"}`
				switch s.protocol {
				case "responses":
					prefix, suffix = `{"type":"input_image","image_url":"data:image/jpeg;base64,`, `"`
				case "messages":
					prefix, suffix = `{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"`, `"}`
				}
				if e = buffer.text(prefix); e != nil {
					return e
				}
				if e = buffer.text(frame.Data); e != nil {
					return e
				}
				if e = buffer.text(suffix); e != nil {
					return e
				}
				if i == len(frames)-1 && block.node.Get("cache_control").Exists() {
					if e = buffer.text(`,"cache_control":`); e != nil {
						return e
					}
					if e = buffer.text(block.node.Get("cache_control").Raw); e != nil {
						return e
					}
				}
				if e = buffer.text("}"); e != nil {
					return e
				}
			}
		} else if block.child != nil {
			if e := separator(); e != nil {
				return e
			}
			offset := block.child.node.Index - block.node.Index
			if offset < 0 || offset+len(block.child.node.Raw) > len(block.raw) {
				return failure(400, "video_content_invalid", "invalid nested content position")
			}
			if e := buffer.text(block.raw[:offset]); e != nil {
				return e
			}
			if e := s.renderArray(buffer, block.child, videoFrames); e != nil {
				return e
			}
			if e := buffer.text(block.raw[offset+len(block.child.node.Raw):]); e != nil {
				return e
			}
		} else {
			if e := separator(); e != nil {
				return e
			}
			if e := buffer.text(block.raw); e != nil {
				return e
			}
		}
	}
	return buffer.text("]")
}
