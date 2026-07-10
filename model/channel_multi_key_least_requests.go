package model

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/go-redis/redis/v8"
)

const (
	DefaultMultiKeyLeastRequestsWindowSeconds = 60
	MinMultiKeyLeastRequestsWindowSeconds     = 10
	MaxMultiKeyLeastRequestsWindowSeconds     = 3600
	MultiKeyLeastRequestsBucketSeconds        = 10
)

const multiKeyLeastRequestsRedisNamespace = "new-api:multi_key_least_requests:v1"

var multiKeyLeastRequestsRedisScript = redis.NewScript(`
local candidate_count = tonumber(ARGV[1])
local min_count = nil
local tied = {}

for i = 1, candidate_count do
  local field = ARGV[i + 1]
  local total = 0
  for key_index = 1, #KEYS do
    total = total + tonumber(redis.call('HGET', KEYS[key_index], field) or '0')
  end
  if min_count == nil or total < min_count then
    min_count = total
    tied = {field}
  elseif total == min_count then
    tied[#tied + 1] = field
  end
end

local random_offset = tonumber(ARGV[candidate_count + 2])
local selected = tied[(random_offset % #tied) + 1]
redis.call('HINCRBY', KEYS[1], selected, 1)
redis.call('EXPIREAT', KEYS[1], tonumber(ARGV[candidate_count + 3]))
return tonumber(selected)
`)

type multiKeyLeastRequestsLocalCounter struct {
	mu      sync.Mutex
	buckets map[int]map[int64]map[int]int64
}

func newMultiKeyLeastRequestsLocalCounter() *multiKeyLeastRequestsLocalCounter {
	return &multiKeyLeastRequestsLocalCounter{
		buckets: make(map[int]map[int64]map[int]int64),
	}
}

var (
	multiKeyLeastRequestsLocal       = newMultiKeyLeastRequestsLocalCounter()
	selectMultiKeyLeastRequestsRedis = selectMultiKeyLeastRequestsIndexRedis
)

func NormalizeMultiKeyLeastRequestsWindowSeconds(windowSeconds int) (int, error) {
	if windowSeconds == 0 {
		return DefaultMultiKeyLeastRequestsWindowSeconds, nil
	}
	if windowSeconds < MinMultiKeyLeastRequestsWindowSeconds ||
		windowSeconds > MaxMultiKeyLeastRequestsWindowSeconds ||
		windowSeconds%MultiKeyLeastRequestsBucketSeconds != 0 {
		return 0, fmt.Errorf("multi-key least requests window must be between %d and %d seconds and a multiple of %d", MinMultiKeyLeastRequestsWindowSeconds, MaxMultiKeyLeastRequestsWindowSeconds, MultiKeyLeastRequestsBucketSeconds)
	}
	return windowSeconds, nil
}

func ValidateAndNormalizeMultiKeySettings(info *ChannelInfo) error {
	if info == nil {
		return nil
	}
	switch info.MultiKeyMode {
	case "", constant.MultiKeyModeRandom, constant.MultiKeyModePolling, constant.MultiKeyModeAffinity, constant.MultiKeyModeLeastRequests:
	default:
		return fmt.Errorf("unsupported multi-key mode: %s", info.MultiKeyMode)
	}

	windowSeconds, err := NormalizeMultiKeyLeastRequestsWindowSeconds(info.MultiKeyLeastRequestsWindowSeconds)
	if err != nil {
		return err
	}
	info.MultiKeyLeastRequestsWindowSeconds = windowSeconds
	return nil
}

func (channel *Channel) selectMultiKeyLeastRequestsIndex(enabledIndexes []int) int {
	windowSeconds, err := NormalizeMultiKeyLeastRequestsWindowSeconds(channel.ChannelInfo.MultiKeyLeastRequestsWindowSeconds)
	if err != nil {
		common.SysError(fmt.Sprintf("invalid multi-key least requests window, using default: channel_id=%d, err=%v", channel.Id, err))
		windowSeconds = DefaultMultiKeyLeastRequestsWindowSeconds
	}
	now := time.Now()

	if channel.Id > 0 && common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		selectedIndex, redisErr := selectMultiKeyLeastRequestsRedis(ctx, common.RDB, channel.Id, enabledIndexes, windowSeconds, now)
		cancel()
		if redisErr == nil {
			return selectedIndex
		}
		common.SysError(fmt.Sprintf("multi-key least requests Redis selection failed, using local fallback: channel_id=%d, err=%v", channel.Id, redisErr))
	}

	return multiKeyLeastRequestsLocal.selectAndIncrement(channel.Id, enabledIndexes, windowSeconds, now)
}

