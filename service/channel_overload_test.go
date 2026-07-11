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
	base := time.Now()
	channel := &model.Channel{Id: 10, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 1},
	}}

	lease, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)

	blocked, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)

	lease.Release(context.Background())
	blocked, scope, err = acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(time.Second))
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)

	lease, scope, err = acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(2*time.Second))
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

func TestLocalOverloadRecoveryClearsRollingRequestWindows(t *testing.T) {
	store := overloadLocalStore{scopes: make(map[string]*overloadLocalScope)}
	config := model.OverloadProtection{Enabled: true, RequestsPerSecond: 1, RequestsPerMinute: 1}
	base := time.Unix(100, 0)

	assert.Empty(t, store.reserve(12, 0, "a", base, config, model.OverloadProtection{}))
	store.commit(12, 0, base, config, model.OverloadProtection{})
	store.release(12, 0, "a")
	assert.Equal(t, OverloadScopeChannel, store.reserve(12, 0, "b", base.Add(999*time.Millisecond), config, model.OverloadProtection{}))
	assert.Equal(t, OverloadScopeChannel, store.reserve(12, 0, "c", base.Add(1999*time.Millisecond), config, model.OverloadProtection{}))
	assert.Empty(t, store.reserve(12, 0, "d", base.Add(2*time.Second), config, model.OverloadProtection{}))
}

func TestLocalDisabledThenReenabledProtectionDoesNotReuseCooldown(t *testing.T) {
	store := overloadLocalStore{scopes: make(map[string]*overloadLocalScope)}
	base := time.Unix(150, 0)
	enabled := model.OverloadProtection{Enabled: true, RequestsPerMinute: 1, RecoverySeconds: 60}
	disabled := model.OverloadProtection{RecoverySeconds: 60}

	assert.Empty(t, store.reserve(126, 0, "first", base, enabled, model.OverloadProtection{}))
	store.commit(126, 0, base, enabled, model.OverloadProtection{})
	store.release(126, 0, "first")
	store.resetStatistics(126, 1)
	assert.Empty(t, store.reserve(126, 0, "disabled", base.Add(time.Second), disabled, model.OverloadProtection{}))

	assert.Empty(t, store.reserve(126, 0, "reenabled", base.Add(2*time.Second), enabled, model.OverloadProtection{}))
}

func TestRedisDisabledThenReenabledProtectionDoesNotReuseCooldown(t *testing.T) {
	server := miniredis.RunT(t)
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

	channel := &model.Channel{Id: 127, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{
			Enabled: true, RequestsPerMinute: 1, RecoverySeconds: 60,
		},
	}}
	first, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Empty(t, scope)
	require.NoError(t, first.Commit(context.Background()))
	first.Release(context.Background())
	ResetChannelOverloadState(context.Background(), channel.Id, 1)

	disabledChannel := *channel
	disabledChannel.ChannelInfo.ChannelOverloadProtection.Enabled = false
	disabledLease, scope, err := AcquireChannelOverloadLease(context.Background(), &disabledChannel, 0)
	require.NoError(t, err)
	assert.Nil(t, disabledLease)
	assert.Empty(t, scope)

	reenabled, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, reenabled)
	assert.Empty(t, scope)
	reenabled.Release(context.Background())
}

func TestResetChannelOverloadStatePreservesActiveLocalLeases(t *testing.T) {
	store := overloadLocalStore{scopes: make(map[string]*overloadLocalScope)}
	channelScope := store.scope(localOverloadScopeKey(128, -1))
	keyScope := store.scope(localOverloadScopeKey(128, 0))
	channelScope.requests = []int64{1}
	channelScope.tokenEvents = []overloadLocalTokenEvent{{at: 1, tokens: 10}}
	channelScope.blockedUntil = 100
	channelScope.leases["active"] = 200
	keyScope.requests = []int64{1}
	keyScope.tokenEvents = []overloadLocalTokenEvent{{at: 1, tokens: 10}}
	keyScope.blockedUntil = 100
	keyScope.leases["active"] = 200

	store.resetStatistics(128, 1)

	assert.Empty(t, channelScope.requests)
	assert.Empty(t, channelScope.tokenEvents)
	assert.Zero(t, channelScope.blockedUntil)
	assert.Equal(t, int64(200), channelScope.leases["active"])
	assert.Empty(t, keyScope.requests)
	assert.Empty(t, keyScope.tokenEvents)
	assert.Zero(t, keyScope.blockedUntil)
	assert.Equal(t, int64(200), keyScope.leases["active"])
}

