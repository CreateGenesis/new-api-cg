package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

const multiKeyCacheAffinityRedisNamespace = "new-api:multi_key_cache_affinity:v1"

var multiKeyCacheAffinityRedisScript = redis.NewScript(`
local candidate_count = tonumber(ARGV[1])
local counts = {}
local total = 0
local minimum = nil
local coldest = {}

for i = 1, candidate_count do
  local field = ARGV[i + 1]
  local count = 0
  for key_index = 1, #KEYS do
    count = count + tonumber(redis.call('HGET', KEYS[key_index], field) or '0')
  end
  counts[field] = count
  total = total + count
  if minimum == nil or count < minimum then
    minimum = count
    coldest = {field}
  elseif count == minimum then
    coldest[#coldest + 1] = field
  end
end

local cursor = candidate_count + 2
local preferred_count = tonumber(ARGV[cursor])
cursor = cursor + 1
local preferred_minimum = nil
local preferred = {}
for i = 1, preferred_count do
  local field = ARGV[cursor]
  cursor = cursor + 1
  local count = counts[field]
  if count ~= nil then
    if preferred_minimum == nil or count < preferred_minimum then
      preferred_minimum = count
      preferred = {field}
    elseif count == preferred_minimum then
      preferred[#preferred + 1] = field
    end
  end
end

local threshold = tonumber(ARGV[cursor])
local preferred_random = tonumber(ARGV[cursor + 1])
local coldest_random = tonumber(ARGV[cursor + 2])
local expire_at = tonumber(ARGV[cursor + 3])
local selected = nil
if #preferred > 0 then
  local candidate = preferred[(preferred_random % #preferred) + 1]
  local difference = counts[candidate] - minimum
  if total == 0 or difference * 100 <= threshold * total then
    selected = candidate
  end
end
if selected == nil then
  selected = coldest[(coldest_random % #coldest) + 1]
end

redis.call('HINCRBY', KEYS[1], selected, 1)
redis.call('EXPIREAT', KEYS[1], expire_at)
return selected
`)

type multiKeyCacheAffinityLocalCounter struct {
	mu      sync.Mutex
	buckets map[int]map[int64]map[string]int64
}

func newMultiKeyCacheAffinityLocalCounter() *multiKeyCacheAffinityLocalCounter {
	return &multiKeyCacheAffinityLocalCounter{buckets: make(map[int]map[int64]map[string]int64)}
}

var multiKeyCacheAffinityLocal = newMultiKeyCacheAffinityLocalCounter()

func MultiKeyKeyDigest(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func (channel *Channel) cacheAffinityThresholdPercent() int {
	if channel.ChannelInfo.MultiKeyCacheAffinityThresholdPercent == nil {
		return DefaultMultiKeyCacheAffinityThresholdPercent
	}
	return *channel.ChannelInfo.MultiKeyCacheAffinityThresholdPercent
}

func (channel *Channel) selectMultiKeyCacheAffinityIndex(keys []string, enabledIndexes []int, preferredDigests []string) int {
	windowSeconds, err := NormalizeMultiKeyLeastRequestsWindowSeconds(channel.ChannelInfo.MultiKeyLeastRequestsWindowSeconds)
	if err != nil {
		common.SysError(fmt.Sprintf("invalid multi-key cache affinity window, using default: channel_id=%d, err=%v", channel.Id, err))
		windowSeconds = DefaultMultiKeyLeastRequestsWindowSeconds
	}

	digestToIndex := make(map[string]int, len(enabledIndexes))
	digests := make([]string, 0, len(enabledIndexes))
	for _, index := range enabledIndexes {
		digest := MultiKeyKeyDigest(keys[index])
		if _, exists := digestToIndex[digest]; exists {
			continue
		}
		digestToIndex[digest] = index
		digests = append(digests, digest)
	}
	preferred := make([]string, 0, len(preferredDigests))
	for _, digest := range preferredDigests {
		if _, exists := digestToIndex[digest]; exists {
			preferred = append(preferred, digest)
		}
	}

	now := time.Now()
	threshold := channel.cacheAffinityThresholdPercent()
	selectedDigest := ""
	if channel.Id > 0 && common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		selectedDigest, err = selectMultiKeyCacheAffinityRedis(ctx, common.RDB, channel.Id, digests, preferred, windowSeconds, threshold, now)
		cancel()
		if err != nil {
			common.SysError(fmt.Sprintf("multi-key cache affinity Redis selection failed, using local fallback: channel_id=%d, err=%v", channel.Id, err))
		}
	}
	if selectedDigest == "" {
		selectedDigest = multiKeyCacheAffinityLocal.selectAndIncrement(channel.Id, digests, preferred, windowSeconds, threshold, now)
	}
	return digestToIndex[selectedDigest]
}

