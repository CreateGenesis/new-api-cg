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
	"github.com/go-redis/redis/v8"
)

const (
	userRequestLimitWindow         = time.Minute
	userRequestLimitLeaseTTL       = 30 * time.Second
	userRequestLimitRenewInterval  = 10 * time.Second
	userRequestLimitRedisNamespace = "new-api:user_request_limit:v1"
)

type UserRequestLimitKind string

const (
	UserRequestLimitConcurrency UserRequestLimitKind = "concurrency"
	UserRequestLimitTPM         UserRequestLimitKind = "tpm"
)

type userTokenEvent struct {
	at     int64
	tokens int64
}

type userRequestLimitScope struct {
	leases      map[string]int64
	tokenEvents []userTokenEvent
	lastSeen    int64
}

type userRequestLimitLocalStore struct {
	mu          sync.Mutex
	scopes      map[int]*userRequestLimitScope
	nextCleanup int64
}

var (
	localUserRequestLimitStore = userRequestLimitLocalStore{scopes: make(map[int]*userRequestLimitScope)}
	userRequestLimitWarnAt     atomic.Int64
)

var userRequestLimitAdmissionScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local lease_id = ARGV[2]
local lease_expiry = tonumber(ARGV[3])
local concurrency_limit = tonumber(ARGV[4])
local token_limit = tonumber(ARGV[5])

redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now - 60000)

if token_limit > 0 then
  local total = 0
  local events = redis.call('ZRANGE', KEYS[2], 0, -1, 'WITHSCORES')
  local earliest = 0
  for index = 1, #events, 2 do
    local tokens = tonumber(string.match(events[index], '|(%d+)$') or '0')
    total = total + tokens
    if earliest == 0 then earliest = tonumber(events[index + 1]) end
  end
  if total >= token_limit then
    local retry_after = earliest + 60000 - now
    if retry_after < 1 then retry_after = 1 end
    return {2, retry_after}
  end
end

if concurrency_limit > 0 and redis.call('ZCARD', KEYS[1]) >= concurrency_limit then
  return {1, 1000}
end

if concurrency_limit > 0 then
  redis.call('ZADD', KEYS[1], lease_expiry, lease_id)
  redis.call('PEXPIRE', KEYS[1], 31000)
end
return {0, 0}
`)

var userRequestLimitRecordTokensScript = redis.NewScript(`
local now = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - 60000)
redis.call('ZADD', KEYS[1], now, ARGV[2] .. '|' .. ARGV[3])
redis.call('PEXPIRE', KEYS[1], 61000)
return 1
`)

var userRequestLimitReleaseScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
return 1
`)

var userRequestLimitRenewScript = redis.NewScript(`
if redis.call('ZSCORE', KEYS[1], ARGV[1]) then
  redis.call('ZADD', KEYS[1], ARGV[2], ARGV[1])
  redis.call('PEXPIRE', KEYS[1], 31000)
end
return 1
`)

type UserRequestLimitLease struct {
	userID int
	id     string
	redis  bool
	cancel context.CancelFunc
	once   sync.Once
}

func AcquireUserRequestLimitLease(ctx context.Context, userID, concurrencyLimit int, tokenLimit int64) (*UserRequestLimitLease, UserRequestLimitKind, time.Duration, error) {
	return acquireUserRequestLimitLeaseAt(ctx, userID, concurrencyLimit, tokenLimit, time.Now())
}

