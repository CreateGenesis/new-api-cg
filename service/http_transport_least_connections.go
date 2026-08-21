package service

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// leastConnectionsRoundTripper distributes requests across proxy transports
// using the number of requests whose response bodies are still open. Keeping
// the reservation until Body.Close makes streaming requests count for their
// full lifetime, while HTTP/2 streams remain independently load-balanced.
type leastConnectionsRoundTripper struct {
	transports []http.RoundTripper
	active     []atomic.Int64
	selectMu   sync.Mutex
	next       uint64
}

func newLeastConnectionsRoundTripper(transports []http.RoundTripper) *leastConnectionsRoundTripper {
	return &leastConnectionsRoundTripper{
		transports: transports,
		active:     make([]atomic.Int64, len(transports)),
	}
}

func (t *leastConnectionsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	index := t.reserve()
	resp, err := t.transports[index].RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		t.release(index)
		return resp, err
	}
	resp.Body = &leastConnectionsBody{
		ReadCloser: resp.Body,
		release: func() {
			t.release(index)
		},
	}
	return resp, nil
}

func (t *leastConnectionsRoundTripper) reserve() int {
	t.selectMu.Lock()
	defer t.selectMu.Unlock()

	start := int(t.next % uint64(len(t.transports)))
	t.next++
	selected := start
	minimum := t.active[selected].Load()
	for offset := 0; offset < len(t.transports); offset++ {
		index := (start + offset) % len(t.transports)
		count := t.active[index].Load()
		if count < minimum {
			selected = index
			minimum = count
		}
	}
	t.active[selected].Add(1)
	return selected
}

func (t *leastConnectionsRoundTripper) release(index int) {
	t.active[index].Add(-1)
}

func (t *leastConnectionsRoundTripper) CloseIdleConnections() {
	for _, transport := range t.transports {
		closeIdleConnections(transport)
	}
}

type leastConnectionsBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *leastConnectionsBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
