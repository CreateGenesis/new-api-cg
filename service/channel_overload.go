package service

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	overloadWindow         = 60 * time.Second
	overloadRedisNamespace = "new-api:channel_overload:v2"
)

type OverloadScope string

const (
	OverloadScopeChannel  OverloadScope = "channel"
	OverloadScopeMultiKey OverloadScope = "multi_key"
)

var ErrChannelOverloaded = errors.New("all available channels are overloaded")

type overloadLocalTokenEvent struct {
	at     int64
	tokens int64
}

type overloadLocalScope struct {
	requests     []int64
	tokenEvents  []overloadLocalTokenEvent
	leases       map[string]int64
	blockedUntil int64
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
local lease_id = ARGV[2]
local lease_expiry = tonumber(ARGV[3])
local channel_enabled = tonumber(ARGV[4])
local channel_second = tonumber(ARGV[5])
local channel_minute = tonumber(ARGV[6])
local channel_concurrent = tonumber(ARGV[7])
local channel_recovery = tonumber(ARGV[8])
local key_enabled = tonumber(ARGV[9])
local key_second = tonumber(ARGV[10])
local key_minute = tonumber(ARGV[11])
local key_concurrent = tonumber(ARGV[12])
local key_recovery = tonumber(ARGV[13])

local function set_blocked(blocked_key, recovery)
  local candidate = now + recovery
  local current = tonumber(redis.call('GET', blocked_key) or '0')
  if current >= candidate then return current end
  redis.call('SET', blocked_key, candidate)
  redis.call('PEXPIRE', blocked_key, candidate - now + 61000)
  return candidate
end

local function recover_or_blocked(blocked_key, requests_key, tokens_key)
  local blocked_until = tonumber(redis.call('GET', blocked_key) or '0')
  if blocked_until > now then return blocked_until end
  if blocked_until > 0 then
    redis.call('DEL', blocked_key)
    redis.call('DEL', requests_key)
    if tokens_key ~= '' then redis.call('DEL', tokens_key) end
  end
  return 0
end

local function overloaded(requests_key, leases_key, enabled, second_limit, minute_limit, concurrent_limit)
  if enabled == 0 then return false end
  if second_limit > 0 and redis.call('ZCOUNT', requests_key, now - 999, '+inf') >= second_limit then return true end
  if minute_limit > 0 and redis.call('ZCARD', requests_key) >= minute_limit then return true end
  if concurrent_limit > 0 and redis.call('ZCARD', leases_key) >= concurrent_limit then return true end
  return false
end

redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - 60000)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[4], '-inf', now - 60000)
redis.call('ZREMRANGEBYSCORE', KEYS[5], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[7], '-inf', now - 60000)

if channel_enabled == 1 then
  local channel_blocked_until = recover_or_blocked(KEYS[3], KEYS[1], '')
  if channel_blocked_until > 0 then return {1, channel_blocked_until} end
else
  redis.call('DEL', KEYS[1], KEYS[3])
end
if key_enabled == 1 then
  local key_blocked_until = recover_or_blocked(KEYS[6], KEYS[4], KEYS[7])
  if key_blocked_until > 0 then return {2, key_blocked_until} end
else
  redis.call('DEL', KEYS[4], KEYS[6], KEYS[7])
end

if overloaded(KEYS[1], KEYS[2], channel_enabled, channel_second, channel_minute, channel_concurrent) then
  return {1, set_blocked(KEYS[3], channel_recovery)}
end
if overloaded(KEYS[4], KEYS[5], key_enabled, key_second, key_minute, key_concurrent) then
  return {2, set_blocked(KEYS[6], key_recovery)}
end

if channel_enabled == 1 then
  redis.call('ZADD', KEYS[2], lease_expiry, lease_id)
  redis.call('PEXPIRE', KEYS[2], 31000)
end
if key_enabled == 1 then
  redis.call('ZADD', KEYS[5], lease_expiry, lease_id)
  redis.call('PEXPIRE', KEYS[5], 31000)
