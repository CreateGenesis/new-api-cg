package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMultiKeyCacheAffinityLocalKeepsPreferredAtThreshold(t *testing.T) {
	now := time.Unix(100, 0)
	counter := newMultiKeyCacheAffinityLocalCounter()
	counter.buckets[1] = map[int64]map[string]int64{
		100: {"hot": 55, "cold": 20, "middle": 25},
	}

	selected := counter.selectAndIncrement(1, []string{"hot", "cold", "middle"}, []string{"hot"}, 60, 35, now)

	assert.Equal(t, "hot", selected)
	assert.Equal(t, int64(56), counter.buckets[1][100]["hot"])
}

func TestMultiKeyCacheAffinityLocalFallsBackWhenThresholdExceeded(t *testing.T) {
	now := time.Unix(100, 0)
	counter := newMultiKeyCacheAffinityLocalCounter()
	counter.buckets[2] = map[int64]map[string]int64{
		100: {"hot": 70, "cold": 10, "middle": 20},
	}

	selected := counter.selectAndIncrement(2, []string{"hot", "cold", "middle"}, []string{"hot"}, 60, 35, now)

	assert.Equal(t, "cold", selected)
}

func TestMultiKeyCacheAffinityLocalUsesLeastRequestedAmongBestMatches(t *testing.T) {
	now := time.Unix(100, 0)
	counter := newMultiKeyCacheAffinityLocalCounter()
	counter.buckets[3] = map[int64]map[string]int64{
		100: {"match-a": 5, "match-b": 2, "cold": 0},
	}

	selected := counter.selectAndIncrement(3, []string{"match-a", "match-b", "cold"}, []string{"match-a", "match-b"}, 60, 100, now)

	assert.Equal(t, "match-b", selected)
}

func TestMultiKeyCacheAffinityLocalMissUsesColdest(t *testing.T) {
	now := time.Unix(100, 0)
	counter := newMultiKeyCacheAffinityLocalCounter()
	counter.buckets[4] = map[int64]map[string]int64{
		100: {"hot": 5, "cold": 1},
	}

	selected := counter.selectAndIncrement(4, []string{"hot", "cold"}, nil, 60, 35, now)

	assert.Equal(t, "cold", selected)
}
