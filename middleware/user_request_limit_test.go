package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureUserRequestLimitTest(t *testing.T, concurrencyLimit, tokenLimit int) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousConcurrencyLimit := setting.GetUserConcurrentRequestLimit()
	previousTokenLimit := setting.GetUserTokensPerMinuteLimit()
	common.RedisEnabled = false
	setting.SetUserConcurrentRequestLimit(concurrencyLimit)
	setting.SetUserTokensPerMinuteLimit(tokenLimit)
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		setting.SetUserConcurrentRequestLimit(previousConcurrencyLimit)
		setting.SetUserTokensPerMinuteLimit(previousTokenLimit)
	})
}

func TestUserRequestRateLimitReturns429AndReleasesConcurrency(t *testing.T) {
	configureUserRequestLimitTest(t, 1, 0)
	gin.SetMode(gin.TestMode)
	started := make(chan struct{})
	finish := make(chan struct{})
	var handlerCalls atomic.Int64

	router := gin.New()
	router.GET(
		"/relay",
		func(c *gin.Context) { c.Set("id", 910001) },
		UserRequestRateLimit(),
		func(c *gin.Context) {
			if handlerCalls.Add(1) == 1 {
				close(started)
				<-finish
				c.Status(http.StatusInternalServerError)
				return
			}
			c.Status(http.StatusNoContent)
		},
	)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/relay", nil))
		firstDone <- response
	}()
	<-started

	limited := httptest.NewRecorder()
	router.ServeHTTP(limited, httptest.NewRequest(http.MethodGet, "/relay", nil))
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Equal(t, "user_concurrency_limit_exceeded", openAIErrorCode(t, limited.Body.Bytes()))
	assert.Equal(t, "1", limited.Header().Get("Retry-After"))
	assert.Equal(t, int64(1), handlerCalls.Load())

	close(finish)
	assert.Equal(t, http.StatusInternalServerError, (<-firstDone).Code)
	afterRelease := httptest.NewRecorder()
	router.ServeHTTP(afterRelease, httptest.NewRequest(http.MethodGet, "/relay", nil))
	assert.Equal(t, http.StatusNoContent, afterRelease.Code)
}

func TestUserRequestRateLimitReturns429ForTPM(t *testing.T) {
	configureUserRequestLimitTest(t, 0, 100)
	require.NoError(t, service.RecordUserRequestLimitTokens(context.Background(), 920001, 100))
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.GET(
		"/relay",
		func(c *gin.Context) { c.Set("id", 920001) },
		UserRequestRateLimit(),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/relay", nil))

	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.Equal(t, "user_tpm_limit_exceeded", openAIErrorCode(t, response.Body.Bytes()))
	assert.NotEmpty(t, response.Header().Get("Retry-After"))
}

func TestUserRequestConcurrencyLimitDoesNotApplyTPMToTaskRoutes(t *testing.T) {
	configureUserRequestLimitTest(t, 0, 100)
	require.NoError(t, service.RecordUserRequestLimitTokens(context.Background(), 930001, 100))
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.GET(
		"/task",
		func(c *gin.Context) { c.Set("id", 930001) },
		UserRequestConcurrencyLimit(),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/task", nil))

	assert.Equal(t, http.StatusNoContent, response.Code)
}

func openAIErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload map[string]any
	require.NoError(t, common.Unmarshal(body, &payload))
	value := payload["error"].(map[string]any)
	return value["code"].(string)
}