func selectMultiKeyLeastRequestsIndexRedis(ctx context.Context, client *redis.Client, channelID int, enabledIndexes []int, windowSeconds int, now time.Time) (int, error) {
	if len(enabledIndexes) == 0 {
		return 0, fmt.Errorf("no enabled indexes")
	}

	currentBucket := now.Unix() / MultiKeyLeastRequestsBucketSeconds * MultiKeyLeastRequestsBucketSeconds
	bucketCount := windowSeconds / MultiKeyLeastRequestsBucketSeconds
	keys := make([]string, 0, bucketCount)
	for offset := 0; offset < bucketCount; offset++ {
		bucket := currentBucket - int64(offset*MultiKeyLeastRequestsBucketSeconds)
		keys = append(keys, multiKeyLeastRequestsRedisBucketKey(channelID, bucket))
	}

	args := make([]interface{}, 0, len(enabledIndexes)+3)
	args = append(args, len(enabledIndexes))
	for _, index := range enabledIndexes {
		args = append(args, index)
	}
	args = append(args, rand.Int31())
	expireAt := currentBucket + int64(windowSeconds+MultiKeyLeastRequestsBucketSeconds)
	args = append(args, expireAt)

	selectedIndex, err := multiKeyLeastRequestsRedisScript.Run(ctx, client, keys, args...).Int()
	if err != nil {
		return 0, err
	}
	for _, index := range enabledIndexes {
		if selectedIndex == index {
			return selectedIndex, nil
		}
	}
	return 0, fmt.Errorf("Redis selected unavailable multi-key index %d", selectedIndex)
}

func multiKeyLeastRequestsRedisBucketKey(channelID int, bucket int64) string {
	return fmt.Sprintf("%s:{%d}:%d", multiKeyLeastRequestsRedisNamespace, channelID, bucket)
}

func (counter *multiKeyLeastRequestsLocalCounter) selectAndIncrement(channelID int, enabledIndexes []int, windowSeconds int, now time.Time) int {
	counter.mu.Lock()
	defer counter.mu.Unlock()

	currentBucket := now.Unix() / MultiKeyLeastRequestsBucketSeconds * MultiKeyLeastRequestsBucketSeconds
	earliestBucket := currentBucket - int64(windowSeconds-MultiKeyLeastRequestsBucketSeconds)
	channelBuckets := counter.buckets[channelID]
	if channelBuckets == nil {
		channelBuckets = make(map[int64]map[int]int64)
		counter.buckets[channelID] = channelBuckets
	}
	for bucket := range channelBuckets {
		if bucket < earliestBucket {
			delete(channelBuckets, bucket)
		}
	}

	minimum := int64(math.MaxInt64)
	leastUsedIndexes := make([]int, 0, len(enabledIndexes))
	for _, index := range enabledIndexes {
		var total int64
		for bucket, counts := range channelBuckets {
			if bucket >= earliestBucket && bucket <= currentBucket {
				total += counts[index]
			}
		}
		if total < minimum {
			minimum = total
			leastUsedIndexes = leastUsedIndexes[:0]
			leastUsedIndexes = append(leastUsedIndexes, index)
		} else if total == minimum {
			leastUsedIndexes = append(leastUsedIndexes, index)
		}
	}

	selectedIndex := leastUsedIndexes[rand.Intn(len(leastUsedIndexes))]
	currentCounts := channelBuckets[currentBucket]
	if currentCounts == nil {
		currentCounts = make(map[int]int64)
		channelBuckets[currentBucket] = currentCounts
	}
	currentCounts[selectedIndex]++
	return selectedIndex
}
