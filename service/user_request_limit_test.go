package service

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetUserRequestLimitTestStore(t *testing.T) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRedisClient := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	localUserRequestLimitStore.mu.Lock()
	localUserRequestLimitStore.scopes = make(map[int]*userRequestLimitScope)
	localUserRequestLimitStore.nextCleanup = 0
	localUserRequestLimitStore.mu.Unlock()
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedisClient
	})
}

func useUserRequestLimitRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	resetUserRequestLimitTestStore(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	require.NoError(t, client.Ping(context.Background()).Err())
	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return server
}

func TestUserRequestLimitDisabledDoesNotCreateLease(t *testing.T) {
	resetUserRequestLimitTestStore(t)
	lease, kind, retryAfter, err := AcquireUserRequestLimitLease(context.Background(), 100, 0, 0)
	require.NoError(t, err)
	assert.Nil(t, lease)
	assert.Empty(t, kind)
	assert.Zero(t, retryAfter)
}

func TestUserRequestLimitConcurrencyIsPerUserAndReleased(t *testing.T) {
	resetUserRequestLimitTestStore(t)
	base := time.Unix(100, 0)

	lease, kind, _, err := acquireUserRequestLimitLeaseAt(context.Background(), 101, 1, 0, base)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, kind)

	blocked, kind, retryAfter, err := acquireUserRequestLimitLeaseAt(context.Background(), 101, 1, 0, base)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, UserRequestLimitConcurrency, kind)
	assert.Positive(t, retryAfter)

	otherUser, kind, _, err := acquireUserRequestLimitLeaseAt(context.Background(), 102, 1, 0, base)
	require.NoError(t, err)
	require.NotNil(t, otherUser)
	assert.Empty(t, kind)

	lease.Release()
	released, kind, _, err := acquireUserRequestLimitLeaseAt(context.Background(), 101, 1, 0, base.Add(time.Second))
	require.NoError(t, err)
	require.NotNil(t, released)
	assert.Empty(t, kind)

	otherUser.Release()
	released.Release()
}

func TestUserRequestLimitTPMUsesRollingWindowAndIsPerUser(t *testing.T) {
	resetUserRequestLimitTestStore(t)
	base := time.Unix(200, 0)
	require.NoError(t, recordUserRequestLimitTokensAt(context.Background(), 201, 40, base))
	require.NoError(t, recordUserRequestLimitTokensAt(context.Background(), 201, 60, base.Add(100*time.Millisecond)))

	lease, kind, retryAfter, err := acquireUserRequestLimitLeaseAt(context.Background(), 201, 0, 100, base.Add(time.Second))
	require.NoError(t, err)
	assert.Nil(t, lease)
	assert.Equal(t, UserRequestLimitTPM, kind)
	assert.Equal(t, 59*time.Second, retryAfter)

	otherUser, kind, _, err := acquireUserRequestLimitLeaseAt(context.Background(), 202, 0, 100, base.Add(time.Second))
	require.NoError(t, err)
	assert.Nil(t, otherUser)
	assert.Empty(t, kind)

	recovered, kind, _, err := acquireUserRequestLimitLeaseAt(context.Background(), 201, 0, 100, base.Add(60*time.Second+101*time.Millisecond))
	require.NoError(t, err)
	assert.Nil(t, recovered)
	assert.Empty(t, kind)
}

func TestRedisUserRequestLimitConcurrencyAdmissionIsAtomic(t *testing.T) {
	useUserRequestLimitRedis(t)
	const requestCount = 12

	leases := make(chan *UserRequestLimitLease, requestCount)
	errorsFound := make(chan error, requestCount)
	var waitGroup sync.WaitGroup
	waitGroup.Add(requestCount)
	for range requestCount {
		go func() {
			defer waitGroup.Done()
			lease, _, _, err := AcquireUserRequestLimitLease(context.Background(), 301, 3, 0)
			if err != nil {
				errorsFound <- err
				return
			}
			if lease != nil {
				leases <- lease
			}
		}()
	}
	waitGroup.Wait()
	close(errorsFound)
	close(leases)
	for err := range errorsFound {
		require.NoError(t, err)
	}

	acquired := make([]*UserRequestLimitLease, 0, requestCount)
	for lease := range leases {
		acquired = append(acquired, lease)
	}
	assert.Len(t, acquired, 3)
	redisLeaseCount, err := common.RDB.ZCard(context.Background(), userRequestLimitLeaseKey(301)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(3), redisLeaseCount)
	for _, lease := range acquired {
		lease.Release()
	}
}

func TestRedisUserRequestLimitTPMRecoversAfterRollingWindow(t *testing.T) {
	useUserRequestLimitRedis(t)
	base := time.Unix(300, 0)
	require.NoError(t, recordUserRequestLimitTokensAt(context.Background(), 401, 100, base))

	lease, kind, _, err := acquireUserRequestLimitLeaseAt(context.Background(), 401, 0, 100, base.Add(time.Second))
	require.NoError(t, err)
	assert.Nil(t, lease)
	assert.Equal(t, UserRequestLimitTPM, kind)

	lease, kind, _, err = acquireUserRequestLimitLeaseAt(context.Background(), 401, 0, 100, base.Add(61*time.Second))
	require.NoError(t, err)
	assert.Nil(t, lease)
	assert.Empty(t, kind)
}

func TestUserRequestLimitUsageCountsNormalizedCacheTokensOnce(t *testing.T) {
	resetUserRequestLimitTestStore(t)
	previousTPMLimit := setting.GetUserTokensPerMinuteLimit()
	setting.SetUserTokensPerMinuteLimit(1000)
	t.Cleanup(func() { setting.SetUserTokensPerMinuteLimit(previousTPMLimit) })

	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = request
	relayInfo := &relaycommon.RelayInfo{UserId: 501}
	usage := &dto.Usage{
		PromptTokens:     60,
		CompletionTokens: 10,
		UsageSemantic:    UsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 40,
		},
	}

	recordUserRequestLimitUsage(ctx, relayInfo, usage)
	recordUserRequestLimitUsage(ctx, relayInfo, usage)

	localUserRequestLimitStore.mu.Lock()
	scope := localUserRequestLimitStore.scopes[501]
	localUserRequestLimitStore.mu.Unlock()
	require.NotNil(t, scope)
	assert.Equal(t, int64(110), userTokenTotal(scope))
	assert.Len(t, scope.tokenEvents, 1)
}
