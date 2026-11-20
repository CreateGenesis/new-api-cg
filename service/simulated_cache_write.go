package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/go-redis/redis/v8"
)

const simulatedWriteBlock = 256
const maxSimulatedWriteTokens = 1048576

// Keys are chained prefix digests and share a Redis hash slot. A pending
// claim blocks another charge even if the first request loses its connection
// before it can confirm whether settlement succeeded.
var claimSimulatedCacheWrite = redis.NewScript(`
local ttl = tonumber(ARGV[1])
local owner = ARGV[2]
local upstreamFull = tonumber(ARGV[3])
local upstreamReserved = tonumber(ARGV[4])
local ttlLocked = false
local hits = 0
local writes = 0
local existing = {}
for i, key in ipairs(KEYS) do
	local state = redis.call('GET', key)
	if state then
		if string.sub(state, 1, 2) ~= 'c:' then
			return {0, 0, 0}
		end
		local originalTTL = tonumber(string.sub(state, 3))
		if not ttlLocked then
			ttl = originalTTL
			ttlLocked = true
		end
		existing[i] = originalTTL
	end
	if hits == i - 1 and (state or i <= upstreamFull) then hits = i end
end
for i, key in ipairs(KEYS) do
	if existing[i] then
		redis.call('EXPIRE', key, existing[i])
	elseif i > upstreamReserved then
		redis.call('SET', key, owner, 'EX', ttl)
		writes = writes + 1
	end
end
return {ttl, hits, writes}
`)

var confirmSimulatedCacheWrite = redis.NewScript(`
for _, key in ipairs(KEYS) do
	if redis.call('GET', key) == ARGV[1] then
		if ARGV[2] == '' then
			redis.call('DEL', key)
		else
			redis.call('SET', key, 'c:' .. ARGV[2], 'EX', ARGV[2])
		end
	end
end
return 1
`)

type SimulatedCacheWrite struct {
	prefixes []string
	ttl      int
	keys     []string
	owner    string
	Read     int
	Write    int
}

type simulatedCacheWriteText struct {
	domain string
	text   string
}

func PrepareSimulatedCacheWrite(format types.RelayFormat, model string, body []byte, settings dto.SimulatedModelCacheSettings, ttl int) *SimulatedCacheWrite {
	write := &SimulatedCacheWrite{ttl: ttl}
	if int64(len(body)) > SimulatedModelCacheResponseBufferBytes() {
		return write
	}
	var root map[string]any
	if common.Unmarshal(body, &root) != nil {
		return write
	}
	builder := simulatedModelCachePromptBuilder{model: model, cacheWrite: true}
	if settings.Multimodal != nil && settings.Multimodal.Enabled && settings.Multimodal.Validate() == nil {
		builder.settings = *settings.Multimodal
	}
	builder.addControl("protocol", string(format))
	switch format {
	case types.RelayFormatOpenAI:
		buildOpenAIChatSimulatedModelCachePrompt(&builder, root)
	case types.RelayFormatOpenAIResponses:
		if root["previous_response_id"] != nil || root["conversation"] != nil {
			return write
		}
		buildOpenAIResponsesSimulatedModelCachePrompt(&builder, root)
	case types.RelayFormatClaude:
		buildClaudeSimulatedModelCachePrompt(&builder, root)
	}
	if builder.fatalReason != "" {
		return write
	}
	digest := sha256.New()
	var encoded [16]byte
	var weight uint64
	for index, block := range builder.blocks {
		if block.barrier || (block.media && !builder.settings.Enabled) {
			break
		}
		before := weight
		weight += uint64(block.weight)
		count := int(min(weight, maxSimulatedWriteTokens)) / simulatedWriteBlock
		if count > len(write.prefixes) {
			previous := digest.Sum(nil)
			text, isText := builder.cacheWriteText[index]
			var runes []rune
			if isText {
				runes = []rune(text.text)
			}
			for len(write.prefixes) < count {
				binary.BigEndian.PutUint64(encoded[:8], block.hashHigh)
				binary.BigEndian.PutUint64(encoded[8:], block.hashLow)
				if isText {
					// Stop at the same estimated boundary when text is
					// later appended inside this fingerprint chunk.
					needed := (len(write.prefixes)+1)*simulatedWriteBlock - int(before)
					end := sort.Search(len(runes), func(i int) bool {
						return tokenkit.Estimate(model, string(runes[:i+1])) >= needed
					})
					copy(encoded[:], simulatedModelCacheBlockHash(text.domain, []byte(string(runes[:min(end+1, len(runes))]))))
				}
				prefix := sha256.Sum256(append(previous, encoded[:]...))
				write.prefixes = append(write.prefixes, hex.EncodeToString(prefix[:]))
			}
		}
		binary.BigEndian.PutUint64(encoded[:8], block.hashHigh)
		binary.BigEndian.PutUint64(encoded[8:], block.hashLow)
		_, _ = digest.Write(encoded[:])
		if weight >= maxSimulatedWriteTokens {
			break
		}
	}
	return write
}

