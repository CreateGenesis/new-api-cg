package common

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProxyURLsRuntimeNormalizesEntries(t *testing.T) {
	proxyURLs, legacySuffixStripped, err := ParseProxyURLsRuntime(
		" socks5://user:pass@proxy-a \r\n\n socks5h://proxy-b:1080/path \n",
	)
	require.NoError(t, err)
	require.Len(t, proxyURLs, 2)
	assert.Equal(t, "socks5://user:pass@proxy-a:1080", proxyURLs[0].String())
	assert.Equal(t, "socks5h://proxy-b:1080", proxyURLs[1].String())
	assert.True(t, legacySuffixStripped)
}

func TestParseProxyURLsStrictRejectsInvalidEntry(t *testing.T) {
	_, err := ParseProxyURLsStrict("socks5://proxy-a:1080\nftp://proxy-b:21")
	require.Error(t, err)
}

func TestParseProxyURLsRuntimeDeduplicatesCanonicalEntries(t *testing.T) {
	proxyURLs, _, err := ParseProxyURLsRuntime(
		"socks5://proxy-a:1080\nsocks5://proxy-a:1080/\n",
	)
	require.NoError(t, err)
	assert.Len(t, proxyURLs, 1)
}

func TestParseProxyURLsStrictRejectsTooManyEntries(t *testing.T) {
	raw := ""
	for i := 0; i < MaxProxyURLs+1; i++ {
		if i > 0 {
			raw += "\n"
		}
		raw += fmt.Sprintf("socks5://proxy-%d:1080", i)
	}

	_, err := ParseProxyURLsStrict(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most")
}