func TestResetChannelOverloadStateClearsRedisStatisticsAndPreservesLeases(t *testing.T) {
	server := miniredis.RunT(t)
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

	channel := &model.Channel{Id: 129, ChannelInfo: model.ChannelInfo{
		IsMultiKey: true,
		ChannelOverloadProtection: model.OverloadProtection{
			Enabled: true, RequestsPerMinute: 1, ConcurrentRequests: 2, RecoverySeconds: 60,
		},
		MultiKeyOverloadProtection: model.OverloadProtection{
			Enabled: true, RequestsPerMinute: 1, TokensPerMinute: 10, ConcurrentRequests: 2, RecoverySeconds: 60,
		},
	}}
	lease, scope, err := AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)
	require.NoError(t, lease.Commit(context.Background()))
	require.NoError(t, recordChannelOverloadTokensAt(context.Background(), channel, 0, 10, time.Now()))

	ResetChannelOverloadState(context.Background(), channel.Id, 1)

	for _, key := range []string{
		overloadRequestKey(channel.Id, "channel"),
		overloadBlockedKey(channel.Id, "channel"),
		overloadRequestKey(channel.Id, "key:0"),
		overloadBlockedKey(channel.Id, "key:0"),
		overloadTokenKey(channel.Id, 0),
	} {
		exists, existsErr := common.RDB.Exists(context.Background(), key).Result()
		require.NoError(t, existsErr)
		assert.Zero(t, exists, key)
	}
	channelLeaseCount, err := common.RDB.ZCard(context.Background(), overloadLeaseKey(channel.Id, "channel")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), channelLeaseCount)
	keyLeaseCount, err := common.RDB.ZCard(context.Background(), overloadLeaseKey(channel.Id, "key:0")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), keyLeaseCount)
	lease.Release(context.Background())
}

func TestLocalOverloadRecoveryPreservesActiveLeases(t *testing.T) {
	resetLocalOverloadTestStore(t)
	base := time.Now()
	channel := &model.Channel{Id: 120, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{
			Enabled: true, ConcurrentRequests: 1, RecoverySeconds: 2,
		},
	}}

	lease, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)

	blocked, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)

	blocked, scope, err = acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(2*time.Second))
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope, "the active lease must immediately trigger a new cooldown after recovery")

	lease.Release(context.Background())
	blocked, scope, err = acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(3*time.Second))
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeChannel, scope)

	next, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(4*time.Second))
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Empty(t, scope)
	next.Release(context.Background())
}

func TestChannelAndKeyCooldownsUseIndependentRecoveryDurations(t *testing.T) {
	store := overloadLocalStore{scopes: make(map[string]*overloadLocalScope)}
	base := time.Unix(200, 0)
	channelConfig := model.OverloadProtection{Enabled: true, RequestsPerMinute: 1, RecoverySeconds: 5}
	keyConfig := model.OverloadProtection{Enabled: true, RequestsPerMinute: 1, RecoverySeconds: 2}

	assert.Empty(t, store.reserve(121, 3, "lease", base, channelConfig, keyConfig))
	store.commit(121, 3, base, channelConfig, keyConfig)

	store.mu.Lock()
	channelBlockedUntil := store.scope(localOverloadScopeKey(121, -1)).blockedUntil
	keyBlockedUntil := store.scope(localOverloadScopeKey(121, 3)).blockedUntil
	store.mu.Unlock()
	assert.Equal(t, base.Add(5*time.Second).UnixMilli(), channelBlockedUntil)
	assert.Equal(t, base.Add(2*time.Second).UnixMilli(), keyBlockedUntil)
}

