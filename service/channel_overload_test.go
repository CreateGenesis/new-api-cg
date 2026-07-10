package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetLocalOverloadTestStore(t *testing.T) {
	t.Helper()
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	localOverloadStore.mu.Lock()
	localOverloadStore.scopes = make(map[string]*overloadLocalScope)
	localOverloadStore.mu.Unlock()
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
	})
}

func TestChannelOverloadConcurrentLeaseReleases(t *testing.T) {
	resetLocalOverloadTestStore(t)
	channel := &model.Channel{Id: 10, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 1},
	}}

	lease, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)

	blocked, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)

	lease.Release(context.Background())
	lease, scope, err = AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)
	lease.Release(context.Background())
}

func TestMultiKeyOverloadDoesNotConsumeChannelQuota(t *testing.T) {
	resetLocalOverloadTestStore(t)
	channel := &model.Channel{Id: 11, ChannelInfo: model.ChannelInfo{
		IsMultiKey:                 true,
		ChannelOverloadProtection:  model.OverloadProtection{Enabled: true, RequestsPerMinute: 2},
		MultiKeyOverloadProtection: model.OverloadProtection{Enabled: true, RequestsPerMinute: 1},
	}}

	lease, _, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, lease.Commit(context.Background()))
	lease.Release(context.Background())

	blocked, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeMultiKey, scope)

	lease, scope, err = AcquireChannelOverloadLease(context.Background(), channel, 1)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)
	lease.Release(context.Background())
}

func TestLocalOverloadRollingWindowsExpire(t *testing.T) {
	store := overloadLocalStore{scopes: make(map[string]*overloadLocalScope)}
	config := model.OverloadProtection{Enabled: true, RequestsPerSecond: 1, RequestsPerMinute: 1}
	base := time.Unix(100, 0)

	assert.Empty(t, store.reserve(12, 0, "a", base, config, model.OverloadProtection{}))
	store.commit(12, 0, base, config, model.OverloadProtection{})
	store.release(12, 0, "a")
	assert.Equal(t, OverloadScopeChannel, store.reserve(12, 0, "b", base.Add(999*time.Millisecond), config, model.OverloadProtection{}))
	assert.Equal(t, OverloadScopeChannel, store.reserve(12, 0, "c", base.Add(59*time.Second), config, model.OverloadProtection{}))
	assert.Empty(t, store.reserve(12, 0, "d", base.Add(60*time.Second), config, model.OverloadProtection{}))
}

func TestReleasedUncommittedLeaseDoesNotConsumeRequestQuota(t *testing.T) {
	resetLocalOverloadTestStore(t)
	channel := &model.Channel{Id: 16, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, RequestsPerMinute: 1},
	}}

	lease, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)
	lease.Release(context.Background())

	next, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Empty(t, scope)
	next.Release(context.Background())
}

func TestCommittedLeaseConsumesRequestQuotaAfterRelease(t *testing.T) {
	resetLocalOverloadTestStore(t)
	channel := &model.Channel{Id: 17, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, RequestsPerMinute: 1},
	}}

	lease, _, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, lease.Commit(context.Background()))
	lease.Release(context.Background())

	blocked, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)
}

func TestRedisReleasedUncommittedLeaseDoesNotConsumeRequestQuota(t *testing.T) {
	server := miniredis.RunT(t)
	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
	})
	resetLocalOverloadTestStore(t)
	channel := &model.Channel{Id: 18, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, RequestsPerMinute: 1},
	}}

	lease, _, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	lease.Release(context.Background())

	next, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Empty(t, scope)
	next.Release(context.Background())
}

func TestRedisCommittedLeaseConsumesRequestQuotaAfterRelease(t *testing.T) {
	server := miniredis.RunT(t)
	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
	})
	resetLocalOverloadTestStore(t)
	channel := &model.Channel{Id: 19, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, RequestsPerMinute: 1},
	}}

	lease, _, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, lease.Commit(context.Background()))
	lease.Release(context.Background())

	blocked, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)
}

func TestRedisOverloadAdmissionChecksChannelAndKeyAtomically(t *testing.T) {
	server, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(server.Close)

	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	localOverloadStore.mu.Lock()
	localOverloadStore.scopes = make(map[string]*overloadLocalScope)
	localOverloadStore.mu.Unlock()
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
	})

	channel := &model.Channel{Id: 13, ChannelInfo: model.ChannelInfo{
		IsMultiKey:                 true,
		ChannelOverloadProtection:  model.OverloadProtection{Enabled: true, ConcurrentRequests: 2},
		MultiKeyOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 1},
	}}

	lease, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)

	blocked, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeMultiKey, scope)

	secondKeyLease, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 1)
	require.NoError(t, err)
	require.NotNil(t, secondKeyLease)
	assert.Empty(t, scope)

	blocked, scope, err = AcquireChannelOverloadLease(context.Background(), channel, 2)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)

	lease.Release(context.Background())
	secondKeyLease.Release(context.Background())
}

func TestOverloadRedisFailureUsesMirroredLocalState(t *testing.T) {
	server, err := miniredis.Run()
	require.NoError(t, err)

	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	localOverloadStore.mu.Lock()
	localOverloadStore.scopes = make(map[string]*overloadLocalScope)
	localOverloadStore.mu.Unlock()
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
	})

	channel := &model.Channel{Id: 14, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 1},
	}}
	lease, _, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)

	server.Close()
	blocked, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)
	lease.Release(context.Background())
}

func TestRedisConcurrentAdmissionsDoNotExceedLimit(t *testing.T) {
	server, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(server.Close)

	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	localOverloadStore.mu.Lock()
	localOverloadStore.scopes = make(map[string]*overloadLocalScope)
	localOverloadStore.mu.Unlock()
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
	})

	channel := &model.Channel{Id: 15, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 5},
	}}
	start := make(chan struct{})
	leases := make(chan *OverloadLease, 20)
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			lease, _, acquireErr := AcquireChannelOverloadLease(context.Background(), channel, 0)
			require.NoError(t, acquireErr)
			if lease != nil {
				leases <- lease
			}
		}()
	}
	close(start)
	wait.Wait()
	close(leases)

	assert.Len(t, leases, 5)
	for lease := range leases {
		lease.Release(context.Background())
	}
}
