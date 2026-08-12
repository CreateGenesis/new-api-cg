package common

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type failingEventWriter struct {
	header http.Header
	err    error
}

func (w *failingEventWriter) Header() http.Header {
	return w.header
}

func (w *failingEventWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func (w *failingEventWriter) WriteHeader(int) {}

func TestCustomEventRenderReturnsWriteError(t *testing.T) {
	connectionReset := errors.New("write: connection reset by peer")
	writer := &failingEventWriter{header: make(http.Header), err: connectionReset}

	err := (CustomEvent{Data: "data: payload"}).Render(writer)

	require.ErrorIs(t, err, connectionReset)
}