end
return {0, 0}
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
local channel_second = tonumber(ARGV[4])
local channel_minute = tonumber(ARGV[5])
local channel_recovery = tonumber(ARGV[6])
local key_enabled = tonumber(ARGV[7])
local key_second = tonumber(ARGV[8])
local key_minute = tonumber(ARGV[9])
local key_recovery = tonumber(ARGV[10])
local lease_id = ARGV[11]

local function set_blocked(blocked_key, recovery)
  local candidate = now + recovery
  local current = tonumber(redis.call('GET', blocked_key) or '0')
  if current >= candidate then return current end
  redis.call('SET', blocked_key, candidate)
  redis.call('PEXPIRE', blocked_key, candidate - now + 61000)
  return candidate
end

local function threshold_reached(requests_key, second_limit, minute_limit)
  if second_limit > 0 and redis.call('ZCOUNT', requests_key, now - 999, '+inf') >= second_limit then return true end
  if minute_limit > 0 and redis.call('ZCARD', requests_key) >= minute_limit then return true end
  return false
end

redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - 60000)
redis.call('ZREMRANGEBYSCORE', KEYS[4], '-inf', now - 60000)

if channel_enabled == 1 and redis.call('ZSCORE', KEYS[2], lease_id) then
  redis.call('ZADD', KEYS[1], now, event_id)
  redis.call('PEXPIRE', KEYS[1], 61000)
  if threshold_reached(KEYS[1], channel_second, channel_minute) then set_blocked(KEYS[3], channel_recovery) end
end
if key_enabled == 1 and redis.call('ZSCORE', KEYS[5], lease_id) then
  redis.call('ZADD', KEYS[4], now, event_id)
  redis.call('PEXPIRE', KEYS[4], 61000)
  if threshold_reached(KEYS[4], key_second, key_minute) then set_blocked(KEYS[6], key_recovery) end
