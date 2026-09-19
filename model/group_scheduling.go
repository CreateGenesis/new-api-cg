package model

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-redis/redis/v8"
)

// Probe state is deliberately separate from Channel.Status: one failed model
// must not disable other models or groups served by the same channel.
type GroupProbeState struct {
	Model            string `json:"model"`
	Healthy          bool   `json:"healthy"`
	TTFTMilliseconds int64  `json:"ttft_ms"`
	CheckedAt        int64  `json:"checked_at"`
	NextAt           int64  `json:"next_at"`
	Failures         int    `json:"failures"`
	ExpiresAt        int64  `json:"expires_at"`
}

var groupProbeLocal = struct {
	sync.Mutex
	states  map[string]GroupProbeState
	running map[string]bool
}{states: map[string]GroupProbeState{}, running: map[string]bool{}}
var GroupProbeWake = make(chan struct{}, 1)

func NotifyGroupProbeConfigChanged() {
	select {
	case GroupProbeWake <- struct{}{}:
	default:
	}
}

func GroupProbeKey(channel *Channel, modelName string) string {
	// Only a digest is stored. Keys and connection configuration never enter logs.
	settings := channel.GetOtherSettings()
	interval, timeout := settings.GroupScheduling.Interval(), settings.GroupScheduling.Timeout()
	settings.GroupScheduling = nil
	data, _ := common.Marshal([]any{channel.Type, channel.Key, channel.GetBaseURL(), channel.ModelMapping, channel.Setting, channel.ParamOverride, channel.HeaderOverride, channel.Other, settings, channel.OpenAIOrganization, modelName, interval, timeout})
	return fmt.Sprintf("new-api:group-probe:v1:{%d}:%x", channel.Id, sha256.Sum256(data))
}

func GroupProbeFresh(state GroupProbeState, now time.Time) bool {
	return state.Healthy && state.CheckedAt > 0 && state.ExpiresAt > now.UnixMilli() && state.TTFTMilliseconds >= 0
}

func GroupProbeStates(channels []*Channel, modelName string) map[int]GroupProbeState {
	states := make(map[int]GroupProbeState, len(channels))
	if len(channels) == 0 {
		return states
	}
	keys := make([]string, len(channels))
	for i, channel := range channels {
		keys[i] = GroupProbeKey(channel, modelName)
	}
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		values, err := common.RDB.MGet(ctx, keys...).Result()
		if err != nil {
			return states
		} // Shared health is authoritative; fail closed.
		for i, value := range values {
			if text, ok := value.(string); ok {
				var state GroupProbeState
				if common.UnmarshalJsonStr(text, &state) == nil {
					states[channels[i].Id] = state
				}
			}
		}
		return states
	}
	groupProbeLocal.Lock()
	defer groupProbeLocal.Unlock()
	for i, key := range keys {
		if state, ok := groupProbeLocal.states[key]; ok {
			states[channels[i].Id] = state
		}
	}
	return states
}

func GroupSchedulingApplies(channel *Channel, group, modelName string) bool {
	return channel != nil && channel.GetOtherSettings().GroupScheduling.Includes(group, modelName)
}

func GroupSchedulingAvailable(channel *Channel, group, modelName string) bool {
	if !GroupSchedulingApplies(channel, group, modelName) {
		return true
	}
	states := GroupProbeStates([]*Channel{channel}, modelName)
	if GroupProbeFresh(states[channel.Id], time.Now()) {
		return true
	}
	return false
}

// GroupSchedulingCandidates captures immutable channel pointers before any
// network access. It follows the existing exact-model / normalized fallback.
func GroupSchedulingCandidates(group, modelName, requestPath string, estimates *kitdto.InputTokenEstimates, excluded map[int]struct{}, filters []dto.ChannelFilter) ([]*Channel, error) {
	if requestPath != "" {
		filters = append(slices.Clone(filters), dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: requestPath})
	}
	names := []string{modelName}
	if normalized := ratio_setting.RoutingMatchModelName(modelName); normalized != modelName {
		names = append(names, normalized)
	}
	for _, name := range names {
		var candidates []*Channel
		if common.MemoryCacheEnabled {
			channelSyncLock.RLock()
			ids, _ := filterCandidateIDs(group2model2channels[group][name], modelName, filters)
			ids = filterChannelsByInputTokens(ids, estimates)
			ids = filterExcludedChannelIDs(ids, excluded)
			for _, id := range ids {
				if channel := channelsIDM[id]; channel != nil {
					candidates = append(candidates, channel)
				}
			}
			channelSyncLock.RUnlock()
		} else {
			var abilities []Ability
			if err := DB.Where(commonGroupCol+" = ? AND model = ? AND enabled = ?", group, name, true).Find(&abilities).Error; err != nil {
				return nil, err
			}
			abilities = filterAbilitiesByConstraints(abilities, modelName, filters)
			abilities = filterAbilitiesByInputTokens(abilities, estimates)
			ids := make([]int, 0, len(abilities))
			for _, ability := range abilities {
				if _, skip := excluded[ability.ChannelId]; !skip {
					ids = append(ids, ability.ChannelId)
				}
			}
			if len(ids) > 0 {
				if err := DB.Where("id IN ? AND status = ?", ids, common.ChannelStatusEnabled).Find(&candidates).Error; err != nil {
					return nil, err
				}
			}
		}
		if len(candidates) > 0 {
			return candidates, nil
		}
	}
	return nil, nil
}

