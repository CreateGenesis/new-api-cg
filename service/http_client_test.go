package service

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func rejectingSOCKSProxyURL(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, listener.Close())
	})

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	return "socks5://" + listener.Addr().String()
}

func TestSOCKSProxyDirectFallback(t *testing.T) {
	ResetProxyClientCache()
	t.Cleanup(ResetProxyClientCache)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	proxyURL := rejectingSOCKSProxyURL(t)
	proxyOnlyClient, err := NewProxyHttpClientWithFallback(proxyURL, false)
	require.NoError(t, err)

	resp, err := proxyOnlyClient.Get(target.URL)
	require.Error(t, err)
	require.Nil(t, resp)

	fallbackClient, err := NewProxyHttpClientWithFallback(proxyURL, true)
	require.NoError(t, err)
	require.NotSame(t, proxyOnlyClient, fallbackClient)

	resp, err = fallbackClient.Get(target.URL)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	cachedFallbackClient, err := NewProxyHttpClientWithFallback(proxyURL, true)
	require.NoError(t, err)
	require.Same(t, fallbackClient, cachedFallbackClient)
}
