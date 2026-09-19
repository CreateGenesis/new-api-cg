package service

import (
	"maps"
	"math"
	"math/rand"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type GroupSchedulingDecision struct {
	Group            string                     `json:"group"`
	Model            string                     `json:"model"`
	InitialChannelID int                        `json:"initial_channel_id"`
	ChannelID        int                        `json:"channel_id"`
	Priority         int64                      `json:"priority"`
	ToleranceMS      int64                      `json:"tolerance_ms"`
	Candidates       []GroupSchedulingCandidate `json:"candidates"`
}
type GroupSchedulingCandidate struct {
	ChannelID int     `json:"channel_id"`
	TTFTMS    int64   `json:"ttft_ms"`
	Cost      float64 `json:"cost"`
}

func selectGroupScheduledChannel(param *ChannelSelectParam, group string, filters []dto.ChannelFilter) (*model.Channel, error) {
	if param.Ctx != nil {
		param.Ctx.Set("group_scheduling_decision", nil)
	}
	var anchor *model.Channel
	var err error
	revalidating := param.SchedulingAnchor != nil && param.SchedulingAnchorGroup == group
	if revalidating {
		anchor = param.SchedulingAnchor
	} else {
		anchor, err = model.GetRandomSatisfiedChannelExcludingPriority(group, param.ModelName, 0, param.RequestPath, param.InputTokenEstimates, param.ExcludedChannelIDs, param.MaxPriority, filters...)
	}
	if err != nil || anchor == nil || !model.GroupSchedulingApplies(anchor, group, param.ModelName) {
		return anchor, err
	}
	candidates, err := model.GroupSchedulingCandidates(group, param.ModelName, param.RequestPath, param.InputTokenEstimates, param.ExcludedChannelIDs, filters)
	if err != nil {
		return nil, err
	}
	var participating []*model.Channel
	for _, candidate := range candidates {
		if model.GroupSchedulingApplies(candidate, group, param.ModelName) {
			participating = append(participating, candidate)
		}
	}
	if len(participating) == 0 {
		return model.GetRandomSatisfiedChannelExcludingPriority(group, param.ModelName, 0, param.RequestPath, param.InputTokenEstimates, param.ExcludedChannelIDs, param.MaxPriority, filters...)
	}
	states := model.GroupProbeStates(participating, param.ModelName)
	excluded := make(map[int]struct{}, len(param.ExcludedChannelIDs))
	maps.Copy(excluded, param.ExcludedChannelIDs)
	now := time.Now()
	healthy := make([]*model.Channel, 0, len(participating))
	for _, candidate := range participating {
		if !model.GroupProbeFresh(states[candidate.Id], now) || !ChannelAcceptsRequestMode(candidate, param.ClientRequestMode) {
			excluded[candidate.Id] = struct{}{}
			if !ChannelAcceptsRequestMode(candidate, param.ClientRequestMode) {
				param.RequestModeFiltered = true
			}
		} else {
			healthy = append(healthy, candidate)
		}
	}
	if _, unavailable := excluded[anchor.Id]; unavailable && (!revalidating || len(healthy) == 0) {
		anchor, err = model.GetRandomSatisfiedChannelExcludingPriority(group, param.ModelName, 0, param.RequestPath, param.InputTokenEstimates, excluded, param.MaxPriority, filters...)
	}
	if err != nil || anchor == nil || !model.GroupSchedulingApplies(anchor, group, param.ModelName) {
		return anchor, err
	}
	if len(healthy) == 0 {
		return nil, nil
	}
	tolerance := operation_setting.GroupSchedulingTolerance(group)
	fastest := int64(math.MaxInt64)
	for _, candidate := range healthy {
		fastest = min(fastest, states[candidate.Id].TTFTMilliseconds)
	}
	bestCost := math.Inf(1)
	bestTime := int64(math.MaxInt64)
	var finalists []*model.Channel
	priority := anchor.GetPriority()
	if anchor.SchedulingPriority != nil {
		priority = *anchor.SchedulingPriority
	}
	decision := GroupSchedulingDecision{Group: group, Model: param.ModelName, InitialChannelID: anchor.Id, Priority: priority, ToleranceMS: tolerance}
	for _, candidate := range healthy {
		latency := states[candidate.Id].TTFTMilliseconds
		cost := candidate.GetOtherSettings().GroupScheduling.Cost()
		decision.Candidates = append(decision.Candidates, GroupSchedulingCandidate{ChannelID: candidate.Id, TTFTMS: latency, Cost: cost})
		if latency-fastest > tolerance {
			continue
		}
		if cost < bestCost || cost == bestCost && latency < bestTime {
			bestCost = cost
			bestTime = latency
			finalists = nil
		}
		if cost == bestCost && latency == bestTime {
			finalists = append(finalists, candidate)
		}
	}
	// Equal cost and latency preserve weighted selection. All-zero means uniform.
	total := float64(0)
	for _, candidate := range finalists {
		total += float64(candidate.GetWeight())
	}
	chosen := finalists[rand.Intn(len(finalists))]
	if total > 0 {
		draw := rand.Float64() * total
		for _, candidate := range finalists {
			draw -= float64(candidate.GetWeight())
			if draw < 0 {
				chosen = candidate
				break
			}
		}
	}
	selected := *chosen
	selected.SchedulingPriority = common.GetPointer(priority)
	decision.ChannelID = selected.Id
	if param.Ctx != nil {
		param.Ctx.Set("group_scheduling_decision", decision)
	}
	return &selected, nil
}