func (w *SimulatedCacheWrite) TTL() string {
	if w.ttl == 3600 {
		return "1h"
	}
	return "5m"
}

func (w *SimulatedCacheWrite) Claim(ctx context.Context, info *relaycommon.RelayInfo, input NormalizedInputTokens) (string, error) {
	count := min(len(w.prefixes), max(input.TotalInputTokens, 0)/simulatedWriteBlock)
	if count == 0 {
		return "no_complete_prefix_block", nil
	}
	if !common.RedisEnabled || common.RDB == nil {
		return "redis_unavailable", nil
	}
	scope := sha256Hex([]byte(info.OriginModelName + "\x00" + info.UpstreamModelName))
	w.keys = make([]string, count)
	for i, prefix := range w.prefixes[:count] {
		w.keys[i] = fmt.Sprintf("simulated-cache-write:{%d:%d:%s}:%d:%s", info.ChannelId, info.UserId, scope, i, prefix)
	}
	w.owner = "p:" + common.NewRequestId()
	upstream := min(max(input.CacheReadInputTokens, 0), maxSimulatedWriteTokens)
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	values, err := claimSimulatedCacheWrite.Run(ctx, common.RDB, w.keys, w.ttl, w.owner,
		upstream/simulatedWriteBlock, (upstream+simulatedWriteBlock-1)/simulatedWriteBlock).Int64Slice()
	if err != nil {
		return "redis_error", err
	}
	if len(values) != 3 {
		return "redis_error", fmt.Errorf("invalid cache write claim")
	}
	if values[0] == 0 {
		return "write_in_progress", nil
	}
	w.ttl, w.Read, w.Write = int(values[0]), int(values[1])*simulatedWriteBlock, int(values[2])*simulatedWriteBlock
	return "", nil
}

func (w *SimulatedCacheWrite) Confirm(ctx context.Context, settled, release bool) error {
	if w.Write == 0 || (!settled && !release) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	ttl := ""
	if settled {
		ttl = strconv.Itoa(w.ttl)
	}
	return confirmSimulatedCacheWrite.Run(ctx, common.RDB, w.keys, w.owner, ttl).Err()
}

func (w *SimulatedCacheWrite) SetTTL(seconds int) {
	w.ttl = seconds
}

func SimulatedCacheWriteBaseUsage(info *relaycommon.RelayInfo, usage *dto.Usage) *dto.Usage {
	return effectiveBillingUsage(prepareKimiK3OfficialBilling(info, usage))
}

// SimulatedCacheWriteUsage moves measured input into the cache categories
// while keeping the same total for billing and protocol conversion.
func SimulatedCacheWriteUsage(source *dto.Usage, w *SimulatedCacheWrite, countHits bool) *dto.Usage {
	input := NormalizeUsageForBilling(source).InputTokens
	adjusted := NormalizeUsageForSemantic(source, UsageSemanticOpenAI)
	adjusted.BillingUsage = nil
	adjusted.PromptTokens, adjusted.InputTokens = input.TotalInputTokens, input.TotalInputTokens
	adjusted.TotalTokens = saturatingTokenAdd(input.TotalInputTokens, adjusted.CompletionTokens)
	details := &adjusted.PromptTokensDetails
	details.CachedTokens = input.CacheReadInputTokens
	if countHits {
		details.CachedTokens = max(details.CachedTokens, min(w.Read, input.TotalInputTokens-input.CacheCreationInputTokens-w.Write))
	}
	details.CacheWriteTokens = input.CacheCreationInputTokens + w.Write
	details.CachedCreationTokens = details.CacheWriteTokens
	adjusted.ClaudeCacheCreation5mTokens = input.CacheCreation5mInputTokens
	adjusted.ClaudeCacheCreation1hTokens = input.CacheCreation1hInputTokens
	if w.ttl == 3600 {
		adjusted.ClaudeCacheCreation1hTokens += w.Write
	} else {
		adjusted.ClaudeCacheCreation5mTokens += w.Write
	}
	inputDetails := *details
	adjusted.InputTokensDetails = &inputDetails
	return &adjusted
}
