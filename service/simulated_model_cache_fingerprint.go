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
	simulatedModelCacheFineFingerprintMaxRunes    = 1024
	simulatedModelCacheFineFingerprintWindowRunes = 3
	simulatedModelCacheFineFingerprintHashBytes   = 16
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
	FineHashes []byte                                `json:"s,omitempty"`
	KeyDigest  string                                `json:"k,omitempty"`
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
	fineRunes := make([]rune, 0, simulatedModelCacheFineFingerprintMaxRunes)
	collectFineRunes := true
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
		if collectFineRunes {
			if len(fineRunes) < simulatedModelCacheFineFingerprintMaxRunes {
				fineRunes = append(fineRunes, r)
			} else {
				fineRunes = nil
				collectFineRunes = false
			}
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
	if collectFineRunes && len(fineRunes) >= simulatedModelCacheFineFingerprintWindowRunes {
		fingerprint.FineHashes = make(
			[]byte,
			(len(fineRunes)-simulatedModelCacheFineFingerprintWindowRunes+1)*simulatedModelCacheFineFingerprintHashBytes,
		)
		var window [simulatedModelCacheFineFingerprintWindowRunes * 4]byte
		for index := 0; index <= len(fineRunes)-simulatedModelCacheFineFingerprintWindowRunes; index++ {
			if index&255 == 0 {
				if err := ctx.Err(); err != nil {
					return simulatedModelCachePromptFingerprint{}, err
				}
			}
			for offset := 0; offset < simulatedModelCacheFineFingerprintWindowRunes; offset++ {
				binary.BigEndian.PutUint32(window[offset*4:], uint32(fineRunes[index+offset]))
			}
			digest := sha256.Sum256(window[:])
			start := index * simulatedModelCacheFineFingerprintHashBytes
			copy(fingerprint.FineHashes[start:start+simulatedModelCacheFineFingerprintHashBytes], digest[:simulatedModelCacheFineFingerprintHashBytes])
		}
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
	fine        *simulatedModelCacheFineFingerprintMatcher
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
		matcher.states, last = appendSimulatedModelCacheFingerprintState(matcher.states, last, chunk.symbol(), index)
	}
	if current.hasUsableFineHashes() {
		matcher.fine = newSimulatedModelCacheFineFingerprintMatcher(current)
	}
	return matcher
}

func (m *simulatedModelCacheFingerprintMatcher) match(ctx context.Context, candidate simulatedModelCachePromptFingerprint) float64 {
	if m == nil || m.totalRunes == 0 {
		return 0
	}
	return float64(m.matchedRunes(ctx, candidate)) / float64(m.totalRunes)
}

func (m *simulatedModelCacheFingerprintMatcher) matchedRunes(ctx context.Context, candidate simulatedModelCachePromptFingerprint) int {
	if m == nil || m.totalRunes == 0 || len(m.states) == 0 {
		return 0
	}
	if m.fine != nil && candidate.hasUsableFineHashes() {
		return m.fine.matchedRunes(ctx, candidate.FineHashes)
	}
	state := 0
	length := 0
	bestRunes := 0
	for index, chunk := range candidate.Chunks {
		if index&255 == 0 && ctx.Err() != nil {
			return 0
		}
		symbol := chunk.symbol()
		state, length = advanceSimulatedModelCacheFingerprintState(m.states, state, length, symbol)
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
				return m.totalRunes
			}
		}
	}
	return bestRunes
}

