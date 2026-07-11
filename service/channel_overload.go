package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	overloadLeaseTTL       = 30 * time.Second
	overloadRenewInterval  = 10 * time.Second
	overloadRedisNamespace = "new-api:channel_overload:v1"
)

type OverloadScope string

const (
	OverloadScopeChannel  OverloadScope = "channel"
	OverloadScopeMultiKey OverloadScope = "multi_key"
)

var ErrChannelOverloaded = errors.New("all available channels are overloaded")

type overloadLocalScope struct {
	requests []int64
	leases   map[string]int64
}

type overloadLocalStore struct {
	mu     sync.Mutex
	scopes map[string]*overloadLocalScope
}

var (
	localOverloadStore = overloadLocalStore{scopes: make(map[string]*overloadLocalScope)}
	overloadWarnAt     atomic.Int64
)

var overloadAdmissionScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local lease_id = ARGV[3]
local lease_expiry = tonumber(ARGV[4])
local channel_enabled = tonumber(ARGV[5])
local channel_second = tonumber(ARGV[6])
local channel_minute = tonumber(ARGV[7])
local channel_concurrent = tonumber(ARGV[8])
local key_enabled = tonumber(ARGV[9])
local key_second = tonumber(ARGV[10])
local key_minute = tonumber(ARGV[11])
local key_concurrent = tonumber(ARGV[12])

redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - 60000)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now - 60000)
redis.call('ZREMRANGEBYSCORE', KEYS[4], '-inf', now)

local function overloaded(requests_key, leases_key, enabled, second_limit, minute_limit, concurrent_limit)
  if enabled == 0 then return false end
  if second_limit > 0 and redis.call('ZCOUNT', requests_key, now - 999, '+inf') >= second_limit then return true end
  if minute_limit > 0 and redis.call('ZCARD', requests_key) >= minute_limit then return true end
  if concurrent_limit > 0 and redis.call('ZCARD', leases_key) >= concurrent_limit then return true end
  return false
end

if overloaded(KEYS[1], KEYS[2], channel_enabled, channel_second, channel_minute, channel_concurrent) then
  return 1
end
if overloaded(KEYS[3], KEYS[4], key_enabled, key_second, key_minute, key_concurrent) then
  return 2
end

if channel_enabled == 1 then
  redis.call('ZADD', KEYS[2], lease_expiry, lease_id)
  redis.call('PEXPIRE', KEYS[2], 31000)
end
if key_enabled == 1 then
  redis.call('ZADD', KEYS[4], lease_expiry, lease_id)
  redis.call('PEXPIRE', KEYS[4], 31000)
end
return 0
`)

var overloadReleaseScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return 1
`)

var overloadRenewScript = redis.NewScript(`
if redis.call('ZSCORE', KEYS[1], ARGV[1]) then redis.call('ZADD', KEYS[1], ARGV[2], ARGV[1]); redis.call('PEXPIRE', KEYS[1], 31000) end
if redis.call('ZSCORE', KEYS[2], ARGV[1]) then redis.call('ZADD', KEYS[2], ARGV[2], ARGV[1]); redis.call('PEXPIRE', KEYS[2], 31000) end
return 1
`)

var overloadCommitScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local event_id = ARGV[2]
local channel_enabled = tonumber(ARGV[3])
local key_enabled = tonumber(ARGV[4])

if channel_enabled == 1 and redis.call('ZSCORE', KEYS[2], ARGV[5]) then
  redis.call('ZADD', KEYS[1], now, event_id)
  redis.call('PEXPIRE', KEYS[1], 61000)
end
if key_enabled == 1 and redis.call('ZSCORE', KEYS[4], ARGV[5]) then
  redis.call('ZADD', KEYS[3], now, event_id)
  redis.call('PEXPIRE', KEYS[3], 61000)
