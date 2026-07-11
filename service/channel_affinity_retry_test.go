package service

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAffinitySkipIsConfirmedOnlyForFinalAdmittedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	setChannelAffinityContext(ctx, channelAffinityMeta{SkipRetry: true})

	MarkChannelAffinityCandidate(ctx, 10)
	assert.False(t, ShouldSkipRetryAfterChannelAffinityFailure(ctx))

	ConfirmChannelAffinitySelection(ctx, "default", 10)
	assert.True(t, ShouldSkipRetryAfterChannelAffinityFailure(ctx))

	ClearRequestChannelAffinitySelection(ctx)
	ConfirmChannelAffinitySelection(ctx, "default", 11)
	assert.False(t, ShouldSkipRetryAfterChannelAffinityFailure(ctx))
	assert.True(t, ctx.GetBool(ginKeyChannelAffinityBypassed))
}

func TestBypassedAffinityDoesNotOverwritePersistentCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheKeySuffix := fmt.Sprintf("retry-bypass-%d", time.Now().UnixNano())
	cache := getChannelAffinityCache()
	require.NoError(t, cache.SetWithTTL(cacheKeySuffix, 10, time.Minute))
	t.Cleanup(func() { _, _ = cache.DeleteMany([]string{cacheKeySuffix}) })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	setChannelAffinityContext(ctx, channelAffinityMeta{
		CacheKey:   channelAffinityCacheNamespace + ":" + cacheKeySuffix,
		TTLSeconds: 60,
		SkipRetry:  true,
	})
	MarkChannelAffinityCandidate(ctx, 10)
	ClearRequestChannelAffinitySelection(ctx)
	RecordChannelAffinity(ctx, 11)

	channelID, found, err := cache.Get(cacheKeySuffix)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 10, channelID)
}