func acquireUserRequestLimitLeaseAt(ctx context.Context, userID, concurrencyLimit int, tokenLimit int64, now time.Time) (*UserRequestLimitLease, UserRequestLimitKind, time.Duration, error) {
	if userID <= 0 {
		return nil, "", 0, errors.New("user ID must be positive")
	}
	if concurrencyLimit < 0 || tokenLimit < 0 {
		return nil, "", 0, errors.New("user request limits cannot be negative")
	}
	if concurrencyLimit == 0 && tokenLimit == 0 {
		return nil, "", 0, nil
	}

	leaseID := common.GetUUID()
	if common.RedisEnabled && common.RDB != nil {
		values, err := userRequestLimitAdmissionScript.Run(
			ctx,
			common.RDB,
			[]string{userRequestLimitLeaseKey(userID), userRequestLimitTokenKey(userID)},
			now.UnixMilli(),
			leaseID,
			now.Add(userRequestLimitLeaseTTL).UnixMilli(),
			concurrencyLimit,
			strconv.FormatInt(tokenLimit, 10),
		).Slice()
		if err == nil && len(values) == 2 {
			kindValue, kindErr := userRequestLimitRedisInteger(values[0])
			retryMilliseconds, retryErr := userRequestLimitRedisInteger(values[1])
			if kindErr == nil && retryErr == nil {
				if kindValue != 0 {
					kind := UserRequestLimitConcurrency
					if kindValue == 2 {
						kind = UserRequestLimitTPM
					}
					return nil, kind, time.Duration(retryMilliseconds) * time.Millisecond, nil
				}
				if concurrencyLimit == 0 {
					return nil, "", 0, nil
				}
				lease := &UserRequestLimitLease{userID: userID, id: leaseID, redis: true}
				localUserRequestLimitStore.mirrorReserve(userID, leaseID, now)
				lease.startRenewal(ctx)
				return lease, "", 0, nil
			}
			if kindErr != nil {
				err = kindErr
			} else {
				err = retryErr
			}
		} else if err == nil {
			err = fmt.Errorf("unexpected Redis user request limit reply length %d", len(values))
		}
		logUserRequestLimitRedisFallback(err)
	}

	kind, retryAfter := localUserRequestLimitStore.reserve(userID, leaseID, now, concurrencyLimit, tokenLimit)
	if kind != "" {
		return nil, kind, retryAfter, nil
	}
	if concurrencyLimit == 0 {
		return nil, "", 0, nil
	}
	lease := &UserRequestLimitLease{userID: userID, id: leaseID}
	lease.startRenewal(ctx)
	return lease, "", 0, nil
}

func RecordUserRequestLimitTokens(ctx context.Context, userID int, tokens int64) error {
	return recordUserRequestLimitTokensAt(ctx, userID, tokens, time.Now())
}

func recordUserRequestLimitTokensAt(ctx context.Context, userID int, tokens int64, now time.Time) error {
	if userID <= 0 || tokens <= 0 {
		return nil
	}
	if common.RedisEnabled && common.RDB != nil {
		eventID := common.GetUUID() + ":" + strconv.FormatInt(now.UnixNano(), 10)
		err := userRequestLimitRecordTokensScript.Run(
			ctx,
			common.RDB,
			[]string{userRequestLimitTokenKey(userID)},
			now.UnixMilli(),
			eventID,
			strconv.FormatInt(tokens, 10),
		).Err()
		if err == nil {
			localUserRequestLimitStore.recordTokens(userID, tokens, now)
			return nil
		}
		logUserRequestLimitRedisFallback(err)
	}
	localUserRequestLimitStore.recordTokens(userID, tokens, now)
	return nil
}

func (lease *UserRequestLimitLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.cancel != nil {
			lease.cancel()
		}
		if lease.redis && common.RedisEnabled && common.RDB != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := userRequestLimitReleaseScript.Run(ctx, common.RDB, []string{userRequestLimitLeaseKey(lease.userID)}, lease.id).Err()
			cancel()
			if err != nil {
				logUserRequestLimitRedisFallback(err)
			}
		}
		localUserRequestLimitStore.release(lease.userID, lease.id)
	})
}

func (lease *UserRequestLimitLease) startRenewal(ctx context.Context) {
	renewCtx, cancel := context.WithCancel(ctx)
	lease.cancel = cancel
	go func() {
		ticker := time.NewTicker(userRequestLimitRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case now := <-ticker.C:
				expiresAt := now.Add(userRequestLimitLeaseTTL).UnixMilli()
				if lease.redis && common.RedisEnabled && common.RDB != nil {
					err := userRequestLimitRenewScript.Run(renewCtx, common.RDB, []string{userRequestLimitLeaseKey(lease.userID)}, lease.id, expiresAt).Err()
					if err != nil {
						logUserRequestLimitRedisFallback(err)
					}
				}
				localUserRequestLimitStore.renew(lease.userID, lease.id, expiresAt)
			}
		}
	}()
}

