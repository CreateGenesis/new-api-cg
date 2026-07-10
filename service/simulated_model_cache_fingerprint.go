package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/go-redis/redis/v8"
)

const (
	simulatedModelCacheFingerprintMinRunes        = 64
	simulatedModelCacheFingerprintAverageRunes    = 128
	simulatedModelCacheFingerprintMaxRunes        = 256
	simulatedModelCacheMaxFingerprintRunes        = 250000
	simulatedModelCacheMaxFingerprintEncodedBytes = 512 * 1024
)

var errSimulatedModelCachePromptTooLarge = errors.New("simulated model cache prompt is too large")

var simulatedModelCacheExtendTTLScript = redis.NewScript(`
local current = redis.call("PTTL", KEYS[1])
local requested = tonumber(ARGV[1])
if current < 0 or current < requested then
  return redis.call("PEXPIRE", KEYS[1], requested)
end
return 0
`)

type simulatedModelCacheFingerprintSymbol struct {
	HashHigh   uint64
	HashLow    uint64
	RuneLength uint16
}

type simulatedModelCacheFingerprintChunk struct {
	HashHigh   uint64 `json:"h"`
	HashLow    uint64 `json:"l"`
	RuneLength uint16 `json:"r"`
}

func (c simulatedModelCacheFingerprintChunk) symbol() simulatedModelCacheFingerprintSymbol {
	return simulatedModelCacheFingerprintSymbol{
		HashHigh:   c.HashHigh,
		HashLow:    c.HashLow,
		RuneLength: c.RuneLength,
	}
}

type simulatedModelCachePromptFingerprint struct {
	Version    string                                `json:"v"`
	TotalRunes int                                   `json:"t"`
	Chunks     []simulatedModelCacheFingerprintChunk `json:"c"`
}

func buildSimulatedModelCachePromptFingerprint(ctx context.Context, prompt string) (simulatedModelCachePromptFingerprint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fingerprint := simulatedModelCachePromptFingerprint{Version: SimulatedModelCacheFingerprintVersion}
	if prompt == "" {
		return fingerprint, nil
	}

	chunkStart := 0
	chunkRunes := 0
	gearHash := uint64(0)
	position := 0
	for position < len(prompt) {
		if fingerprint.TotalRunes&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return simulatedModelCachePromptFingerprint{}, err
			}
		}
		r, size := utf8.DecodeRuneInString(prompt[position:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		position += size
		fingerprint.TotalRunes++
		if fingerprint.TotalRunes > simulatedModelCacheMaxFingerprintRunes {
			return simulatedModelCachePromptFingerprint{}, errSimulatedModelCachePromptTooLarge
		}
		chunkRunes++
		gearHash = (gearHash << 1) + simulatedModelCacheGearValue(r)

		atBoundary := chunkRunes >= simulatedModelCacheFingerprintMinRunes &&
			(gearHash&(simulatedModelCacheFingerprintAverageRunes-1) == 0 || chunkRunes >= simulatedModelCacheFingerprintMaxRunes)
		if !atBoundary && position < len(prompt) {
			continue
		}
		fingerprint.Chunks = append(fingerprint.Chunks, simulatedModelCacheFingerprintChunkForText(prompt[chunkStart:position], chunkRunes))
		chunkStart = position
		chunkRunes = 0
		gearHash = 0
	}
	return fingerprint, nil
}

