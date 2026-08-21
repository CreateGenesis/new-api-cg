package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type leastConnectionsTestTransport struct {
	started chan struct{}
	release chan struct{}
	err     error
	mu      sync.Mutex
	calls   int
}

func (t *leastConnectionsTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	if t.started != nil {
		t.started <- struct{}{}
	}
	if t.release != nil {
		<-t.release
	}
	if t.err != nil {
		return nil, t.err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
	}, nil
}

func (t *leastConnectionsTestTransport) callCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func TestLeastConnectionsRoundTripperPrefersIdleProxy(t *testing.T) {
	first := &leastConnectionsTestTransport{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	second := &leastConnectionsTestTransport{}
	roundTripper := newLeastConnectionsRoundTripper([]http.RoundTripper{first, second})
	request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	firstResponseCh := make(chan *http.Response, 1)
	firstErrorCh := make(chan error, 1)
	go func() {
		firstResponse, firstErr := roundTripper.RoundTrip(request)
		firstResponseCh <- firstResponse
		firstErrorCh <- firstErr
	}()
	<-first.started

	secondResponse, err := roundTripper.RoundTrip(request)
	require.NoError(t, err)
	require.NotNil(t, secondResponse)
	assert.Equal(t, 1, second.callCount())
	assert.Equal(t, 1, first.callCount())

	require.NoError(t, secondResponse.Body.Close())
	close(first.release)
	firstResponse := <-firstResponseCh
	firstErr := <-firstErrorCh
	assert.NoError(t, firstErr)
	require.NotNil(t, firstResponse)
	require.NoError(t, firstResponse.Body.Close())
	assert.Equal(t, int64(0), roundTripper.active[0].Load())
	assert.Equal(t, int64(0), roundTripper.active[1].Load())
}

func TestLeastConnectionsRoundTripperReleasesOnError(t *testing.T) {
	transportError := errors.New("proxy unavailable")
	roundTripper := newLeastConnectionsRoundTripper([]http.RoundTripper{
		&leastConnectionsTestTransport{err: transportError},
	})
	request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	_, err = roundTripper.RoundTrip(request)
	require.ErrorIs(t, err, transportError)
	assert.Equal(t, int64(0), roundTripper.active[0].Load())
}

func TestGetHttpClientWithProxySettingsBuildsProxyPool(t *testing.T) {
	ResetProxyClientCache()
	t.Cleanup(ResetProxyClientCache)

	client, err := GetHttpClientWithProxySettings(
		"socks5://proxy-a:1080\nsocks5h://proxy-b:1080",
		dto.ChannelSettings{},
	)
	require.NoError(t, err)
	pool, ok := client.Transport.(*leastConnectionsRoundTripper)
	require.True(t, ok)
	assert.Len(t, pool.transports, 2)

	InvalidateProxyClient("socks5://proxy-a:1080\nsocks5h://proxy-b:1080")
	newClient, err := GetHttpClientWithProxySettings(
		"socks5://proxy-a:1080\nsocks5h://proxy-b:1080",
		dto.ChannelSettings{},
	)
	require.NoError(t, err)
	assert.NotSame(t, client, newClient)
}