end
return 1
`)

var overloadTokenScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local event_id = ARGV[2]
local event_tokens = ARGV[3]
local token_limit = ARGV[4]
local recovery = tonumber(ARGV[5])
local max_int64 = '9223372036854775807'

local function normalize_decimal(value)
  value = tostring(value or '0')
  if not string.match(value, '^%d+$') then return '0' end
  value = string.gsub(value, '^0+', '')
  if value == '' then return '0' end
  return value
end

local function compare_decimal(a, b)
  a = normalize_decimal(a)
  b = normalize_decimal(b)
  if string.len(a) < string.len(b) then return -1 end
  if string.len(a) > string.len(b) then return 1 end
  if a < b then return -1 end
  if a > b then return 1 end
  return 0
end

local function add_decimal(a, b)
  a = normalize_decimal(a)
  b = normalize_decimal(b)
  local i = string.len(a)
  local j = string.len(b)
  local carry = 0
  local out = {}
  while i > 0 or j > 0 or carry > 0 do
    local da = 0
    local db = 0
    if i > 0 then da = tonumber(string.sub(a, i, i)); i = i - 1 end
    if j > 0 then db = tonumber(string.sub(b, j, j)); j = j - 1 end
    local sum = da + db + carry
    table.insert(out, 1, tostring(sum % 10))
    carry = math.floor(sum / 10)
  end
  local result = table.concat(out)
  if compare_decimal(result, max_int64) > 0 then return max_int64 end
  return result
end

local function set_blocked()
  local candidate = now + recovery
  local current = tonumber(redis.call('GET', KEYS[2]) or '0')
  if current >= candidate then return current end
  redis.call('SET', KEYS[2], candidate)
  redis.call('PEXPIRE', KEYS[2], candidate - now + 61000)
  return candidate
end

local blocked_until = tonumber(redis.call('GET', KEYS[2]) or '0')
if blocked_until > 0 and blocked_until <= now then
  redis.call('DEL', KEYS[2])
  redis.call('DEL', KEYS[1])
  blocked_until = 0
end

redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - 60000)
redis.call('ZADD', KEYS[1], now, event_id .. '|' .. normalize_decimal(event_tokens))
redis.call('PEXPIRE', KEYS[1], 61000)

local total = '0'
local events = redis.call('ZRANGE', KEYS[1], 0, -1)
for _, event in ipairs(events) do
  local tokens = string.match(event, '|(%d+)$') or '0'
  total = add_decimal(total, tokens)
end

if compare_decimal(total, token_limit) >= 0 then
  return {1, set_blocked()}
end
return {0, blocked_until}
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
	return acquireChannelOverloadLeaseAt(ctx, channel, keyIndex, time.Now())
}

func acquireChannelOverloadLeaseAt(ctx context.Context, channel *model.Channel, keyIndex int, now time.Time) (*OverloadLease, OverloadScope, error) {
	if channel == nil {
		return nil, OverloadScopeChannel, errors.New("channel is nil")
	}
	channelConfig := channel.ChannelInfo.ChannelOverloadProtection
	keyConfig := channel.ChannelInfo.MultiKeyOverloadProtection
	keyConfig.Enabled = keyConfig.Enabled && channel.ChannelInfo.IsMultiKey
	if !channelConfig.Enabled && !keyConfig.Enabled {
		return nil, "", nil
	}

	lease := &OverloadLease{
		channelID:     channel.Id,
		keyIndex:      keyIndex,
		id:            common.GetUUID(),
		channelConfig: channelConfig,
		keyConfig:     keyConfig,
	}
	lease.eventID = lease.id + ":" + strconv.FormatInt(now.UnixNano(), 10)

	if common.RedisEnabled && common.RDB != nil {
		result, err := overloadAdmissionScript.Run(ctx, common.RDB, overloadRedisKeys(channel.Id, keyIndex),
			now.UnixMilli(), lease.id, now.Add(overloadLeaseTTL).UnixMilli(),
			boolInt(channelConfig.Enabled), channelConfig.RequestsPerSecond, channelConfig.RequestsPerMinute, channelConfig.ConcurrentRequests, overloadRecoveryMilliseconds(channelConfig),
			boolInt(keyConfig.Enabled), keyConfig.RequestsPerSecond, keyConfig.RequestsPerMinute, keyConfig.ConcurrentRequests, overloadRecoveryMilliseconds(keyConfig),
		).Result()
		if err == nil {
			scope, blockedUntil, parseErr := parseOverloadScriptResult(result)
			if parseErr == nil {
				if scope != "" {
					localOverloadStore.block(channel.Id, keyIndex, scope, blockedUntil)
					return nil, scope, nil
				}
				lease.redis = true
				localOverloadStore.mirrorReserve(channel.Id, keyIndex, lease.id, now, channelConfig, keyConfig)
				lease.startRenewal(ctx)
				return lease, "", nil
			}
			err = parseErr
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
			now.UnixMilli(), lease.eventID,
			boolInt(lease.channelConfig.Enabled), lease.channelConfig.RequestsPerSecond, lease.channelConfig.RequestsPerMinute, overloadRecoveryMilliseconds(lease.channelConfig),
			boolInt(lease.keyConfig.Enabled), lease.keyConfig.RequestsPerSecond, lease.keyConfig.RequestsPerMinute, overloadRecoveryMilliseconds(lease.keyConfig),
			lease.id,
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
			if err := overloadReleaseScript.Run(ctx, common.RDB, overloadLeaseKeys(lease.channelID, lease.keyIndex), lease.id).Err(); err != nil {
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

func RecordChannelOverloadTokens(ctx context.Context, channelID, keyIndex int, tokens int64) error {
	if tokens <= 0 || channelID <= 0 {
		return nil
	}
	channel, err := model.CacheGetChannel(channelID)
	if err != nil {
		return err
	}
	return recordChannelOverloadTokensAt(ctx, channel, keyIndex, tokens, time.Now())
}

func ResetChannelOverloadState(ctx context.Context, channelID, keyCount int) {
	if channelID <= 0 {
		return
	}
	if keyCount < 0 {
		keyCount = 0
	}
	localOverloadStore.resetStatistics(channelID, keyCount)
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	keys := []string{
		overloadRequestKey(channelID, "channel"),
		overloadBlockedKey(channelID, "channel"),
	}
	for keyIndex := 0; keyIndex < keyCount; keyIndex++ {
		scope := fmt.Sprintf("key:%d", keyIndex)
		keys = append(keys,
			overloadRequestKey(channelID, scope),
			overloadBlockedKey(channelID, scope),
			overloadTokenKey(channelID, keyIndex),
		)
	}
	if err := common.RDB.Del(ctx, keys...).Err(); err != nil {
		logOverloadRedisFallback(err)
	}
}

func recordChannelOverloadTokensAt(ctx context.Context, channel *model.Channel, keyIndex int, tokens int64, now time.Time) error {
	if channel == nil {
		return errors.New("channel is nil")
	}
	config := channel.ChannelInfo.MultiKeyOverloadProtection
	if !channel.ChannelInfo.IsMultiKey || !config.Enabled || config.TokensPerMinute <= 0 || tokens <= 0 {
		return nil
	}

	eventID := common.GetUUID() + ":" + strconv.FormatInt(now.UnixNano(), 10)
	if common.RedisEnabled && common.RDB != nil {
		result, err := overloadTokenScript.Run(ctx, common.RDB, []string{
			overloadTokenKey(channel.Id, keyIndex),
			overloadBlockedKey(channel.Id, fmt.Sprintf("key:%d", keyIndex)),
		}, now.UnixMilli(), eventID, strconv.FormatInt(tokens, 10), strconv.FormatInt(config.TokensPerMinute, 10), overloadRecoveryMilliseconds(config)).Result()
		if err == nil {
			_, blockedUntil, parseErr := parseOverloadScriptResult(result)
			if parseErr == nil {
				localOverloadStore.recordTokens(channel.Id, keyIndex, tokens, now, config)
				if blockedUntil > now.UnixMilli() {
					localOverloadStore.block(channel.Id, keyIndex, OverloadScopeMultiKey, blockedUntil)
				}
				return nil
			}
			err = parseErr
		}
		logOverloadRedisFallback(err)
	}

	localOverloadStore.recordTokens(channel.Id, keyIndex, tokens, now, config)
	return nil
}

func (store *overloadLocalStore) reserve(channelID, keyIndex int, leaseID string, now time.Time, channelConfig, keyConfig model.OverloadProtection) OverloadScope {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	channelScope := store.scope(localOverloadScopeKey(channelID, -1))
	keyScope := store.scope(localOverloadScopeKey(channelID, keyIndex))
	store.prepare(channelScope, nowMS)
	store.prepare(keyScope, nowMS)
	if channelConfig.Enabled && channelScope.blockedUntil > nowMS {
		return OverloadScopeChannel
	}
	if keyConfig.Enabled && keyScope.blockedUntil > nowMS {
		return OverloadScopeMultiKey
	}
	if localScopeThresholdReached(channelScope, nowMS, channelConfig) {
		blockLocalScope(channelScope, nowMS, channelConfig)
		return OverloadScopeChannel
	}
	if localScopeThresholdReached(keyScope, nowMS, keyConfig) {
		blockLocalScope(keyScope, nowMS, keyConfig)
		return OverloadScopeMultiKey
	}
	store.reserveLocked(channelScope, keyScope, leaseID, nowMS, channelConfig, keyConfig)
	return ""
}

func (store *overloadLocalStore) mirrorReserve(channelID, keyIndex int, leaseID string, now time.Time, channelConfig, keyConfig model.OverloadProtection) {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	channelScope := store.scope(localOverloadScopeKey(channelID, -1))
	keyScope := store.scope(localOverloadScopeKey(channelID, keyIndex))
	store.prepare(channelScope, nowMS)
	store.prepare(keyScope, nowMS)
	if channelScope.blockedUntil > nowMS {
		channelScope.requests = nil
		channelScope.tokenEvents = nil
	}
	if keyScope.blockedUntil > nowMS {
		keyScope.requests = nil
		keyScope.tokenEvents = nil
	}
	channelScope.blockedUntil = 0
	keyScope.blockedUntil = 0
	store.reserveLocked(channelScope, keyScope, leaseID, nowMS, channelConfig, keyConfig)
}

func (store *overloadLocalStore) resetStatistics(channelID, keyCount int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	indexes := make([]int, 0, keyCount+1)
	indexes = append(indexes, -1)
	for keyIndex := 0; keyIndex < keyCount; keyIndex++ {
		indexes = append(indexes, keyIndex)
	}
	for _, keyIndex := range indexes {
		scope := store.scope(localOverloadScopeKey(channelID, keyIndex))
		scope.requests = nil
		scope.tokenEvents = nil
		scope.blockedUntil = 0
	}
}

func (store *overloadLocalStore) commit(channelID, keyIndex int, now time.Time, channelConfig, keyConfig model.OverloadProtection) {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	channelScope := store.scope(localOverloadScopeKey(channelID, -1))
	keyScope := store.scope(localOverloadScopeKey(channelID, keyIndex))
	store.prepare(channelScope, nowMS)
	store.prepare(keyScope, nowMS)
	if channelConfig.Enabled {
		channelScope.requests = append(channelScope.requests, nowMS)
		if localRequestThresholdReached(channelScope, nowMS, channelConfig) {
			blockLocalScope(channelScope, nowMS, channelConfig)
		}
	}
	if keyConfig.Enabled {
		keyScope.requests = append(keyScope.requests, nowMS)
		if localRequestThresholdReached(keyScope, nowMS, keyConfig) {
			blockLocalScope(keyScope, nowMS, keyConfig)
		}
	}
}

func (store *overloadLocalStore) recordTokens(channelID, keyIndex int, tokens int64, now time.Time, config model.OverloadProtection) {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	scope := store.scope(localOverloadScopeKey(channelID, keyIndex))
	store.prepare(scope, nowMS)
	scope.tokenEvents = append(scope.tokenEvents, overloadLocalTokenEvent{at: nowMS, tokens: tokens})
	if localTokenTotal(scope) >= config.TokensPerMinute {
		blockLocalScope(scope, nowMS, config)
	}
}

func (store *overloadLocalStore) block(channelID, keyIndex int, scope OverloadScope, blockedUntil int64) {
	if blockedUntil <= 0 {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	index := keyIndex
	if scope == OverloadScopeChannel {
		index = -1
	}
	localScope := store.scope(localOverloadScopeKey(channelID, index))
	if blockedUntil > localScope.blockedUntil {
		localScope.blockedUntil = blockedUntil
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

func localScopeThresholdReached(scope *overloadLocalScope, nowMS int64, config model.OverloadProtection) bool {
	if !config.Enabled {
		return false
	}
	if localRequestThresholdReached(scope, nowMS, config) {
		return true
	}
	return config.ConcurrentRequests > 0 && len(scope.leases) >= config.ConcurrentRequests
}

func localRequestThresholdReached(scope *overloadLocalScope, nowMS int64, config model.OverloadProtection) bool {
	if config.RequestsPerMinute > 0 && len(scope.requests) >= config.RequestsPerMinute {
		return true
	}
	if config.RequestsPerSecond <= 0 {
		return false
	}
	secondCount := 0
	for i := len(scope.requests) - 1; i >= 0 && scope.requests[i] >= nowMS-999; i-- {
		secondCount++
	}
	return secondCount >= config.RequestsPerSecond
}

func localTokenTotal(scope *overloadLocalScope) int64 {
	var total int64
	for _, event := range scope.tokenEvents {
		if event.tokens <= 0 {
			continue
		}
		if total > math.MaxInt64-event.tokens {
			return math.MaxInt64
		}
		total += event.tokens
	}
	return total
}

func blockLocalScope(scope *overloadLocalScope, nowMS int64, config model.OverloadProtection) {
	candidate := nowMS + overloadRecoveryMilliseconds(config)
	if candidate > scope.blockedUntil {
		scope.blockedUntil = candidate
	}
}

func (store *overloadLocalStore) scope(key string) *overloadLocalScope {
	scope := store.scopes[key]
	if scope == nil {
		scope = &overloadLocalScope{leases: make(map[string]int64)}
		store.scopes[key] = scope
	}
	return scope
}

func (store *overloadLocalStore) prepare(scope *overloadLocalScope, nowMS int64) {
	store.cleanup(scope, nowMS)
	if scope.blockedUntil > 0 && scope.blockedUntil <= nowMS {
		scope.blockedUntil = 0
		scope.requests = nil
		scope.tokenEvents = nil
	}
}

func (store *overloadLocalStore) cleanup(scope *overloadLocalScope, nowMS int64) {
	requestFirst := 0
	for requestFirst < len(scope.requests) && scope.requests[requestFirst] <= nowMS-overloadWindow.Milliseconds() {
		requestFirst++
	}
	scope.requests = scope.requests[requestFirst:]
	tokenFirst := 0
	for tokenFirst < len(scope.tokenEvents) && scope.tokenEvents[tokenFirst].at <= nowMS-overloadWindow.Milliseconds() {
		tokenFirst++
	}
	scope.tokenEvents = scope.tokenEvents[tokenFirst:]
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
		overloadBlockedKey(channelID, "channel"),
		overloadRequestKey(channelID, fmt.Sprintf("key:%d", keyIndex)),
		overloadLeaseKey(channelID, fmt.Sprintf("key:%d", keyIndex)),
		overloadBlockedKey(channelID, fmt.Sprintf("key:%d", keyIndex)),
		overloadTokenKey(channelID, keyIndex),
	}
}

func overloadLeaseKeys(channelID, keyIndex int) []string {
	keys := overloadRedisKeys(channelID, keyIndex)
	return []string{keys[1], keys[4]}
}

func overloadRequestKey(channelID int, scope string) string {
	return fmt.Sprintf("%s:{%d}:%s:requests", overloadRedisNamespace, channelID, scope)
}

func overloadLeaseKey(channelID int, scope string) string {
	return fmt.Sprintf("%s:{%d}:%s:leases", overloadRedisNamespace, channelID, scope)
}

func overloadBlockedKey(channelID int, scope string) string {
	return fmt.Sprintf("%s:{%d}:%s:blocked_until", overloadRedisNamespace, channelID, scope)
}

func overloadTokenKey(channelID, keyIndex int) string {
	return fmt.Sprintf("%s:{%d}:key:%d:tokens", overloadRedisNamespace, channelID, keyIndex)
}

func localOverloadScopeKey(channelID, keyIndex int) string {
	return fmt.Sprintf("%d:%d", channelID, keyIndex)
}

func overloadRecoveryMilliseconds(config model.OverloadProtection) int64 {
	seconds := config.RecoverySeconds
	if seconds <= 0 {
		seconds = model.DefaultOverloadRecoverySeconds
	}
	return int64(seconds) * time.Second.Milliseconds()
}

func parseOverloadScriptResult(result interface{}) (OverloadScope, int64, error) {
	values, ok := result.([]interface{})
	if !ok || len(values) != 2 {
		return "", 0, fmt.Errorf("unexpected overload script result: %T", result)
	}
	scopeCode, err := overloadScriptInt64(values[0])
	if err != nil {
		return "", 0, err
	}
	blockedUntil, err := overloadScriptInt64(values[1])
	if err != nil {
		return "", 0, err
	}
	switch scopeCode {
	case 0:
		return "", blockedUntil, nil
	case 1:
		return OverloadScopeChannel, blockedUntil, nil
	case 2:
		return OverloadScopeMultiKey, blockedUntil, nil
	default:
		return "", 0, fmt.Errorf("unexpected overload scope code: %d", scopeCode)
	}
}

func overloadScriptInt64(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected overload script integer: %T", value)
	}
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