func simulatedModelCacheGearValue(r rune) uint64 {
	x := uint64(uint32(r)) + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func simulatedModelCacheFingerprintChunkForText(text string, runeLength int) simulatedModelCacheFingerprintChunk {
	digest := sha256.Sum256([]byte(text))
	return simulatedModelCacheFingerprintChunk{
		HashHigh:   binary.BigEndian.Uint64(digest[0:8]),
		HashLow:    binary.BigEndian.Uint64(digest[8:16]),
		RuneLength: uint16(runeLength),
	}
}

type simulatedModelCacheFingerprintSuffixState struct {
	Length   int
	Link     int
	FirstPos int
	Next     map[simulatedModelCacheFingerprintSymbol]int
}

type simulatedModelCacheFingerprintMatcher struct {
	states      []simulatedModelCacheFingerprintSuffixState
	runeOffsets []int
	totalRunes  int
}

func newSimulatedModelCacheFingerprintMatcher(current simulatedModelCachePromptFingerprint) *simulatedModelCacheFingerprintMatcher {
	matcher := &simulatedModelCacheFingerprintMatcher{
		states:      []simulatedModelCacheFingerprintSuffixState{{Link: -1, FirstPos: -1}},
		runeOffsets: make([]int, len(current.Chunks)+1),
		totalRunes:  current.TotalRunes,
	}
	last := 0
	for index, chunk := range current.Chunks {
		matcher.runeOffsets[index+1] = matcher.runeOffsets[index] + int(chunk.RuneLength)
		symbol := chunk.symbol()
		currentState := len(matcher.states)
		matcher.states = append(matcher.states, simulatedModelCacheFingerprintSuffixState{
			Length:   matcher.states[last].Length + 1,
			FirstPos: index,
			Next:     map[simulatedModelCacheFingerprintSymbol]int{},
		})
		previous := last
		for previous != -1 {
			if matcher.states[previous].Next == nil {
				matcher.states[previous].Next = map[simulatedModelCacheFingerprintSymbol]int{}
			}
			if _, exists := matcher.states[previous].Next[symbol]; exists {
				break
			}
			matcher.states[previous].Next[symbol] = currentState
			previous = matcher.states[previous].Link
		}
		if previous == -1 {
			matcher.states[currentState].Link = 0
		} else {
			nextState := matcher.states[previous].Next[symbol]
			if matcher.states[previous].Length+1 == matcher.states[nextState].Length {
				matcher.states[currentState].Link = nextState
			} else {
				cloneTransitions := make(map[simulatedModelCacheFingerprintSymbol]int, len(matcher.states[nextState].Next))
				for key, value := range matcher.states[nextState].Next {
					cloneTransitions[key] = value
				}
				clone := len(matcher.states)
				matcher.states = append(matcher.states, simulatedModelCacheFingerprintSuffixState{
					Length:   matcher.states[previous].Length + 1,
					Link:     matcher.states[nextState].Link,
					FirstPos: matcher.states[nextState].FirstPos,
					Next:     cloneTransitions,
				})
				for previous != -1 {
					transition, exists := matcher.states[previous].Next[symbol]
					if !exists || transition != nextState {
						break
					}
					matcher.states[previous].Next[symbol] = clone
					previous = matcher.states[previous].Link
				}
				matcher.states[nextState].Link = clone
				matcher.states[currentState].Link = clone
			}
		}
		last = currentState
	}
	return matcher
}

func (m *simulatedModelCacheFingerprintMatcher) match(ctx context.Context, candidate simulatedModelCachePromptFingerprint) float64 {
	if m == nil || m.totalRunes == 0 || len(m.states) == 0 {
		return 0
	}
	state := 0
	length := 0
	bestRunes := 0
	for index, chunk := range candidate.Chunks {
		if index&255 == 0 && ctx.Err() != nil {
			return 0
		}
		symbol := chunk.symbol()
		if next, exists := m.states[state].Next[symbol]; exists {
			state = next
			length++
		} else {
			for state != -1 {
				state = m.states[state].Link
				if state == -1 {
					break
				}
				if next, exists := m.states[state].Next[symbol]; exists {
					length = m.states[state].Length + 1
					state = next
					break
				}
			}
			if state == -1 {
				state = 0
				length = 0
			}
		}
		if length == 0 {
			continue
		}
		end := m.states[state].FirstPos + 1
		start := end - length
		if start < 0 || end >= len(m.runeOffsets) {
			continue
		}
		matchedRunes := m.runeOffsets[end] - m.runeOffsets[start]
		if matchedRunes > bestRunes {
			bestRunes = matchedRunes
			if bestRunes >= m.totalRunes {
				return 1
			}
		}
	}
	return float64(bestRunes) / float64(m.totalRunes)
}

func SimulatedModelCacheMatchRatio(cachedPrompt string, currentPrompt string) float64 {
	current, err := buildSimulatedModelCachePromptFingerprint(context.Background(), currentPrompt)
	if err != nil || current.TotalRunes == 0 {
		return 0
	}
	cached, err := buildSimulatedModelCachePromptFingerprint(context.Background(), cachedPrompt)
	if err != nil {
		return 0
	}
	return newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), cached)
}

func FindSimulatedModelCachePartialMatch(ctx context.Context, req SimulatedModelCachePartialMatchRequest) (SimulatedModelCachePartialMatch, error) {
	return findSimulatedModelCachePartialMatch(ctx, req, false)
}