func appendSimulatedModelCacheFingerprintState(states []simulatedModelCacheFingerprintSuffixState, last int, symbol simulatedModelCacheFingerprintSymbol, firstPos int) ([]simulatedModelCacheFingerprintSuffixState, int) {
	currentState := len(states)
	states = append(states, simulatedModelCacheFingerprintSuffixState{
		Length:   states[last].Length + 1,
		FirstPos: firstPos,
		Next:     map[simulatedModelCacheFingerprintSymbol]int{},
	})
	previous := last
	for previous != -1 {
		if states[previous].Next == nil {
			states[previous].Next = map[simulatedModelCacheFingerprintSymbol]int{}
		}
		if _, exists := states[previous].Next[symbol]; exists {
			break
		}
		states[previous].Next[symbol] = currentState
		previous = states[previous].Link
	}
	if previous == -1 {
		states[currentState].Link = 0
		return states, currentState
	}

	nextState := states[previous].Next[symbol]
	if states[previous].Length+1 == states[nextState].Length {
		states[currentState].Link = nextState
		return states, currentState
	}

	cloneTransitions := make(map[simulatedModelCacheFingerprintSymbol]int, len(states[nextState].Next))
	for key, value := range states[nextState].Next {
		cloneTransitions[key] = value
	}
	clone := len(states)
	states = append(states, simulatedModelCacheFingerprintSuffixState{
		Length:   states[previous].Length + 1,
		Link:     states[nextState].Link,
		FirstPos: states[nextState].FirstPos,
		Next:     cloneTransitions,
	})
	for previous != -1 {
		transition, exists := states[previous].Next[symbol]
		if !exists || transition != nextState {
			break
		}
		states[previous].Next[symbol] = clone
		previous = states[previous].Link
	}
	states[nextState].Link = clone
	states[currentState].Link = clone
	return states, currentState
}

func advanceSimulatedModelCacheFingerprintState(states []simulatedModelCacheFingerprintSuffixState, state int, length int, symbol simulatedModelCacheFingerprintSymbol) (int, int) {
	if next, exists := states[state].Next[symbol]; exists {
		return next, length + 1
	}
	for state != -1 {
		state = states[state].Link
		if state == -1 {
			break
		}
		if next, exists := states[state].Next[symbol]; exists {
			return next, states[state].Length + 1
		}
	}
	return 0, 0
}

type simulatedModelCacheFineFingerprintMatcher struct {
	states     []simulatedModelCacheFingerprintSuffixState
	totalRunes int
}

func newSimulatedModelCacheFineFingerprintMatcher(current simulatedModelCachePromptFingerprint) *simulatedModelCacheFineFingerprintMatcher {
	matcher := &simulatedModelCacheFineFingerprintMatcher{
		states:     []simulatedModelCacheFingerprintSuffixState{{Link: -1, FirstPos: -1}},
		totalRunes: current.TotalRunes,
	}
	last := 0
	for index := 0; index < len(current.FineHashes)/simulatedModelCacheFineFingerprintHashBytes; index++ {
		matcher.states, last = appendSimulatedModelCacheFingerprintState(
			matcher.states,
			last,
			simulatedModelCacheFineFingerprintSymbolAt(current.FineHashes, index),
			index,
		)
	}
	return matcher
}

func (m *simulatedModelCacheFineFingerprintMatcher) match(ctx context.Context, candidateHashes []byte) float64 {
	if m == nil || m.totalRunes == 0 {
		return 0
	}
	return float64(m.matchedRunes(ctx, candidateHashes)) / float64(m.totalRunes)
}

func (m *simulatedModelCacheFineFingerprintMatcher) matchedRunes(ctx context.Context, candidateHashes []byte) int {
	if m == nil || m.totalRunes == 0 || len(m.states) == 0 {
		return 0
	}
	state := 0
	length := 0
	bestRunes := 0
	symbolCount := len(candidateHashes) / simulatedModelCacheFineFingerprintHashBytes
	for index := 0; index < symbolCount; index++ {
		if index&255 == 0 && ctx.Err() != nil {
			return 0
		}
		state, length = advanceSimulatedModelCacheFingerprintState(
			m.states,
			state,
			length,
			simulatedModelCacheFineFingerprintSymbolAt(candidateHashes, index),
		)
		if length == 0 {
			continue
		}
		matchedRunes := length + simulatedModelCacheFineFingerprintWindowRunes - 1
		if matchedRunes > bestRunes {
			bestRunes = matchedRunes
			if bestRunes >= m.totalRunes {
				return m.totalRunes
			}
		}
	}
	return bestRunes
}

