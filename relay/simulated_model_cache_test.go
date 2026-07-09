package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryServeSimulatedModelCacheReplaySkipsWhenRedisDisabled(t *testing.T) {
	oldRedisEnabled := common.RedisEnabled
	oldRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRDB
	})

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         7,
			UpstreamModelName: "gpt-test",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				SimulatedModelCache: &dto.SimulatedModelCacheSettings{
					Enabled: true,
				},
			},
		},
	}

	attempt, hit := tryServeSimulatedModelCacheReplay(c, info, []byte(`{"messages":[]}`))

	require.False(t, hit)
	assert.Nil(t, attempt)
}

func TestSimulatedModelCacheSettingsAllowsExactReplayOnly(t *testing.T) {
	exactReplayEnabled := true
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{
				SimulatedModelCache: &dto.SimulatedModelCacheSettings{
					Enabled:            false,
					ExactReplayEnabled: &exactReplayEnabled,
				},
			},
		},
	}

	settings, ok := simulatedModelCacheSettings(info)

	require.True(t, ok)
	assert.False(t, settings.Enabled)
	assert.True(t, settings.IsExactReplayEnabled())
}

func TestSimulatedModelCacheRecorderPassesThroughStreamWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	attempt := &simulatedModelCacheAttempt{
		requestBody: []byte(`{"stream":true}`),
		promptText:  "hello",
	}
	info := &relaycommon.RelayInfo{IsStream: true}

	recorder := beginSimulatedModelCacheRecorder(c, info, attempt)
	require.NotNil(t, recorder)

	_, err := c.Writer.Write([]byte("data: one\n\n"))
	require.NoError(t, err)
	c.Writer.Flush()

	assert.Equal(t, "data: one\n\n", w.Body.String())
	assert.Equal(t, "data: one\n\n", recorder.body.String())
}

func TestSimulatedModelCacheRecorderRecordsSSEEventDelays(t *testing.T) {
	recorder := &simulatedModelCacheRecorder{
		passThrough:       true,
		lastStreamEventAt: time.Now().Add(-5 * time.Millisecond),
	}

	recorder.recordStreamWrite([]byte("data: one\n\ndata: two\n\npartial"))
	recorder.finishStreamDelays()

	require.Len(t, recorder.streamDelaysMS, 3)
	assert.GreaterOrEqual(t, recorder.streamDelaysMS[0], int64(4))
	assert.Equal(t, int64(0), recorder.streamDelaysMS[1])
	assert.Equal(t, int64(0), recorder.streamDelaysMS[2])
}

func TestSplitSimulatedModelCacheSSEChunksPreservesEventBoundaries(t *testing.T) {
	chunks := splitSimulatedModelCacheSSEChunks([]byte("data: one\r\n\r\ndata: two\n\ntrail"))

	require.Len(t, chunks, 3)
	assert.Equal(t, []byte("data: one\r\n\r\n"), chunks[0])
	assert.Equal(t, []byte("data: two\n\n"), chunks[1])
	assert.Equal(t, []byte("trail"), chunks[2])
}

func TestSimulatedModelCacheRecorderNormalizesInvalidStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	attempt := &simulatedModelCacheAttempt{
		requestBody: []byte(`{"stream":true}`),
		promptText:  "hello",
	}

	recorder := beginSimulatedModelCacheRecorder(c, &relaycommon.RelayInfo{IsStream: true}, attempt)
	require.NotNil(t, recorder)

	c.Writer.WriteHeader(-1)

	assert.Equal(t, http.StatusOK, recorder.Status())
}

func TestFlushSimulatedModelCacheRecorderRewritesContentLength(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("Content-Length", "999")
	recorder := &simulatedModelCacheRecorder{
		ResponseWriter: c.Writer,
		status:         http.StatusOK,
	}

	flushSimulatedModelCacheRecorder(recorder, []byte("short"))

	assert.Equal(t, "5", w.Header().Get("Content-Length"))
	assert.Equal(t, "short", w.Body.String())
}

func TestWriteSimulatedModelCacheResponseReplaysStreamWithRecordedDelays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte("data: one\n\ndata: two\n\n")

	startedAt := time.Now()
	writeSimulatedModelCacheResponse(c, service.SimulatedModelCacheResponse{
		StatusCode:     http.StatusOK,
		ContentType:    "text/event-stream",
		StreamDelaysMS: []int64{5, 5},
	}, body)

	assert.GreaterOrEqual(t, time.Since(startedAt), 8*time.Millisecond)
	assert.Empty(t, w.Header().Get("Content-Length"))
	assert.Equal(t, body, w.Body.Bytes())
}

func TestWriteSimulatedModelCacheResponseReplaysLegacyStreamWithDurationFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte("data: one\n\ndata: two\n\n")

	startedAt := time.Now()
	writeSimulatedModelCacheResponse(c, service.SimulatedModelCacheResponse{
		StatusCode:      http.StatusOK,
		ContentType:     "text/event-stream",
		DurationSeconds: 0.01,
	}, body)

	assert.GreaterOrEqual(t, time.Since(startedAt), 8*time.Millisecond)
	assert.Empty(t, w.Header().Get("Content-Length"))
	assert.Equal(t, body, w.Body.Bytes())
}

func TestSimulatedModelCacheStreamDelayKeepsRecordedZeroDelay(t *testing.T) {
	delay := simulatedModelCacheStreamDelay(service.SimulatedModelCacheResponse{
		StreamDelaysMS:  []int64{0},
		DurationSeconds: 10,
	}, 0, 1)

	assert.Equal(t, time.Duration(0), delay)
}

func TestSimulatedModelCacheStreamDelayCountsReplayPreparation(t *testing.T) {
	delay := simulatedModelCacheStreamDelay(service.SimulatedModelCacheResponse{
		StreamDelaysMS:           []int64{20},
		ReplayPreparationSeconds: 0.011,
	}, 0, 1)

	assert.Equal(t, 9*time.Millisecond, delay)
}
