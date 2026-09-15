package common

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponseModelName is independent of the model used for routing and billing.
// An empty result means the channel has not opted into response model rewriting.
func (info *RelayInfo) ResponseModelName() string {
	if info == nil || info.ChannelMeta == nil {
		return ""
	}
	setting := info.ChannelOtherSettings.ResponseModelMapping
	if setting == nil || !setting.Enabled {
		return ""
	}
	requested := info.RequestedModelName
	if requested == "" {
		requested = info.UpstreamAttemptModelName()
	}
	if strings.TrimSpace(requested) == "" {
		return ""
	}
	if target := setting.Mapping[requested]; strings.TrimSpace(target) != "" {
		return target
	}
	return requested
}

// RewriteResponseModelJSON patches only protocol model metadata. It never walks
// generated content, tool arguments, or arbitrary nested provider extensions.
func (info *RelayInfo) RewriteResponseModelJSON(data []byte) []byte {
	name := info.ResponseModelName()
	if name == "" || !gjson.ValidBytes(data) {
		return data
	}
	root := gjson.ParseBytes(data)
	if root.Get("error").Exists() && root.Get("error").Type != gjson.Null {
		return data
	}
	if root.Get("type").String() == "error" || strings.HasSuffix(root.Get("type").String(), ".error") || strings.HasSuffix(root.Get("type").String(), ".failed") {
		return data
	}
	paths := []string{"model"}
	switch info.RelayFormat {
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		paths = append(paths, "response.model")
	case types.RelayFormatClaude:
		paths = append(paths, "message.model")
	case types.RelayFormatGemini:
		paths = append(paths, "modelVersion")
		if root.IsArray() {
			paths = nil
			for index := range root.Array() {
				prefix := strconv.Itoa(index)
				paths = append(paths, prefix+".model", prefix+".modelVersion")
			}
		}
	case types.RelayFormatOpenAIRealtime:
		paths = append(paths, "session.model", "response.model")
	case types.RelayFormatTask:
		paths = append(paths, "response.model", "data.model", "data.data.model")
	}
	patched := data
	for _, path := range paths {
		value := gjson.GetBytes(patched, path)
		if value.Type != gjson.String || value.String() == name {
			continue
		}
		var err error
		patched, err = sjson.SetBytes(patched, path, name)
		if err != nil {
			common.SysError("failed to rewrite response model: " + err.Error())
			return data
		}
	}
	return patched
}

// ResponseModelWriter is installed outside the per-attempt output policies, so
// it sees the final client protocol, including synthesized usage and conversions.
// Complete JSON writes are patched without buffering; SSE retains one event.
type ResponseModelWriter struct {
	gin.ResponseWriter
	context *gin.Context
	info    *RelayInfo
	pending []byte
	err     error
}

func WrapResponseModelWriter(c *gin.Context, info *RelayInfo) *ResponseModelWriter {
	if info.ResponseModelName() == "" {
		return nil
	}
	w := &ResponseModelWriter{ResponseWriter: c.Writer, context: c, info: info}
	c.Writer = w
	return w
}

func (w *ResponseModelWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *ResponseModelWriter) canRewrite(status int) bool {
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	return status < http.StatusBadRequest && (contentType == "" || strings.Contains(contentType, "json") || strings.Contains(contentType, "text/event-stream"))
}

func (w *ResponseModelWriter) WriteHeader(code int) {
	// A changed payload must never retain the upstream byte count.
	if w.canRewrite(code) {
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *ResponseModelWriter) WriteHeaderNow() {
	if w.canRewrite(w.Status()) {
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *ResponseModelWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if !w.canRewrite(w.Status()) {
		return w.ResponseWriter.Write(data)
	}
	w.Header().Del("Content-Length")
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		w.pending = append(w.pending, data...)
		for {
			end, delimiter := bytes.Index(w.pending, []byte("\n\n")), 2
			if crlf := bytes.Index(w.pending, []byte("\r\n\r\n")); crlf >= 0 && (end < 0 || crlf < end) {
				end, delimiter = crlf, 4
			}
			if end < 0 {
				break
			}
			event := w.pending[:end+delimiter]
			if err := w.write(w.rewriteEvent(event)); err != nil {
				return 0, err
			}
			w.pending = w.pending[end+delimiter:]
		}
	} else if len(w.pending) == 0 && gjson.ValidBytes(data) {
		if err := w.write(w.info.RewriteResponseModelJSON(data)); err != nil {
			return 0, err
		}
	} else {
		w.pending = append(w.pending, data...)
		if gjson.ValidBytes(w.pending) {
			if err := w.write(w.info.RewriteResponseModelJSON(w.pending)); err != nil {
				return 0, err
			}
			w.pending = nil
		}
	}
	return len(data), nil
}

func (w *ResponseModelWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

func (w *ResponseModelWriter) write(data []byte) error {
	var n int
	n, w.err = w.ResponseWriter.Write(data)
	if w.err == nil && n != len(data) {
		w.err = io.ErrShortWrite
	}
	return w.err
}

func (w *ResponseModelWriter) rewriteEvent(event []byte) []byte {
	newline := "\n"
	if bytes.Contains(event, []byte("\r\n")) {
		newline = "\r\n"
	}
	lines := strings.Split(string(event), newline)
	var dataLines []string
	first := -1
	for i, line := range lines {
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			if first < 0 {
				first = i
			}
			dataLines = append(dataLines, strings.TrimPrefix(data, " "))
		}
	}
	if first < 0 {
		return event
	}
	data := []byte(strings.Join(dataLines, "\n"))
	patched := w.info.RewriteResponseModelJSON(data)
	if bytes.Equal(data, patched) {
		return event
	}
	var result strings.Builder
	for i, line := range lines {
		if i > 0 {
			result.WriteString(newline)
		}
		if i == first {
			result.WriteString("data: ")
			result.WriteString(strings.ReplaceAll(string(patched), "\n", newline+"data: "))
		} else if strings.HasPrefix(line, "data:") {
			// Retain a comment in place of additional data lines, preserving framing.
			result.WriteString(":")
		} else {
			result.WriteString(line)
		}
	}
	return []byte(result.String())
}

// Finish flushes any trailing data and restores the original writer.
// A nil receiver represents a channel with rewriting disabled.
func (w *ResponseModelWriter) Finish() error {
	if w == nil {
		return nil
	}
	defer func() { w.context.Writer = w.ResponseWriter }()
	if w.err == nil && len(w.pending) > 0 {
		data := w.pending
		w.pending = nil
		if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
			data = w.rewriteEvent(data)
		} else {
			data = w.info.RewriteResponseModelJSON(data)
		}
		return w.write(data)
	}
	return w.err
}
