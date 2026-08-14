package common

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateBodyStorageFromReaderSpoolsUnknownLargeBodyToDisk(t *testing.T) {
	originalConfig := GetDiskCacheConfig()
	SetDiskCacheConfig(DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: 1,
		MaxSizeMB:   32,
		Path:        t.TempDir(),
	})
	ResetDiskCacheUsage()
	t.Cleanup(func() {
		SetDiskCacheConfig(originalConfig)
		ResetDiskCacheUsage()
	})

	body := bytes.Repeat([]byte("x"), (2<<20)+17)
	storage, err := CreateBodyStorageFromReader(bytes.NewReader(body), -1, 4<<20)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })

	assert.True(t, storage.IsDisk())
	assert.Equal(t, int64(len(body)), storage.Size())
	storedBody, err := io.ReadAll(storage)
	require.NoError(t, err)
	assert.Equal(t, body, storedBody)
}

func TestCreateBodyStorageFromReaderKeepsSmallUnknownBodyInMemory(t *testing.T) {
	originalConfig := GetDiskCacheConfig()
	SetDiskCacheConfig(DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: 1,
		MaxSizeMB:   32,
		Path:        t.TempDir(),
	})
	ResetDiskCacheUsage()
	t.Cleanup(func() {
		SetDiskCacheConfig(originalConfig)
		ResetDiskCacheUsage()
	})

	body := bytes.Repeat([]byte("x"), 64<<10)
	storage, err := CreateBodyStorageFromReader(bytes.NewReader(body), -1, 4<<20)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })

	assert.False(t, storage.IsDisk())
	assert.Equal(t, int64(len(body)), storage.Size())
}

func TestNewReplayableBodyReaderKeepsStorageLifecycleWithCaller(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":"hello"}`)
	storage, err := CreateBodyStorage(payload)
	require.NoError(t, err)
	defer storage.Close()

	body := NewReplayableBodyReader(storage)
	assert.EqualValues(t, len(payload), body.Size())
	_, exposesCloser := any(body).(io.Closer)
	assert.False(t, exposesCloser, "the request body must not expose the storage closer")

	req, err := http.NewRequest(http.MethodPost, "https://example.com", body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())

	replayBody, err := body.NewReader()
	require.NoError(t, err, "closing the HTTP request body must not close the storage")
	replay, err := io.ReadAll(replayBody)
	require.NoError(t, err)
	require.NoError(t, replayBody.Close())
	assert.Equal(t, payload, replay)

	require.NoError(t, storage.Close())
	_, err = body.NewReader()
	require.ErrorIs(t, err, ErrStorageClosed)
}
