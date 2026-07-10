package model

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiKeyLeastRequestsLocalSelectsMinimumAndIncrementsImmediately(t *testing.T) {
	now := time.Unix(100, 0)
	counter := newMultiKeyLeastRequestsLocalCounter()
	counter.buckets[1] = map[int64]map[int]int64{
		100: {0: 5, 1: 2, 2: 4},
	}

	selected := counter.selectAndIncrement(1, []int{0, 1, 2}, 60, now)

	assert.Equal(t, 1, selected)
	assert.Equal(t, int64(3), counter.buckets[1][100][1])
}

func TestMultiKeyLeastRequestsLocalTieOnlyUsesMinimumSet(t *testing.T) {
	now := time.Unix(100, 0)
	selected := map[int]bool{}
	for range 100 {
		counter := newMultiKeyLeastRequestsLocalCounter()
		counter.buckets[2] = map[int64]map[int]int64{
			100: {2: 10},
		}
		index := counter.selectAndIncrement(2, []int{0, 1, 2}, 60, now)
		assert.Contains(t, []int{0, 1}, index)
		selected[index] = true
	}

	assert.True(t, selected[0])
	assert.True(t, selected[1])
}

func TestMultiKeyLeastRequestsLocalIgnoresExpiredBuckets(t *testing.T) {
	now := time.Unix(100, 0)
	counter := newMultiKeyLeastRequestsLocalCounter()
	counter.buckets[3] = map[int64]map[int]int64{
		40:  {0: 100},
		100: {1: 1},
	}

	selected := counter.selectAndIncrement(3, []int{0, 1}, 60, now)

	assert.Equal(t, 0, selected)
	_, exists := counter.buckets[3][40]
	assert.False(t, exists)
}

func TestMultiKeyLeastRequestsSkipsDisabledAndExcludedIndexes(t *testing.T) {
	originalRedisEnabled := common.RedisEnabled
	originalLocalCounter := multiKeyLeastRequestsLocal
	common.RedisEnabled = false
	multiKeyLeastRequestsLocal = newMultiKeyLeastRequestsLocalCounter()
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
		multiKeyLeastRequestsLocal = originalLocalCounter
	})

	channel := &Channel{
		Id:  930001,
		Key: "key-a\nkey-b\nkey-c",
		ChannelInfo: ChannelInfo{
			IsMultiKey:                         true,
			MultiKeyMode:                       constant.MultiKeyModeLeastRequests,
			MultiKeyLeastRequestsWindowSeconds: 60,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusManuallyDisabled,
			},
		},
	}

	key, index, newAPIError := channel.GetNextEnabledKeyWithSelection("", map[int]struct{}{1: {}}, true)

	require.Nil(t, newAPIError)
	assert.Equal(t, "key-c", key)
	assert.Equal(t, 2, index)

	channel.ChannelInfo.MultiKeyStatusList[1] = common.ChannelStatusManuallyDisabled
	channel.ChannelInfo.MultiKeyStatusList[2] = common.ChannelStatusManuallyDisabled
	_, _, newAPIError = channel.GetNextEnabledKey()
	require.NotNil(t, newAPIError)
	assert.Equal(t, types.ErrorCodeChannelNoAvailableKey, newAPIError.GetErrorCode())
}

func TestMultiKeyLeastRequestsFallsBackToLocalCounterWhenRedisFails(t *testing.T) {
	originalRedisEnabled := common.RedisEnabled
	originalRDB := common.RDB
	originalSelector := selectMultiKeyLeastRequestsRedis
	originalLocalCounter := multiKeyLeastRequestsLocal
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	selectMultiKeyLeastRequestsRedis = func(context.Context, *redis.Client, int, []int, int, time.Time) (int, error) {
		return 0, errors.New("redis unavailable")
	}
	multiKeyLeastRequestsLocal = newMultiKeyLeastRequestsLocalCounter()
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled = originalRedisEnabled
		common.RDB = originalRDB
		selectMultiKeyLeastRequestsRedis = originalSelector
		multiKeyLeastRequestsLocal = originalLocalCounter
	})

	nowBucket := time.Now().Unix() / MultiKeyLeastRequestsBucketSeconds * MultiKeyLeastRequestsBucketSeconds
	multiKeyLeastRequestsLocal.buckets[930002] = map[int64]map[int]int64{
		nowBucket: {0: 4, 1: 1},
	}
	channel := &Channel{
		Id:  930002,
		Key: "key-a\nkey-b",
		ChannelInfo: ChannelInfo{
			IsMultiKey:                         true,
			MultiKeyMode:                       constant.MultiKeyModeLeastRequests,
			MultiKeyLeastRequestsWindowSeconds: 60,
		},
	}

	_, selected, newAPIError := channel.GetNextEnabledKey()

	require.Nil(t, newAPIError)
	assert.Equal(t, 1, selected)
}

