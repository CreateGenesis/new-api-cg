package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
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