end
return 1
`)

type OverloadLease struct {
	channelID     int
	keyIndex      int
	id            string
	eventID       string
	channelConfig model.OverloadProtection
	keyConfig     model.OverloadProtection
	redis         bool
	committed     atomic.Bool
	cancel        context.CancelFunc
	once          sync.Once
}

func SetChannelOverloadLease(c *gin.Context, lease *OverloadLease) {
	if c == nil {
		return
	}
	common.SetContextKey(c, constant.ContextKeyChannelOverloadLease, lease)
}

func SetChannelOverloadLeaseControllerOwned(c *gin.Context, owned bool) {
	if c == nil {
		return
	}
	common.SetContextKey(c, constant.ContextKeyChannelOverloadOwner, owned)
}

func IsChannelOverloadLeaseControllerOwned(c *gin.Context) bool {
	return common.GetContextKeyBool(c, constant.ContextKeyChannelOverloadOwner)
}

func GetChannelOverloadLease(c *gin.Context) *OverloadLease {
	if c == nil {
		return nil
	}
	value, exists := common.GetContextKey(c, constant.ContextKeyChannelOverloadLease)
	if !exists {
		return nil
	}
	lease, _ := value.(*OverloadLease)
	return lease
}

func ClearChannelOverloadLease(c *gin.Context) {
	if c == nil {
		return
	}
	common.SetContextKey(c, constant.ContextKeyChannelOverloadLease, (*OverloadLease)(nil))
}

func DetachChannelOverloadLease(c *gin.Context) *OverloadLease {
	lease := GetChannelOverloadLease(c)
	ClearChannelOverloadLease(c)
	return lease
}

func CommitChannelOverloadLease(c *gin.Context) error {
	lease := GetChannelOverloadLease(c)
	if lease == nil {
		return nil
	}
	if err := lease.Commit(c.Request.Context()); err != nil {
		ClearChannelOverloadLease(c)
		lease.Release(c.Request.Context())
		return err
	}
	return nil
}

func ReleaseChannelOverloadLease(c *gin.Context) {
	lease := GetChannelOverloadLease(c)
	if lease == nil {
		return
	}
	ClearChannelOverloadLease(c)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease.Release(ctx)
}

func AcquireChannelOverloadLease(ctx context.Context, channel *model.Channel, keyIndex int) (*OverloadLease, OverloadScope, error) {
	if channel == nil {
		return nil, OverloadScopeChannel, errors.New("channel is nil")
	}
	channelConfig := channel.ChannelInfo.ChannelOverloadProtection
	keyConfig := channel.ChannelInfo.MultiKeyOverloadProtection
	keyConfig.Enabled = keyConfig.Enabled && channel.ChannelInfo.IsMultiKey
	if !channelConfig.Enabled && !keyConfig.Enabled {
		return nil, "", nil
	}

	now := time.Now()
	lease := &OverloadLease{
		channelID:     channel.Id,
		keyIndex:      keyIndex,
		id:            common.GetUUID(),
		channelConfig: channelConfig,
		keyConfig:     keyConfig,
	}
	eventID := lease.id + ":" + strconv.FormatInt(now.UnixNano(), 10)
	lease.eventID = eventID

	if common.RedisEnabled && common.RDB != nil {
		result, err := overloadAdmissionScript.Run(ctx, common.RDB, overloadRedisKeys(channel.Id, keyIndex),
			now.UnixMilli(), eventID, lease.id, now.Add(overloadLeaseTTL).UnixMilli(),
			boolInt(channelConfig.Enabled), channelConfig.RequestsPerSecond, channelConfig.RequestsPerMinute, channelConfig.ConcurrentRequests,
			boolInt(keyConfig.Enabled), keyConfig.RequestsPerSecond, keyConfig.RequestsPerMinute, keyConfig.ConcurrentRequests,
		).Int()
		if err == nil {
			if result == 1 {
				return nil, OverloadScopeChannel, nil
			}
			if result == 2 {
				return nil, OverloadScopeMultiKey, nil
			}
			lease.redis = true
			localOverloadStore.reserve(channel.Id, keyIndex, lease.id, now, channelConfig, keyConfig)
			lease.startRenewal(ctx)
			return lease, "", nil
		}
		logOverloadRedisFallback(err)
	}

	scope := localOverloadStore.reserve(channel.Id, keyIndex, lease.id, now, channelConfig, keyConfig)
	if scope != "" {
		return nil, scope, nil
	}
	lease.startRenewal(ctx)
	return lease, "", nil
}

func (lease *OverloadLease) Commit(ctx context.Context) error {
	if lease == nil || !lease.committed.CompareAndSwap(false, true) {
		return nil
	}
	now := time.Now()
	if lease.redis && common.RedisEnabled && common.RDB != nil {
		if err := overloadCommitScript.Run(ctx, common.RDB, overloadRedisKeys(lease.channelID, lease.keyIndex),
			now.UnixMilli(), lease.eventID, boolInt(lease.channelConfig.Enabled), boolInt(lease.keyConfig.Enabled), lease.id,
		).Err(); err != nil {
			logOverloadRedisFallback(err)
		} else {
			localOverloadStore.commit(lease.channelID, lease.keyIndex, now, lease.channelConfig, lease.keyConfig)
			return nil
		}
	}
	localOverloadStore.commit(lease.channelID, lease.keyIndex, now, lease.channelConfig, lease.keyConfig)
	return nil
}

func (lease *OverloadLease) Release(ctx context.Context) {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.cancel != nil {
			lease.cancel()
		}
		if lease.redis && common.RedisEnabled && common.RDB != nil {
			if err := overloadReleaseScript.Run(ctx, common.RDB, overloadLeaseKeys(lease.channelID, lease.keyIndex), lease.id).Err(); err == nil {
			} else {
				logOverloadRedisFallback(err)
			}
		}
		localOverloadStore.release(lease.channelID, lease.keyIndex, lease.id)
	})
}

func (lease *OverloadLease) startRenewal(ctx context.Context) {
	renewCtx, cancel := context.WithCancel(ctx)
	lease.cancel = cancel
	go func() {
		ticker := time.NewTicker(overloadRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case now := <-ticker.C:
				if lease.redis && common.RedisEnabled && common.RDB != nil {
					err := overloadRenewScript.Run(renewCtx, common.RDB, overloadLeaseKeys(lease.channelID, lease.keyIndex), lease.id, now.Add(overloadLeaseTTL).UnixMilli()).Err()
					if err == nil {
						localOverloadStore.renew(lease.channelID, lease.keyIndex, lease.id, now.Add(overloadLeaseTTL).UnixMilli())
						continue
					}
					logOverloadRedisFallback(err)
				}
				localOverloadStore.renew(lease.channelID, lease.keyIndex, lease.id, now.Add(overloadLeaseTTL).UnixMilli())
			}
		}
	}()
}

func (store *overloadLocalStore) reserve(channelID, keyIndex int, leaseID string, now time.Time, channelConfig, keyConfig model.OverloadProtection) OverloadScope {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	channelScope := store.scope(localOverloadScopeKey(channelID, -1))
	keyScope := store.scope(localOverloadScopeKey(channelID, keyIndex))
	store.cleanup(channelScope, nowMS)
	store.cleanup(keyScope, nowMS)
	if locallyOverloaded(channelScope, nowMS, channelConfig) {
		return OverloadScopeChannel
	}
	if locallyOverloaded(keyScope, nowMS, keyConfig) {
		return OverloadScopeMultiKey
	}
	store.reserveLocked(channelScope, keyScope, leaseID, nowMS, channelConfig, keyConfig)
	return ""
}

func (store *overloadLocalStore) commit(channelID, keyIndex int, now time.Time, channelConfig, keyConfig model.OverloadProtection) {
	store.mu.Lock()
	defer store.mu.Unlock()
	channelScope := store.scope(localOverloadScopeKey(channelID, -1))
	keyScope := store.scope(localOverloadScopeKey(channelID, keyIndex))
	store.cleanup(channelScope, now.UnixMilli())
	store.cleanup(keyScope, now.UnixMilli())
	if channelConfig.Enabled {
		channelScope.requests = append(channelScope.requests, now.UnixMilli())
	}
	if keyConfig.Enabled {
		keyScope.requests = append(keyScope.requests, now.UnixMilli())
	}
}

func (store *overloadLocalStore) reserveLocked(channelScope, keyScope *overloadLocalScope, leaseID string, nowMS int64, channelConfig, keyConfig model.OverloadProtection) {
	expires := nowMS + overloadLeaseTTL.Milliseconds()
	if channelConfig.Enabled {
		channelScope.leases[leaseID] = expires
	}
	if keyConfig.Enabled {
		keyScope.leases[leaseID] = expires
	}
}

func locallyOverloaded(scope *overloadLocalScope, nowMS int64, config model.OverloadProtection) bool {
	if !config.Enabled {
		return false
	}
	if config.RequestsPerMinute > 0 && len(scope.requests) >= config.RequestsPerMinute {
		return true
	}
	if config.RequestsPerSecond > 0 {
		secondCount := 0
		for i := len(scope.requests) - 1; i >= 0 && scope.requests[i] >= nowMS-999; i-- {
			secondCount++
		}
		if secondCount >= config.RequestsPerSecond {
			return true
		}
	}
	return config.ConcurrentRequests > 0 && len(scope.leases) >= config.ConcurrentRequests
}

func (store *overloadLocalStore) scope(key string) *overloadLocalScope {
	scope := store.scopes[key]
	if scope == nil {
		scope = &overloadLocalScope{leases: make(map[string]int64)}
		store.scopes[key] = scope
	}
	return scope
}

func (store *overloadLocalStore) cleanup(scope *overloadLocalScope, nowMS int64) {
	first := 0
	for first < len(scope.requests) && scope.requests[first] <= nowMS-60000 {
		first++
	}
	scope.requests = scope.requests[first:]
	for id, expiry := range scope.leases {
		if expiry <= nowMS {
			delete(scope.leases, id)
		}
	}
}

func (store *overloadLocalStore) renew(channelID, keyIndex int, leaseID string, expiry int64) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, index := range []int{-1, keyIndex} {
		scope := store.scope(localOverloadScopeKey(channelID, index))
		if _, exists := scope.leases[leaseID]; exists {
			scope.leases[leaseID] = expiry
		}
	}
}

func (store *overloadLocalStore) release(channelID, keyIndex int, leaseID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.scope(localOverloadScopeKey(channelID, -1)).leases, leaseID)
	delete(store.scope(localOverloadScopeKey(channelID, keyIndex)).leases, leaseID)
}

func overloadRedisKeys(channelID, keyIndex int) []string {
	return []string{
		overloadRequestKey(channelID, "channel"),
		overloadLeaseKey(channelID, "channel"),
		overloadRequestKey(channelID, fmt.Sprintf("key:%d", keyIndex)),
		overloadLeaseKey(channelID, fmt.Sprintf("key:%d", keyIndex)),
	}
}

func overloadLeaseKeys(channelID, keyIndex int) []string {
	keys := overloadRedisKeys(channelID, keyIndex)
	return []string{keys[1], keys[3]}
}

func overloadRequestKey(channelID int, scope string) string {
	return fmt.Sprintf("%s:{%d}:%s:requests", overloadRedisNamespace, channelID, scope)
}

func overloadLeaseKey(channelID int, scope string) string {
	return fmt.Sprintf("%s:{%d}:%s:leases", overloadRedisNamespace, channelID, scope)
}

func localOverloadScopeKey(channelID, keyIndex int) string {
	return fmt.Sprintf("%d:%d", channelID, keyIndex)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func logOverloadRedisFallback(err error) {
	now := time.Now().Unix()
	previous := overloadWarnAt.Load()
	if now-previous < 60 || !overloadWarnAt.CompareAndSwap(previous, now) {
		return
	}
	common.SysError(fmt.Sprintf("channel overload Redis operation failed, using local fallback: %v", err))
}