func TestMultiKeyLeastRequestsWindowValidationAndLegacyDefault(t *testing.T) {
	for _, windowSeconds := range []int{10, 60, 3600} {
		normalized, err := NormalizeMultiKeyLeastRequestsWindowSeconds(windowSeconds)
		require.NoError(t, err)
		assert.Equal(t, windowSeconds, normalized)
	}

	normalized, err := NormalizeMultiKeyLeastRequestsWindowSeconds(0)
	require.NoError(t, err)
	assert.Equal(t, DefaultMultiKeyLeastRequestsWindowSeconds, normalized)

	for _, windowSeconds := range []int{-10, 9, 15, 3610} {
		_, err := NormalizeMultiKeyLeastRequestsWindowSeconds(windowSeconds)
		assert.Error(t, err)
	}

	var info ChannelInfo
	require.NoError(t, info.Scan([]byte(`{"is_multi_key":true,"multi_key_mode":"least_requests"}`)))
	assert.Equal(t, DefaultMultiKeyLeastRequestsWindowSeconds, info.MultiKeyLeastRequestsWindowSeconds)
}

func TestMultiKeyLeastRequestsRedisIsAtomicAcrossClients(t *testing.T) {
	redisURL := os.Getenv("NEW_API_TEST_REDIS_CONN_STRING")
	if redisURL == "" {
		t.Skip("NEW_API_TEST_REDIS_CONN_STRING is not set")
	}
	options, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	clientA := redis.NewClient(options)
	clientB := redis.NewClient(options)
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})

	channelID := int(time.Now().UnixNano() & 0x3fffffff)
	now := time.Now()
	currentBucket := now.Unix() / MultiKeyLeastRequestsBucketSeconds * MultiKeyLeastRequestsBucketSeconds
	keys := make([]string, 0, 6)
	for offset := range 6 {
		keys = append(keys, multiKeyLeastRequestsRedisBucketKey(channelID, currentBucket-int64(offset*MultiKeyLeastRequestsBucketSeconds)))
	}
	t.Cleanup(func() { _ = clientA.Del(context.Background(), keys...).Err() })

	const requestCount = 40
	selected := make(chan int, requestCount)
	selectErrors := make(chan error, requestCount)
	var waitGroup sync.WaitGroup
	for requestIndex := range requestCount {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			client := clientA
			if index%2 == 1 {
				client = clientB
			}
			selectedIndex, selectErr := selectMultiKeyLeastRequestsIndexRedis(context.Background(), client, channelID, []int{0, 1}, 60, now)
			if selectErr != nil {
				selectErrors <- selectErr
				return
			}
			selected <- selectedIndex
		}(requestIndex)
	}
	waitGroup.Wait()
	close(selected)
	close(selectErrors)
	for selectErr := range selectErrors {
		require.NoError(t, selectErr)
	}

	counts := map[int]int{}
	for index := range selected {
		counts[index]++
	}
	assert.Equal(t, requestCount, counts[0]+counts[1])
	difference := counts[0] - counts[1]
	if difference < 0 {
		difference = -difference
	}
	assert.LessOrEqual(t, difference, 1)
	redisCounts, err := clientA.HGetAll(context.Background(), keys[0]).Result()
	require.NoError(t, err)
	assert.Equal(t, requestCount, common.String2Int(redisCounts["0"])+common.String2Int(redisCounts["1"]))
}