func TestLocalTPMBlocksOnlyTheMatchingKeyAndResetsAfterRecovery(t *testing.T) {
	resetLocalOverloadTestStore(t)
	base := time.Now()
	channel := &model.Channel{Id: 122, ChannelInfo: model.ChannelInfo{
		IsMultiKey: true,
		MultiKeyOverloadProtection: model.OverloadProtection{
			Enabled: true, TokensPerMinute: 100, RecoverySeconds: 2,
		},
	}}

	require.NoError(t, recordChannelOverloadTokensAt(context.Background(), channel, 0, 40, base))
	require.NoError(t, recordChannelOverloadTokensAt(context.Background(), channel, 0, 60, base.Add(100*time.Millisecond)))

	blocked, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(time.Second))
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeMultiKey, scope)

	otherKey, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 1, base.Add(time.Second))
	require.NoError(t, err)
	require.NotNil(t, otherKey)
	assert.Empty(t, scope)
	otherKey.Release(context.Background())

	blocked, scope, err = acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(2099*time.Millisecond))
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeMultiKey, scope)

	recovered, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(2100*time.Millisecond))
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.Empty(t, scope)
	recovered.Release(context.Background())

	localOverloadStore.mu.Lock()
	assert.Empty(t, localOverloadStore.scope(localOverloadScopeKey(channel.Id, 0)).tokenEvents)
	localOverloadStore.mu.Unlock()
}

func TestRedisTPMAccumulationIsAtomicAcrossConcurrentRecorders(t *testing.T) {
	server := miniredis.RunT(t)
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

	base := time.Now()
	channel := &model.Channel{Id: 123, ChannelInfo: model.ChannelInfo{
		IsMultiKey: true,
		MultiKeyOverloadProtection: model.OverloadProtection{
			Enabled: true, TokensPerMinute: 100, RecoverySeconds: 2,
		},
	}}

	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 10 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			require.NoError(t, recordChannelOverloadTokensAt(context.Background(), channel, 0, 10, base))
		}()
	}
	close(start)
	wait.Wait()

	tokenEventCount, err := common.RDB.ZCard(context.Background(), overloadTokenKey(channel.Id, 0)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(10), tokenEventCount)
	blocked, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeMultiKey, scope)

	otherKey, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 1, base)
	require.NoError(t, err)
	require.NotNil(t, otherKey)
	assert.Empty(t, scope)
	otherKey.Release(context.Background())
}

func TestRedisTPMFallsBackToMirroredLocalState(t *testing.T) {
	server := miniredis.RunT(t)
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

	base := time.Now()
	channel := &model.Channel{Id: 124, ChannelInfo: model.ChannelInfo{
		IsMultiKey: true,
		MultiKeyOverloadProtection: model.OverloadProtection{
			Enabled: true, TokensPerMinute: 100, RecoverySeconds: 2,
		},
	}}

	require.NoError(t, recordChannelOverloadTokensAt(context.Background(), channel, 0, 60, base))
	server.Close()
	require.NoError(t, recordChannelOverloadTokensAt(context.Background(), channel, 0, 40, base.Add(time.Millisecond)))

	blocked, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base.Add(time.Second))
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, OverloadScopeMultiKey, scope)
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

func TestRedisRecoveryClearsRequestsAndPreservesActiveLeases(t *testing.T) {
	server := miniredis.RunT(t)
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

	base := time.Now()
	channel := &model.Channel{Id: 125, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{
			Enabled: true, RequestsPerMinute: 1, ConcurrentRequests: 2, RecoverySeconds: 2,
		},
	}}

	first, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, base)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Empty(t, scope)
	require.NoError(t, first.Commit(context.Background()))

	recovered, scope, err := acquireChannelOverloadLeaseAt(context.Background(), channel, 0, time.Now().Add(3*time.Second))
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.Empty(t, scope)

	requestCount, err := common.RDB.ZCard(context.Background(), overloadRequestKey(channel.Id, "channel")).Result()
	require.NoError(t, err)
	assert.Zero(t, requestCount)
	leaseCount, err := common.RDB.ZCard(context.Background(), overloadLeaseKey(channel.Id, "channel")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(2), leaseCount)

	first.Release(context.Background())
	recovered.Release(context.Background())
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