func simulatedModelCacheFineFingerprintSymbolAt(hashes []byte, index int) simulatedModelCacheFingerprintSymbol {
	start := index * simulatedModelCacheFineFingerprintHashBytes
	return simulatedModelCacheFingerprintSymbol{
		HashHigh: binary.BigEndian.Uint64(hashes[start : start+8]),
		HashLow:  binary.BigEndian.Uint64(hashes[start+8 : start+simulatedModelCacheFineFingerprintHashBytes]),
	}
}

func (f simulatedModelCachePromptFingerprint) hasUsableFineHashes() bool {
	if f.TotalRunes < simulatedModelCacheFineFingerprintWindowRunes || f.TotalRunes > simulatedModelCacheFineFingerprintMaxRunes {
		return false
	}
	expectedBytes := (f.TotalRunes - simulatedModelCacheFineFingerprintWindowRunes + 1) * simulatedModelCacheFineFingerprintHashBytes
	return len(f.FineHashes) == expectedBytes
}

func (f simulatedModelCachePromptFingerprint) hasValidFineHashes() bool {
	if f.TotalRunes < 0 || f.TotalRunes > simulatedModelCacheMaxFingerprintRunes {
		return false
	}
	if f.TotalRunes < simulatedModelCacheFineFingerprintWindowRunes || f.TotalRunes > simulatedModelCacheFineFingerprintMaxRunes {
		return len(f.FineHashes) == 0
	}
	return f.hasUsableFineHashes()
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
	result, err := findSimulatedModelCachePartialMatch(ctx, req)
	result.prepared = nil
	return result, err
}

func runSimulatedModelCachePartialMatch(ctx context.Context, req SimulatedModelCachePartialMatchRequest) (SimulatedModelCachePartialMatch, *SimulatedModelCachePreparedFingerprint, error) {
	result, err := findSimulatedModelCachePartialMatch(ctx, req)
	prepared := result.prepared
	result.prepared = nil
	return result, prepared, err
}

func findSimulatedModelCachePartialMatch(ctx context.Context, req SimulatedModelCachePartialMatchRequest) (result SimulatedModelCachePartialMatch, resultErr error) {
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
	result.prepared = &SimulatedModelCachePreparedFingerprint{
		request:     req,
		fingerprint: current,
	}
	matcher := newSimulatedModelCacheFingerprintMatcher(current)

	indexKey := simulatedModelCacheScopeIndexKey(req.ChannelID, req.UserID, req.Model)
	maxEntries := common.GetSimulatedModelCacheEntriesPerScope()
	promptIDs, err := common.RDB.ZRevRange(ctx, indexKey, 0, int64(maxEntries-1)).Result()
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
		keys[index] = simulatedModelCacheFingerprintKey(req.ChannelID, req.UserID, req.Model, promptID)
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
	bestMatchedRunes := 0
	preferredSet := make(map[string]struct{})
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
		if common.UnmarshalJsonStr(rawString, &candidate) != nil || candidate.Version != SimulatedModelCacheFingerprintVersion || !candidate.hasValidFineHashes() {
			stalePromptIDs = append(stalePromptIDs, promptIDs[index])
			continue
		}
		if len(req.AllowedKeyDigests) > 0 {
			if _, allowed := req.AllowedKeyDigests[candidate.KeyDigest]; !allowed {
				continue
			}
		}
		matchedRunes := matcher.matchedRunes(ctx, candidate)
		if err := ctx.Err(); err != nil {
			result.BypassReason = SimulatedModelCacheBypassRequestCanceled
			result.MatchDuration = time.Since(startedAt)
			return result, err
		}
		ratio := float64(matchedRunes) / float64(current.TotalRunes)
		if ratio < minRatio {
			continue
		}
		if matchedRunes > bestMatchedRunes {
			bestMatchedRunes = matchedRunes
			preferredSet = make(map[string]struct{})
			if candidate.KeyDigest != "" {
				preferredSet[candidate.KeyDigest] = struct{}{}
			}
		} else if matchedRunes == bestMatchedRunes && candidate.KeyDigest != "" {
			preferredSet[candidate.KeyDigest] = struct{}{}
		}
	}
	if bestMatchedRunes > 0 {
		result.Found = true
		result.MatchRatio = float64(bestMatchedRunes) / float64(current.TotalRunes)
		for digest := range preferredSet {
			result.PreferredKeyDigests = append(result.PreferredKeyDigests, digest)
		}
	}
	if len(stalePromptIDs) > 0 && ctx.Err() == nil {
		_ = common.RDB.ZRem(ctx, indexKey, stalePromptIDs...).Err()
	}
	result.MatchDuration = time.Since(startedAt)
	return result, nil
}