func runSimulatedModelCachePartialMatch(ctx context.Context, req SimulatedModelCachePartialMatchRequest) (SimulatedModelCachePartialMatch, error) {
	return findSimulatedModelCachePartialMatch(ctx, req, true)
}

func findSimulatedModelCachePartialMatch(ctx context.Context, req SimulatedModelCachePartialMatchRequest, storeCurrent bool) (result SimulatedModelCachePartialMatch, resultErr error) {
	startedAt := time.Now()
	result = SimulatedModelCachePartialMatch{FingerprintVersion: SimulatedModelCacheFingerprintVersion}
	defer func() {
		result.MatchDuration = time.Since(startedAt)
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		result.BypassReason = SimulatedModelCacheBypassRequestCanceled
		result.MatchDuration = time.Since(startedAt)
		return result, err
	}
	if !common.RedisEnabled || common.RDB == nil {
		result.BypassReason = SimulatedModelCacheBypassRedisUnavailable
		result.MatchDuration = time.Since(startedAt)
		return result, nil
	}
	if strings.TrimSpace(req.PromptText) == "" {
		result.MatchDuration = time.Since(startedAt)
		return result, nil
	}

	var currentReservation *SimulatedModelCacheMemoryReservation
	if !req.currentMemoryReserved {
		currentReservation = ReserveSimulatedModelCacheMemory(estimateSimulatedModelCacheCurrentMatchBytes(req.PromptText))
		if currentReservation == nil {
			result.BypassReason = SimulatedModelCacheBypassMemoryBudget
			result.MatchDuration = time.Since(startedAt)
			return result, nil
		}
		defer currentReservation.Release()
	}
	current, err := buildSimulatedModelCachePromptFingerprint(ctx, req.PromptText)
	if err != nil {
		if errors.Is(err, errSimulatedModelCachePromptTooLarge) {
			result.BypassReason = SimulatedModelCacheBypassPromptTooLarge
		} else {
			result.BypassReason = SimulatedModelCacheBypassRequestCanceled
		}
		result.MatchDuration = time.Since(startedAt)
		return result, err
	}
	if storeCurrent {
		defer func() {
			if ctx.Err() != nil {
				return
			}
			if err := storeSimulatedModelCachePromptFingerprint(ctx, req, current, ttlFromSeconds(req.TTLSeconds)); err != nil && resultErr == nil && !result.Found && result.BypassReason == "" {
				result.BypassReason = SimulatedModelCacheBypassRedisError
				resultErr = err
			}
		}()
	}
	matcher := newSimulatedModelCacheFingerprintMatcher(current)

	indexKey := simulatedModelCacheScopeIndexKey(req.UserID, req.Model)
	promptIDs, err := common.RDB.ZRevRange(ctx, indexKey, 0, simulatedModelCacheMaxEntriesPerScope-1).Result()
	if err != nil {
		result.BypassReason = SimulatedModelCacheBypassRedisError
		result.MatchDuration = time.Since(startedAt)
		return result, err
	}
	result.CandidateCount = len(promptIDs)
	if len(promptIDs) == 0 {
		result.MatchDuration = time.Since(startedAt)
		return result, nil
	}

	candidateReservation := ReserveSimulatedModelCacheMemory(int64(len(promptIDs)) * simulatedModelCacheMaxFingerprintEncodedBytes)
	if candidateReservation == nil {
		result.BypassReason = SimulatedModelCacheBypassMemoryBudget
		result.MatchDuration = time.Since(startedAt)
		return result, nil
	}
	defer candidateReservation.Release()
	keys := make([]string, len(promptIDs))
	for index, promptID := range promptIDs {
		keys[index] = simulatedModelCacheFingerprintKey(promptID)
	}
	rawFingerprints, err := common.RDB.MGet(ctx, keys...).Result()
	if err != nil {
		result.BypassReason = SimulatedModelCacheBypassRedisError
		result.MatchDuration = time.Since(startedAt)
		return result, err
	}

	minRatio := req.MinMatchRatio
	if minRatio <= 0 {
		minRatio = 0.01
	}
	if minRatio > 1 {
		minRatio = 1
	}
	stalePromptIDs := make([]interface{}, 0)
	for index, raw := range rawFingerprints {
		if index&7 == 0 {
			if err := ctx.Err(); err != nil {
				result.BypassReason = SimulatedModelCacheBypassRequestCanceled
				result.MatchDuration = time.Since(startedAt)
				return result, err
			}
		}
		rawString, ok := raw.(string)
		if !ok || rawString == "" || len(rawString) > simulatedModelCacheMaxFingerprintEncodedBytes {
			stalePromptIDs = append(stalePromptIDs, promptIDs[index])
			continue
		}
		var candidate simulatedModelCachePromptFingerprint
		if common.UnmarshalJsonStr(rawString, &candidate) != nil || candidate.Version != SimulatedModelCacheFingerprintVersion {
			stalePromptIDs = append(stalePromptIDs, promptIDs[index])
			continue
		}
		ratio := matcher.match(ctx, candidate)
		if err := ctx.Err(); err != nil {
			result.BypassReason = SimulatedModelCacheBypassRequestCanceled
			result.MatchDuration = time.Since(startedAt)
			return result, err
		}
		if ratio >= minRatio && (!result.Found || ratio > result.MatchRatio) {
			result.Found = true
			result.MatchRatio = ratio
			if ratio >= 1 {
				break
			}
		}
	}
	if len(stalePromptIDs) > 0 && ctx.Err() == nil {
		_ = common.RDB.ZRem(ctx, indexKey, stalePromptIDs...).Err()
	}
	result.MatchDuration = time.Since(startedAt)
	return result, nil
}

