package model

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRelayDebugLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousLogDB := LOG_DB
	previousLogDatabaseType := common.LogDatabaseType()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&RelayDebugPayload{}))
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogDatabaseType)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestRelayDebugPayloadRoundTripAndChunkReassembly(t *testing.T) {
	db := setupRelayDebugLogDB(t)
	ctx := context.Background()
	payload := []byte(`{"request_id":"round-trip","attempts":[{"error":"busy"}]}`)

	require.NoError(t, SaveRelayDebugPayload(ctx, "round-trip", payload))
	loaded, err := LoadRelayDebugPayload(ctx, "round-trip")
	require.NoError(t, err)
	assert.Equal(t, payload, loaded)

	chunkedPayload := []byte(`{"request_id":"chunked","client":{"body":"complete request"}}`)
	var compressed bytes.Buffer
	zipWriter := gzip.NewWriter(&compressed)
	_, err = zipWriter.Write(chunkedPayload)
	require.NoError(t, err)
	require.NoError(t, zipWriter.Close())
	compressedBytes := compressed.Bytes()
	splitAt := len(compressedBytes) / 2
	checksum := fmt.Sprintf("%x", sha256.Sum256(chunkedPayload))
	rows := []RelayDebugPayload{
		{RequestId: "chunked", CreatedAt: 1, ChunkIndex: 0, ChunkCount: 2, Encoding: "gzip", UncompressedSize: int64(len(chunkedPayload)), Checksum: checksum, Payload: append([]byte(nil), compressedBytes[:splitAt]...)},
		{RequestId: "chunked", CreatedAt: 1, ChunkIndex: 1, ChunkCount: 2, Encoding: "gzip", UncompressedSize: int64(len(chunkedPayload)), Checksum: checksum, Payload: append([]byte(nil), compressedBytes[splitAt:]...)},
	}
	require.NoError(t, db.Create(&rows).Error)

	loaded, err = LoadRelayDebugPayload(ctx, "chunked")
	require.NoError(t, err)
	assert.Equal(t, chunkedPayload, loaded)
}

func TestRelayDebugPayloadRejectsCorruption(t *testing.T) {
	db := setupRelayDebugLogDB(t)
	ctx := context.Background()
	require.NoError(t, SaveRelayDebugPayload(ctx, "corrupted", []byte(`{"request_id":"corrupted"}`)))
	require.NoError(t, db.Model(&RelayDebugPayload{}).
		Where("request_id = ? AND chunk_index = ?", "corrupted", 0).
		Update("payload", []byte("not-gzip-data")).Error)

	_, err := LoadRelayDebugPayload(ctx, "corrupted")

	assert.ErrorIs(t, err, ErrRelayDebugTraceUnavailable)
}

func TestDeleteOldRelayDebugPayloadsUsesLogRetentionBoundary(t *testing.T) {
	db := setupRelayDebugLogDB(t)
	rows := []RelayDebugPayload{
		{RequestId: "old", CreatedAt: 99, ChunkIndex: 0, ChunkCount: 1, Encoding: "gzip", UncompressedSize: 1, Checksum: "old", Payload: []byte("old")},
		{RequestId: "boundary", CreatedAt: 100, ChunkIndex: 0, ChunkCount: 1, Encoding: "gzip", UncompressedSize: 1, Checksum: "boundary", Payload: []byte("boundary")},
		{RequestId: "new", CreatedAt: 101, ChunkIndex: 0, ChunkCount: 1, Encoding: "gzip", UncompressedSize: 1, Checksum: "new", Payload: []byte("new")},
	}
	require.NoError(t, db.Create(&rows).Error)

	require.NoError(t, deleteOldRelayDebugPayloads(context.Background(), 100))

	var remaining []RelayDebugPayload
	require.NoError(t, db.Order("created_at asc").Find(&remaining).Error)
	require.Len(t, remaining, 2)
	assert.Equal(t, "boundary", remaining[0].RequestId)
	assert.Equal(t, "new", remaining[1].RequestId)
}
