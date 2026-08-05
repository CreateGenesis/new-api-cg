package common

import (
	"bytes"
	"io"
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