func StoreSimulatedModelCachePromptFingerprint(ctx context.Context, req SimulatedModelCachePartialMatchRequest) error {
	if strings.TrimSpace(req.PromptText) == "" {
		return nil
	}
	reservation := ReserveSimulatedModelCacheMemory(estimateSimulatedModelCacheCurrentMatchBytes(req.PromptText))
	if reservation == nil {
		return ErrSimulatedModelCacheMemoryBudget
	}
	defer reservation.Release()
	fingerprint, err := buildSimulatedModelCachePromptFingerprint(ctx, req.PromptText)
	if err != nil {
		return err
	}
	return storeSimulatedModelCachePromptFingerprint(ctx, req, fingerprint, ttlFromSeconds(req.TTLSeconds))
}

func storeSimulatedModelCachePromptFingerprint(ctx context.Context, req SimulatedModelCachePartialMatchRequest, fingerprint simulatedModelCachePromptFingerprint, ttl time.Duration) error {
	cleanupLegacySimulatedModelCacheReplayFiles()
	raw, err := common.Marshal(fingerprint)
	if err != nil {
		return err
	}
	if len(raw) > simulatedModelCacheMaxFingerprintEncodedBytes {
		return fmt.Errorf("simulated model cache fingerprint exceeds %d bytes", simulatedModelCacheMaxFingerprintEncodedBytes)
	}
	promptID := sha256Hex([]byte(req.PromptText))
	fingerprintKey := simulatedModelCacheFingerprintKey(promptID)
	stored, err := common.RDB.SetNX(ctx, fingerprintKey, string(raw), ttl).Result()
	if err != nil {
		return err
	}
	if !stored {
		if err := extendSimulatedModelCacheTTL(ctx, fingerprintKey, ttl); err != nil {
			return err
		}
	}

	indexKey := simulatedModelCacheScopeIndexKey(req.UserID, req.Model)
	now := time.Now()
	if err := common.RDB.ZAdd(ctx, indexKey, &redis.Z{Score: float64(now.UnixNano()), Member: promptID}).Err(); err != nil {
		return err
	}
	if err := extendSimulatedModelCacheTTL(ctx, indexKey, ttl); err != nil {
		return err
	}
	return evictSimulatedModelCacheOldestPromptIDs(ctx, indexKey, simulatedModelCacheMaxEntriesPerScope)
}

func extendSimulatedModelCacheTTL(ctx context.Context, key string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	return simulatedModelCacheExtendTTLScript.Run(ctx, common.RDB, []string{key}, ttl.Milliseconds()).Err()
}

func evictSimulatedModelCacheOldestPromptIDs(ctx context.Context, indexKey string, limit int) error {
	if limit <= 0 {
		return nil
	}
	count, err := common.RDB.ZCard(ctx, indexKey).Result()
	if err != nil || count <= int64(limit) {
		return err
	}
	return common.RDB.ZRemRangeByRank(ctx, indexKey, 0, count-int64(limit)-1).Err()
}

func simulatedModelCacheFingerprintKey(promptID string) string {
	return fmt.Sprintf("%s:fingerprint:%s", simulatedModelCacheKeyPrefix, promptID)
}
