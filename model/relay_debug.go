package model

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	relayDebugChunkSize            = 1 << 20
	maxRelayDebugUncompressedBytes = 132 << 20
)

var (
	ErrRelayDebugTraceNotFound    = errors.New("relay debug trace not found")
	ErrRelayDebugTraceUnavailable = errors.New("relay debug trace unavailable")
)

type RelayDebugPayload struct {
	RequestId        string `gorm:"primaryKey;type:varchar(64)"`
	CreatedAt        int64  `gorm:"bigint;index"`
	ChunkIndex       int    `gorm:"primaryKey"`
	ChunkCount       int
	Encoding         string `gorm:"type:varchar(16)"`
	UncompressedSize int64
	Checksum         string `gorm:"type:varchar(64)"`
	Payload          []byte
}

func (RelayDebugPayload) TableName() string {
	return "relay_debug_payloads"
}

func SaveRelayDebugPayload(ctx context.Context, requestId string, payload []byte) error {
	if LOG_DB == nil {
		return errors.New("log database is not initialized")
	}
	if requestId == "" {
		return errors.New("request id is empty")
	}
	if len(payload) == 0 || len(payload) > maxRelayDebugUncompressedBytes {
		return fmt.Errorf("invalid relay debug payload size: %d", len(payload))
	}

	var compressed bytes.Buffer
	zipWriter := gzip.NewWriter(&compressed)
	if _, err := zipWriter.Write(payload); err != nil {
		return err
	}
	if err := zipWriter.Close(); err != nil {
		return err
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256(payload))
	compressedBytes := compressed.Bytes()
	chunkCount := (len(compressedBytes) + relayDebugChunkSize - 1) / relayDebugChunkSize
	createdAt := time.Now().Unix()
	rows := make([]RelayDebugPayload, 0, chunkCount)
	for index := 0; index < chunkCount; index++ {
		start := index * relayDebugChunkSize
		end := start + relayDebugChunkSize
		if end > len(compressedBytes) {
			end = len(compressedBytes)
		}
		rows = append(rows, RelayDebugPayload{
			RequestId:        requestId,
			CreatedAt:        createdAt,
			ChunkIndex:       index,
			ChunkCount:       chunkCount,
			Encoding:         "gzip",
			UncompressedSize: int64(len(payload)),
			Checksum:         checksum,
			Payload:          append([]byte(nil), compressedBytes[start:end]...),
		})
	}

	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		if err := LOG_DB.WithContext(ctx).Create(&rows).Error; err != nil {
			return err
		}
		manifest := RelayDebugPayload{
			RequestId:        requestId,
			CreatedAt:        createdAt,
			ChunkIndex:       -1,
			ChunkCount:       chunkCount,
			Encoding:         "gzip",
			UncompressedSize: int64(len(payload)),
			Checksum:         checksum,
		}
		return LOG_DB.WithContext(ctx).Create(&manifest).Error
	}

	return LOG_DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Create(&rows).Error
	})
}

func LoadRelayDebugPayload(ctx context.Context, requestId string) ([]byte, error) {
	if LOG_DB == nil || requestId == "" {
		return nil, ErrRelayDebugTraceNotFound
	}

	var rows []RelayDebugPayload
	query := LOG_DB.WithContext(ctx).Where("request_id = ?", requestId)
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		var manifest RelayDebugPayload
		if err := query.Where("chunk_index = ?", -1).Order("created_at desc").First(&manifest).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrRelayDebugTraceNotFound
			}
			return nil, err
		}
		if err := LOG_DB.WithContext(ctx).Where("request_id = ? AND chunk_index >= 0", requestId).Order("chunk_index asc").Find(&rows).Error; err != nil {
			return nil, err
		}
		if err := validateRelayDebugChunks(rows, manifest.ChunkCount, manifest.Encoding, manifest.UncompressedSize, manifest.Checksum); err != nil {
			return nil, err
		}
	} else {
		if err := query.Order("chunk_index asc").Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, ErrRelayDebugTraceNotFound
		}
		first := rows[0]
		if err := validateRelayDebugChunks(rows, first.ChunkCount, first.Encoding, first.UncompressedSize, first.Checksum); err != nil {
			return nil, err
		}
	}

	var compressed bytes.Buffer
	for _, row := range rows {
		compressed.Write(row.Payload)
	}
	reader, err := gzip.NewReader(&compressed)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid gzip data", ErrRelayDebugTraceUnavailable)
	}
	defer reader.Close()

	expectedSize := rows[0].UncompressedSize
	decoded, err := io.ReadAll(io.LimitReader(reader, maxRelayDebugUncompressedBytes+1))
	if err != nil || int64(len(decoded)) != expectedSize || len(decoded) > maxRelayDebugUncompressedBytes {
		return nil, fmt.Errorf("%w: invalid uncompressed size", ErrRelayDebugTraceUnavailable)
	}
	if fmt.Sprintf("%x", sha256.Sum256(decoded)) != rows[0].Checksum {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrRelayDebugTraceUnavailable)
	}
	return decoded, nil
}

func validateRelayDebugChunks(rows []RelayDebugPayload, chunkCount int, encoding string, uncompressedSize int64, checksum string) error {
	if chunkCount <= 0 || len(rows) != chunkCount || encoding != "gzip" || uncompressedSize <= 0 || uncompressedSize > maxRelayDebugUncompressedBytes || checksum == "" {
		return ErrRelayDebugTraceUnavailable
	}
	for index, row := range rows {
		if row.ChunkIndex != index || row.ChunkCount != chunkCount || row.Encoding != encoding || row.UncompressedSize != uncompressedSize || row.Checksum != checksum {
			return ErrRelayDebugTraceUnavailable
		}
	}
	return nil
}

func deleteOldRelayDebugPayloads(ctx context.Context, targetTimestamp int64) error {
	if LOG_DB == nil {
		return nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return LOG_DB.WithContext(ctx).Exec(
			"ALTER TABLE relay_debug_payloads DELETE WHERE created_at < ? SETTINGS mutations_sync = 1",
			targetTimestamp,
		).Error
	}
	return LOG_DB.WithContext(ctx).Where("created_at < ?", targetTimestamp).Delete(&RelayDebugPayload{}).Error
}