func StoreSimulatedModelCachePromptFingerprint(ctx context.Context, req SimulatedModelCachePartialMatchRequest) error {
	if !common.RedisEnabled || common.RDB == nil {
		return ErrSimulatedModelCacheRedisDisabled
	}
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

type SimulatedModelCachePreparedFingerprint struct {
	request     SimulatedModelCachePartialMatchRequest
	fingerprint simulatedModelCachePromptFingerprint
}

func (p *SimulatedModelCachePreparedFingerprint) BindKey(channelID int, keyDigest string) {
	if p == nil {
		return
	}
	p.request.ChannelID = channelID
	p.request.KeyDigest = keyDigest
}

func (p *SimulatedModelCachePreparedFingerprint) Store(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if !common.RedisEnabled || common.RDB == nil {
		return ErrSimulatedModelCacheRedisDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return storeSimulatedModelCachePromptFingerprint(ctx, p.request, p.fingerprint, ttlFromSeconds(p.request.TTLSeconds))
}

func storeSimulatedModelCachePromptFingerprint(ctx context.Context, req SimulatedModelCachePartialMatchRequest, fingerprint simulatedModelCachePromptFingerprint, ttl time.Duration) error {
	cleanupLegacySimulatedModelCacheReplayFiles()
	if fingerprint.Version != SimulatedModelCacheFingerprintVersion || !fingerprint.hasValidFineHashes() {
		return fmt.Errorf("invalid simulated model cache %s fingerprint", SimulatedModelCacheFingerprintVersion)
	}
	fingerprint.KeyDigest = req.KeyDigest
	raw, err := common.Marshal(fingerprint)
	if err != nil {
		return err
	}
	if len(raw) > simulatedModelCacheMaxFingerprintEncodedBytes {
		return fmt.Errorf("simulated model cache fingerprint exceeds %d bytes", simulatedModelCacheMaxFingerprintEncodedBytes)
	}
	promptID := sha256Hex([]byte(req.PromptText))
	fingerprintKey := simulatedModelCacheFingerprintKey(req.ChannelID, req.UserID, req.Model, promptID)
	if err := common.RDB.Set(ctx, fingerprintKey, string(raw), ttl).Err(); err != nil {
		return err
	}

	indexKey := simulatedModelCacheScopeIndexKey(req.ChannelID, req.UserID, req.Model)
	now := time.Now()
	if err := common.RDB.ZAdd(ctx, indexKey, &redis.Z{Score: float64(now.UnixNano()), Member: promptID}).Err(); err != nil {
		return err
	}
	if err := extendSimulatedModelCacheTTL(ctx, indexKey, ttl); err != nil {
		return err
	}
	return evictSimulatedModelCacheOldestPromptIDs(ctx, indexKey, common.GetSimulatedModelCacheEntriesPerScope())
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

func simulatedModelCacheFingerprintKey(channelID int, userID int, model string, promptID string) string {
	return fmt.Sprintf("%s:fingerprint:%d:%d:%s:%s", simulatedModelCacheKeyPrefix, channelID, userID, sha256Hex([]byte(model)), promptID)
}