func selectMultiKeyCacheAffinityRedis(ctx context.Context, client *redis.Client, channelID int, digests []string, preferred []string, windowSeconds int, threshold int, now time.Time) (string, error) {
	if len(digests) == 0 {
		return "", fmt.Errorf("no enabled keys")
	}
	currentBucket := now.Unix() / MultiKeyLeastRequestsBucketSeconds * MultiKeyLeastRequestsBucketSeconds
	bucketCount := windowSeconds / MultiKeyLeastRequestsBucketSeconds
	keys := make([]string, 0, bucketCount)
	for offset := 0; offset < bucketCount; offset++ {
		bucket := currentBucket - int64(offset*MultiKeyLeastRequestsBucketSeconds)
		keys = append(keys, multiKeyCacheAffinityBucketKey(channelID, bucket))
	}
	args := make([]interface{}, 0, len(digests)+len(preferred)+6)
	args = append(args, len(digests))
	for _, digest := range digests {
		args = append(args, digest)
	}
	args = append(args, len(preferred))
	for _, digest := range preferred {
		args = append(args, digest)
	}
	args = append(args, threshold, rand.Int31(), rand.Int31(), currentBucket+int64(windowSeconds+MultiKeyLeastRequestsBucketSeconds))
	selected, err := multiKeyCacheAffinityRedisScript.Run(ctx, client, keys, args...).Text()
	if err != nil {
		return "", err
	}
	for _, digest := range digests {
		if selected == digest {
			return selected, nil
		}
	}
	return "", fmt.Errorf("Redis selected unavailable multi-key digest")
}

func multiKeyCacheAffinityBucketKey(channelID int, bucket int64) string {
	return fmt.Sprintf("%s:{%d}:%d", multiKeyCacheAffinityRedisNamespace, channelID, bucket)
}

func (counter *multiKeyCacheAffinityLocalCounter) selectAndIncrement(channelID int, digests []string, preferred []string, windowSeconds int, threshold int, now time.Time) string {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	currentBucket := now.Unix() / MultiKeyLeastRequestsBucketSeconds * MultiKeyLeastRequestsBucketSeconds
	earliestBucket := currentBucket - int64(windowSeconds-MultiKeyLeastRequestsBucketSeconds)
	channelBuckets := counter.buckets[channelID]
	if channelBuckets == nil {
		channelBuckets = make(map[int64]map[string]int64)
		counter.buckets[channelID] = channelBuckets
	}
	for bucket := range channelBuckets {
		if bucket < earliestBucket {
			delete(channelBuckets, bucket)
		}
	}
	counts := make(map[string]int64, len(digests))
	minimum := int64(math.MaxInt64)
	var total int64
	for _, digest := range digests {
		for bucket, bucketCounts := range channelBuckets {
			if bucket >= earliestBucket && bucket <= currentBucket {
				counts[digest] += bucketCounts[digest]
			}
		}
		total += counts[digest]
		if counts[digest] < minimum {
			minimum = counts[digest]
		}
	}
	coldest := make([]string, 0, len(digests))
	for _, digest := range digests {
		if counts[digest] == minimum {
			coldest = append(coldest, digest)
		}
	}
	preferredMinimum := int64(math.MaxInt64)
	preferredColdest := make([]string, 0, len(preferred))
	for _, digest := range preferred {
		count, exists := counts[digest]
		if !exists {
			continue
		}
		if count < preferredMinimum {
			preferredMinimum = count
			preferredColdest = preferredColdest[:0]
			preferredColdest = append(preferredColdest, digest)
		} else if count == preferredMinimum {
			preferredColdest = append(preferredColdest, digest)
		}
	}
	selected := ""
	if len(preferredColdest) > 0 {
		candidate := preferredColdest[rand.Intn(len(preferredColdest))]
		if total == 0 || (counts[candidate]-minimum)*100 <= int64(threshold)*total {
			selected = candidate
		}
	}
	if selected == "" {
		selected = coldest[rand.Intn(len(coldest))]
	}
	currentCounts := channelBuckets[currentBucket]
	if currentCounts == nil {
		currentCounts = make(map[string]int64)
		channelBuckets[currentBucket] = currentCounts
	}
	currentCounts[selected]++
	return selected
}
