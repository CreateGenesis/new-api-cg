package videocompat

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const maxSourceFrames = 20000
const maxJPEGBytes = 32 * MiB
const maxImageBytes = 8 * MiB

type sourceFrame struct {
	PTS      int64
	Duration int64
}
type manifest struct {
	Width, Height int
	TimeBase      *big.Rat
	Frames        []sourceFrame
	Duration      *big.Rat
}
type imageFrame struct {
	PTS  int64
	Data string
}
type boundedReader struct {
	r         io.Reader
	remaining int64
}

func (r *boundedReader) Read(b []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, failure(413, "video_probe_limit", "video metadata exceeded budget")
	}
	b = b[:min(int64(len(b)), r.remaining)]
	n, e := r.r.Read(b)
	r.remaining -= int64(n)
	return n, e
}

func probe(ctxSession *Session, path string, remaining int) (*manifest, error) {
	m := &manifest{}
	rotation := 0
	args := []string{"-v", "error", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,avi,mpegts,mpeg,ogg", "-threads", strconv.Itoa(ctxSession.config.FFmpegThreads), "-err_detect", "explode", "-select_streams", "v:0", "-show_frames", "-show_streams", "-show_entries", "frame=pts,pkt_duration,duration,width,height:stream=time_base,width,height:stream_side_data=rotation:frame_side_data=", "-of", "compact=p=1:nk=0", path}
	log, err := runMedia(ctxSession.mediaContext, ctxSession.config, ffprobeBinary, args, func(r io.Reader) error {
		scanner := bufio.NewScanner(&boundedReader{r: r, remaining: 4*MiB + 1})
		scanner.Buffer(make([]byte, 4096), 128*1024)
		for scanner.Scan() {
			line := scanner.Text()
			fields := strings.Split(line, "|")
			if len(fields) < 2 {
				continue
			}
			values := make(map[string]string, len(fields))
			for _, field := range fields[1:] {
				k, v, ok := strings.Cut(field, "=")
				if ok {
					values[k] = v
				}
			}
			switch fields[0] {
			case "frame":
				pts, e := strconv.ParseInt(values["pts"], 10, 64)
				if e != nil {
					return failure(422, "video_timestamps_invalid", "video frames require source timestamps")
				}
				d := values["duration"]
				if d == "" {
					d = values["pkt_duration"]
				}
				duration, e := strconv.ParseInt(d, 10, 64)
				if e != nil || duration <= 0 {
					return failure(422, "video_timestamps_invalid", "video frames require positive durations")
				}
				w, we := strconv.Atoi(values["width"])
				h, he := strconv.Atoi(values["height"])
				if we != nil || he != nil || w <= 0 || h <= 0 || w > 8192 || h > 8192 || int64(w)*int64(h) > 4096*4096 {
					return failure(413, "video_resolution_limit", "video source resolution exceeds budget")
				}
				if len(m.Frames) == 0 {
					m.Width = w
					m.Height = h
				} else {
					if w != m.Width || h != m.Height {
						return failure(422, "video_dynamic_resolution", "dynamic video resolution is unsupported")
					}
					if pts <= m.Frames[len(m.Frames)-1].PTS {
						return failure(422, "video_timestamps_invalid", "video timestamps must increase")
					}
				}
				if len(m.Frames) >= remaining {
					return failure(413, "video_source_frames_limit", "decoded source frames exceed request budget")
				}
				m.Frames = append(m.Frames, sourceFrame{pts, duration})
			case "stream":
				if tb, ok := values["time_base"]; ok {
					m.TimeBase = new(big.Rat)
					if _, ok = m.TimeBase.SetString(tb); !ok || m.TimeBase.Sign() <= 0 {
						return failure(422, "video_timestamps_invalid", "invalid video time base")
					}
				}
			}
			if value, ok := values["rotation"]; ok {
				n, e := strconv.Atoi(value)
				if e != nil || n%90 != 0 {
					return failure(422, "video_rotation_invalid", "unsupported video rotation")
				}
				rotation = n
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(log) != "" {
		return nil, failure(422, "video_decode_failed", "video source contains decoding errors")
	}
	if len(m.Frames) == 0 || m.TimeBase == nil {
		return nil, failure(422, "video_empty", "video contains no timed frames")
	}
	first, last := m.Frames[0], m.Frames[len(m.Frames)-1]
	ticks := new(big.Int).Sub(big.NewInt(last.PTS), big.NewInt(first.PTS))
	ticks.Add(ticks, big.NewInt(last.Duration))
	m.Duration = new(big.Rat).Mul(new(big.Rat).SetInt(ticks), m.TimeBase)
	if m.Duration.Sign() <= 0 || m.Duration.Cmp(big.NewRat(300, 1)) > 0 {
		return nil, failure(413, "video_duration_limit", "video duration exceeds 300 seconds")
	}
	if rotation%180 != 0 {
		m.Width, m.Height = m.Height, m.Width
	}
	return m, nil
}

func halfUp(r *big.Rat) int {
	n := new(big.Int).Mul(r.Num(), big.NewInt(2))
	n.Add(n, r.Denom())
	d := new(big.Int).Mul(r.Denom(), big.NewInt(2))
	n.Quo(n, d)
	if !n.IsInt64() {
		return maxSourceFrames
	}
	return int(n.Int64())
}
func integerSqrt(n int64) int { return int(new(big.Int).Sqrt(big.NewInt(n)).Int64()) }

func desiredFrames(m *manifest, tenths int) int {
	w, h := int64(m.Width), int64(m.Height)
	nw, nh := w, h
	patches := max(1, w/14) * max(1, h/14)
	if patches > 16384 {
		nw = min(nw, int64(integerSqrt(w*w*16384/patches)))
		nh = min(nh, int64(integerSqrt(h*h*16384/patches)))
	}
	if w > 7168 {
		nw = min(nw, int64(7168))
		nh = min(nh, h*7168/w)
	}
	if h > 7168 {
		nh = min(nh, int64(7168))
		nw = min(nw, w*7168/h)
	}
	r := ((max(1, nw) + 13) / 14) * ((max(1, nh) + 13) / 14)
	count := halfUp(new(big.Rat).Mul(m.Duration, big.NewRat(int64(tenths), 10)))
	return min(len(m.Frames), max(1, count), int(655360/r))
}

func allocateFrames(desired []int, budget int) []int {
	counts := make([]int, len(desired))
	total := 0
	for i, n := range desired {
		counts[i] = 1
		total += n - 1
	}
	remaining := budget - len(desired)
	if total <= remaining {
		return append([]int(nil), desired...)
	}
	remainders := make([]int, len(desired))
	used := 0
	for i, n := range desired {
		count := (n - 1) * remaining / total
		counts[i] += count
		used += count
		remainders[i] = (n - 1) * remaining % total
	}
	for range remaining - used {
		winner := 0
		for i := range remainders {
			if remainders[i] > remainders[winner] {
				winner = i
			}
		}
		counts[winner]++
		remainders[winner] = -1
	}
	return counts
}

func indices(frames, count int) []int {
	out := make([]int, count)
	if count == 1 {
		return out
	}
	for i := range count {
		out[i] = (2*i*(frames-1) + (count - 1)) / (2 * (count - 1))
	}
	return out
}
func outputDimensions(m *manifest, side, percent int) (int, int) {
	w, h := int64(m.Width), int64(m.Height)
	if max(w, h) > int64(side) {
		long := max(w, h)
		w = max(1, w*int64(side)/long)
		h = max(1, h*int64(side)/long)
	}
	return max(1, integerSqrt(w*w*int64(percent)/100)), max(1, integerSqrt(h*h*int64(percent)/100))
}

var showPTS = regexp.MustCompile(`\bn:\s*\d+\s+pts:\s*(-?\d+)\s+pts_time:`)
var showTimeBase = regexp.MustCompile(`config in time_base:\s*(\d+/\d+)`)

func extract(s *Session, path string, m *manifest, selected []int, width, height, q int) ([]imageFrame, error) {
	beforeMemory := s.memoryBytes
	completed := false
	defer func() {
		if !completed {
			s.releaseMemory(s.memoryBytes - beforeMemory)
		}
	}()
	expressions := make([]string, len(selected))
	for i, n := range selected {
		expressions[i] = "eq(n," + strconv.Itoa(n) + ")"
	}
	filter := "select='" + strings.Join(expressions, "+") + "',showinfo=checksum=0,scale=" + strconv.Itoa(width) + ":" + strconv.Itoa(height) + ",setsar=1"
	boundary := "newapi-" + rand.Text()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "info", "-xerror", "-copyts", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,avi,mpegts,mpeg,ogg", "-threads", strconv.Itoa(s.config.FFmpegThreads), "-err_detect", "explode", "-i", path, "-map", "0:v:0", "-an", "-sn", "-dn", "-filter_threads", strconv.Itoa(s.config.FFmpegThreads), "-filter_complex_threads", strconv.Itoa(s.config.FFmpegThreads), "-vf", filter, "-fps_mode", "passthrough", "-c:v", "mjpeg", "-threads", strconv.Itoa(s.config.FFmpegThreads), "-pix_fmt", "yuvj420p", "-q:v", strconv.Itoa(q), "-f", "mpjpeg", "-boundary_tag", boundary, "pipe:1"}
	frames := make([]imageFrame, 0, len(selected))
	log, err := runMedia(s.mediaContext, s.config, ffmpegBinary, args, func(r io.Reader) error {
		reader := bufio.NewReaderSize(r, 4096)
		lineBytes, e := reader.ReadSlice('\n')
		line := string(lineBytes)
		if e != nil || line != "--"+boundary+"\r\n" {
			return failure(422, "video_output_invalid", "invalid JPEG stream boundary")
		}
		for _, index := range selected {
			length := int64(0)
			contentType := ""
			headerBytes := 0
			for {
				lineBytes, e = reader.ReadSlice('\n')
				line = string(lineBytes)
				headerBytes += len(line)
				if e != nil || headerBytes > 4096 {
					return failure(422, "video_output_invalid", "invalid JPEG stream headers")
				}
				if line == "\r\n" {
					break
				}
				key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
				if !ok {
					return failure(422, "video_output_invalid", "invalid JPEG stream headers")
				}
				value = strings.TrimSpace(value)
				switch strings.ToLower(key) {
				case "content-type":
					contentType = value
				case "content-length":
					length, e = strconv.ParseInt(value, 10, 64)
					if e != nil {
						return failure(422, "video_output_invalid", "invalid JPEG stream length")
					}
				}
			}
			if contentType != "image/jpeg" || length <= 0 || length > maxImageBytes {
				return failure(413, "video_image_limit", "extracted image exceeds budget")
			}
			if length > maxJPEGBytes-s.jpegBytes {
				return failure(413, "video_images_limit", "extracted images exceed request budget")
			}
			encoded := int64(base64.StdEncoding.EncodedLen(int(length)))
			if e = s.reserveMemory(length + 2*encoded); e != nil {
				return e
			}
			data := make([]byte, length)
			_, e = io.ReadFull(reader, data)
			if e != nil {
				s.releaseMemory(length + 2*encoded)
				return failure(422, "video_output_invalid", "truncated JPEG image")
			}
			config, e := jpeg.DecodeConfig(bytes.NewReader(data))
			if e != nil || config.Width != width || config.Height != height {
				s.releaseMemory(length + 2*encoded)
				return failure(422, "video_output_invalid", "invalid extracted JPEG dimensions")
			}
			frame := imageFrame{PTS: m.Frames[index].PTS, Data: base64.StdEncoding.EncodeToString(data)}
			s.releaseMemory(length + encoded)
			s.jpegBytes += length
			globalPool.mu.Lock()
			globalPool.stats.GeneratedJPEGBytes += length
			globalPool.mu.Unlock()
			frames = append(frames, frame)
			lineBytes, e = reader.ReadSlice('\n')
			line = string(lineBytes)
			if e != nil || line != "\r\n" {
				return failure(422, "video_output_invalid", "invalid JPEG stream separator")
			}
			lineBytes, e = reader.ReadSlice('\n')
			line = string(lineBytes)
			if e != nil || line != "--"+boundary+"\r\n" {
				return failure(422, "video_output_invalid", "invalid JPEG stream boundary")
			}
		}
		if _, e = reader.Peek(1); e != io.EOF {
			return failure(422, "video_output_invalid", "unexpected extra extracted images")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	pts := showPTS.FindAllStringSubmatch(log, -1)
	tb := showTimeBase.FindStringSubmatch(log)
	if len(pts) != len(selected) || len(tb) != 2 {
		return nil, failure(422, "video_timestamps_invalid", "extracted frame manifest mismatch")
	}
	actual, ok := new(big.Rat).SetString(tb[1])
	if !ok || actual.Cmp(m.TimeBase) != 0 {
		return nil, failure(422, "video_timestamps_invalid", "extracted frame time base mismatch")
	}
	for i, p := range pts {
		value, e := strconv.ParseInt(p[1], 10, 64)
		if e != nil || value != m.Frames[selected[i]].PTS {
			return nil, failure(422, "video_timestamps_invalid", "extracted source frame timestamp mismatch")
		}
	}
	completed = true
	return frames, nil
}

func timestamp(pts int64, tb *big.Rat) string {
	r := new(big.Rat).Mul(big.NewRat(pts, 1), tb)
	r.Mul(r, big.NewRat(1000, 1))
	negative := r.Sign() < 0
	if negative {
		r.Neg(r)
	}
	ms := new(big.Int).Mul(r.Num(), big.NewInt(2))
	ms.Add(ms, r.Denom())
	ms.Quo(ms, new(big.Int).Mul(r.Denom(), big.NewInt(2)))
	sign := ""
	if negative {
		sign = "-"
	}
	hours, rest := new(big.Int), new(big.Int)
	hours.QuoRem(ms, big.NewInt(3600000), rest)
	value := rest.Int64()
	return fmt.Sprintf("%s%02s:%02d:%02d.%03d", sign, hours.String(), value/60000, value/1000%60, value%1000)
}