func GroupProbeBackoff(failures int, interval time.Duration) time.Duration {
	if failures > 10 {
		return interval
	}
	return min(100*time.Millisecond*time.Duration(1<<min(max(failures-1, 0), 9)), interval)
}

var acquireGroupProbe = redis.NewScript(`
local state = redis.call('GET', KEYS[1])
if state then
 local ok, data = pcall(cjson.decode, state)
 if ok and tonumber(data.next_at or 0) > tonumber(ARGV[1]) then return -tonumber(data.next_at) end
end
if redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3], 'NX') then return 1 end
return 0
`)
var finishGroupProbe = redis.NewScript(`
if redis.call('GET', KEYS[2]) ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
redis.call('DEL', KEYS[2])
return 1
`)

// TryGroupProbe reserves a single channel/model probe across local workers and
// Redis-connected instances. Complete must be called exactly once when claimed.
func TryGroupProbe(channel *Channel, modelName string, now time.Time) (string, bool) {
	key := GroupProbeKey(channel, modelName)
	groupProbeLocal.Lock()
	if groupProbeLocal.running[key] {
		groupProbeLocal.Unlock()
		return "", false
	}
	if groupProbeLocal.states[key].NextAt > now.UnixMilli() {
		groupProbeLocal.Unlock()
		return "", false
	}
	groupProbeLocal.running[key] = true
	groupProbeLocal.Unlock()
	owner := common.GetUUID()
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		acquired, err := acquireGroupProbe.Run(ctx, common.RDB, []string{key, key + ":lease"}, now.UnixMilli(), owner, (channel.GetOtherSettings().GroupScheduling.Timeout()+5)*1000).Int64()
		cancel()
		if err != nil || acquired != 1 {
			groupProbeLocal.Lock()
			delete(groupProbeLocal.running, key)
			if err == nil && acquired < 0 {
				state := groupProbeLocal.states[key]
				state.NextAt = -acquired
				state.ExpiresAt = -acquired
				groupProbeLocal.states[key] = state
			}
			groupProbeLocal.Unlock()
			return "", false
		}
	}
	return owner, true
}

func CompleteGroupProbe(channel *Channel, modelName, owner string, ttft time.Duration, success bool, now time.Time) {
	key := GroupProbeKey(channel, modelName)
	previous := GroupProbeStates([]*Channel{channel}, modelName)[channel.Id]
	settings := channel.GetOtherSettings().GroupScheduling
	interval := time.Duration(settings.Interval()) * time.Second
	timeout := time.Duration(settings.Timeout()) * time.Second
	state := GroupProbeState{Model: modelName, Healthy: success, TTFTMilliseconds: ttft.Milliseconds(), CheckedAt: now.UnixMilli(), ExpiresAt: now.Add(max(3*interval, interval+timeout)).UnixMilli()}
	delay := interval
	if !success {
		state.Failures = min(previous.Failures+1, 2147483647)
		delay = GroupProbeBackoff(state.Failures, interval)
	}
	state.NextAt = now.Add(delay).UnixMilli()
	if common.RedisEnabled && common.RDB != nil {
		encoded, err := common.Marshal(state)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			err = finishGroupProbe.Run(ctx, common.RDB, []string{key, key + ":lease"}, owner, string(encoded), max(3*interval, interval+timeout).Milliseconds()+60000).Err()
			cancel()
		}
		if err != nil {
			common.SysError("failed to publish group scheduling probe state")
		}
	}
	groupProbeLocal.Lock()
	groupProbeLocal.states[key] = state
	delete(groupProbeLocal.running, key)
	for oldKey, old := range groupProbeLocal.states {
		if old.ExpiresAt+60000 < now.UnixMilli() && !groupProbeLocal.running[oldKey] {
			delete(groupProbeLocal.states, oldKey)
		}
	}
	groupProbeLocal.Unlock()
}
