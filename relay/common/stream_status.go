package common

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type StreamEndReason string

const (
	StreamEndReasonNone        StreamEndReason = ""
	StreamEndReasonDone        StreamEndReason = "done"
	StreamEndReasonTimeout     StreamEndReason = "timeout"
	StreamEndReasonClientGone  StreamEndReason = "client_gone"
	StreamEndReasonScannerErr  StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop StreamEndReason = "handler_stop"
	StreamEndReasonEOF         StreamEndReason = "eof"
	StreamEndReasonPanic       StreamEndReason = "panic"
	StreamEndReasonPingFail    StreamEndReason = "ping_fail"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error
	endOnce   sync.Once

	mu                  sync.Mutex
	Errors              []StreamErrorEntry
	ErrorCount          int
	ProtocolEndRequired bool
	ProtocolEndReceived bool
	ProtocolEndEvent    string
}

type StreamStatusSnapshot struct {
	EndReason           StreamEndReason
	EndError            error
	Errors              []StreamErrorEntry
	ErrorCount          int
	ProtocolEndRequired bool
	ProtocolEndReceived bool
	ProtocolEndEvent    string
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.EndReason = reason
		s.EndError = err
	})
}

func (s *StreamStatus) RequireProtocolEnd() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ProtocolEndRequired = true
	s.mu.Unlock()
}

func (s *StreamStatus) MarkProtocolEnd(event string) {
	if s == nil || strings.TrimSpace(event) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ProtocolEndReceived && s.ProtocolEndEvent != "[DONE]" {
		return
	}
	s.ProtocolEndReceived = true
	s.ProtocolEndEvent = event
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount > 0
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	snapshot := s.Snapshot()
	if snapshot.ProtocolEndRequired && !snapshot.ProtocolEndReceived {
		return false
	}
	switch snapshot.EndReason {
	case StreamEndReasonDone, StreamEndReasonEOF:
		return true
	case StreamEndReasonClientGone:
		return snapshot.ProtocolEndRequired && snapshot.ProtocolEndReceived
	default:
		return false
	}
}

func (s *StreamStatus) IsInterrupted() bool {
	if s == nil {
		return false
	}
	return !s.IsNormalEnd() || s.HasErrors()
}

func (s *StreamStatus) Snapshot() StreamStatusSnapshot {
	if s == nil {
		return StreamStatusSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := StreamStatusSnapshot{
		EndReason:           s.EndReason,
		EndError:            s.EndError,
		ErrorCount:          s.ErrorCount,
		ProtocolEndRequired: s.ProtocolEndRequired,
		ProtocolEndReceived: s.ProtocolEndReceived,
		ProtocolEndEvent:    s.ProtocolEndEvent,
	}
	snapshot.Errors = append([]StreamErrorEntry(nil), s.Errors...)
	return snapshot
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	snapshot := s.Snapshot()
	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", snapshot.EndReason)
	if snapshot.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", snapshot.EndError.Error())
	}
	if snapshot.ProtocolEndRequired {
		fmt.Fprintf(b, " protocol_end_received=%t", snapshot.ProtocolEndReceived)
		if snapshot.ProtocolEndEvent != "" {
			fmt.Fprintf(b, " protocol_end_event=%q", snapshot.ProtocolEndEvent)
		}
	}
	if snapshot.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", snapshot.ErrorCount)
	}
	return b.String()
}