func (store *userRequestLimitLocalStore) reserve(userID int, leaseID string, now time.Time, concurrencyLimit int, tokenLimit int64) (UserRequestLimitKind, time.Duration) {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	store.cleanupIdleScopes(nowMS)
	scope := store.scope(userID, nowMS)
	store.cleanupScope(scope, nowMS)

	if tokenLimit > 0 && userTokenTotal(scope) >= tokenLimit {
		retryAfter := time.Millisecond
		if len(scope.tokenEvents) > 0 {
			retryAfter = time.Duration(scope.tokenEvents[0].at+userRequestLimitWindow.Milliseconds()-nowMS) * time.Millisecond
			if retryAfter < time.Millisecond {
				retryAfter = time.Millisecond
			}
		}
		return UserRequestLimitTPM, retryAfter
	}
	if concurrencyLimit > 0 && len(scope.leases) >= concurrencyLimit {
		return UserRequestLimitConcurrency, time.Second
	}
	if concurrencyLimit > 0 {
		scope.leases[leaseID] = now.Add(userRequestLimitLeaseTTL).UnixMilli()
	}
	return "", 0
}

func (store *userRequestLimitLocalStore) mirrorReserve(userID int, leaseID string, now time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	store.cleanupIdleScopes(nowMS)
	scope := store.scope(userID, nowMS)
	store.cleanupScope(scope, nowMS)
	scope.leases[leaseID] = now.Add(userRequestLimitLeaseTTL).UnixMilli()
}

func (store *userRequestLimitLocalStore) recordTokens(userID int, tokens int64, now time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	nowMS := now.UnixMilli()
	store.cleanupIdleScopes(nowMS)
	scope := store.scope(userID, nowMS)
	store.cleanupScope(scope, nowMS)
	scope.tokenEvents = append(scope.tokenEvents, userTokenEvent{at: nowMS, tokens: tokens})
}

func (store *userRequestLimitLocalStore) renew(userID int, leaseID string, expiresAt int64) {
	store.mu.Lock()
	defer store.mu.Unlock()
	scope := store.scopes[userID]
	if scope == nil {
		return
	}
	if _, exists := scope.leases[leaseID]; exists {
		scope.leases[leaseID] = expiresAt
		scope.lastSeen = time.Now().UnixMilli()
	}
}

func (store *userRequestLimitLocalStore) release(userID int, leaseID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	scope := store.scopes[userID]
	if scope == nil {
		return
	}
	delete(scope.leases, leaseID)
	scope.lastSeen = time.Now().UnixMilli()
}

func (store *userRequestLimitLocalStore) scope(userID int, nowMS int64) *userRequestLimitScope {
	scope := store.scopes[userID]
	if scope == nil {
		scope = &userRequestLimitScope{leases: make(map[string]int64)}
		store.scopes[userID] = scope
	}
	scope.lastSeen = nowMS
	return scope
}

func (store *userRequestLimitLocalStore) cleanupScope(scope *userRequestLimitScope, nowMS int64) {
	first := 0
	for first < len(scope.tokenEvents) && scope.tokenEvents[first].at <= nowMS-userRequestLimitWindow.Milliseconds() {
		first++
	}
	scope.tokenEvents = scope.tokenEvents[first:]
	for leaseID, expiry := range scope.leases {
		if expiry <= nowMS {
			delete(scope.leases, leaseID)
		}
	}
}

func (store *userRequestLimitLocalStore) cleanupIdleScopes(nowMS int64) {
	if nowMS < store.nextCleanup {
		return
	}
	for userID, scope := range store.scopes {
		store.cleanupScope(scope, nowMS)
		if len(scope.leases) == 0 && len(scope.tokenEvents) == 0 && scope.lastSeen <= nowMS-userRequestLimitWindow.Milliseconds() {
			delete(store.scopes, userID)
		}
	}
	store.nextCleanup = nowMS + userRequestLimitWindow.Milliseconds()
}

func userTokenTotal(scope *userRequestLimitScope) int64 {
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

func userRequestLimitLeaseKey(userID int) string {
	return fmt.Sprintf("%s:{%d}:leases", userRequestLimitRedisNamespace, userID)
}

func userRequestLimitTokenKey(userID int) string {
	return fmt.Sprintf("%s:{%d}:tokens", userRequestLimitRedisNamespace, userID)
}

func userRequestLimitRedisInteger(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected Redis integer reply type %T", value)
	}
}

func logUserRequestLimitRedisFallback(err error) {
	if err == nil {
		return
	}
	now := time.Now().Unix()
	previous := userRequestLimitWarnAt.Load()
	if now-previous < 60 || !userRequestLimitWarnAt.CompareAndSwap(previous, now) {
		return
	}
	common.SysError("user request limit Redis unavailable, using local fallback: " + err.Error())
}
