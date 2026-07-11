package service

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

type InterChannelRetryState struct {
	retries int
}

func (s *InterChannelRetryState) Count() int {
	return s.retries
}

func (s *InterChannelRetryState) Increase() {
	s.retries++
}

type SameChannelRetryState struct {
	retries int
}

func (s *SameChannelRetryState) Count() int {
	return s.retries
}

func (s *SameChannelRetryState) Increase() {
	s.retries++
}

type ChannelSelectParam struct {
	Ctx                  *gin.Context
	TokenGroup           string
	ModelName            string
	RequestPath          string
	EstimatedInputTokens *int
	ExcludedChannelIDs   map[int]struct{}
	LastAttemptedChannel int
	DeferredChannelID    int
	AutoGroupIndex       int
	AutoGroupSelected    bool
}

func (p *ChannelSelectParam) ExcludeAttemptedChannel(channelID int) {
	p.excludeChannel(channelID)
	if channelID > 0 {
		p.LastAttemptedChannel = channelID
	}
}

func (p *ChannelSelectParam) ExcludeUnavailableChannel(channelID int) {
	p.excludeChannel(channelID)
}

func (p *ChannelSelectParam) excludeChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	if p.ExcludedChannelIDs == nil {
		p.ExcludedChannelIDs = make(map[int]struct{})
	}
	p.ExcludedChannelIDs[channelID] = struct{}{}
}

func (p *ChannelSelectParam) BeginNextSelectionSweep() bool {
	if len(p.ExcludedChannelIDs) == 0 {
		return false
	}
	p.ExcludedChannelIDs = make(map[int]struct{})
	p.DeferredChannelID = 0
	if p.LastAttemptedChannel > 0 {
		p.ExcludedChannelIDs[p.LastAttemptedChannel] = struct{}{}
		p.DeferredChannelID = p.LastAttemptedChannel
	}
	if p.TokenGroup != "auto" || common.GetContextKeyBool(p.Ctx, constant.ContextKeyTokenCrossGroupRetry) {
		p.AutoGroupIndex = 0
		p.AutoGroupSelected = false
	}
	return true
}

func (p *ChannelSelectParam) AllowLastAttemptedChannel() {
	delete(p.ExcludedChannelIDs, p.LastAttemptedChannel)
	p.DeferredChannelID = 0
	if len(p.ExcludedChannelIDs) == 0 {
		p.ExcludedChannelIDs = nil
	}
	if p.TokenGroup != "auto" || common.GetContextKeyBool(p.Ctx, constant.ContextKeyTokenCrossGroupRetry) {
		p.AutoGroupIndex = 0
		p.AutoGroupSelected = false
	}
}

func (p *ChannelSelectParam) restoreDeferredChannelAfterSelection() {
	if p.DeferredChannelID <= 0 {
		return
	}
	delete(p.ExcludedChannelIDs, p.DeferredChannelID)
	p.DeferredChannelID = 0
	if len(p.ExcludedChannelIDs) == 0 {
		p.ExcludedChannelIDs = nil
	}
}

// CacheGetRandomSatisfiedChannel tries to get a random channel that satisfies the requirements.
// 尝试获取一个满足要求的随机渠道。
//
// For "auto" tokenGroup with cross-group Retry enabled:
// 对于启用了跨分组重试的 "auto" tokenGroup：
//
//   - Each group will exhaust all its priorities before moving to the next group.
//     每个分组会用完所有优先级后才会切换到下一个分组。
//
//   - Uses ContextKeyAutoGroupIndex to track current group index.
//     使用 ContextKeyAutoGroupIndex 跟踪当前分组索引。
//
//   - When GetRandomSatisfiedChannel returns nil (priorities exhausted), moves to next group.
//     当 GetRandomSatisfiedChannel 返回 nil（优先级用完）时，切换到下一个分组。
func CacheGetRandomSatisfiedChannel(param *ChannelSelectParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)

	if param.TokenGroup == "auto" {
		if len(setting.GetAutoGroups()) == 0 {
			return nil, selectGroup, errors.New("auto groups is not enabled")
		}
		autoGroups := GetUserAutoGroup(userGroup)
		if len(autoGroups) == 0 {
			return nil, selectGroup, errors.New("no auto groups available for current user")
		}

		startGroupIndex := param.AutoGroupIndex
		if startGroupIndex < 0 {
			startGroupIndex = 0
		}
		if startGroupIndex >= len(autoGroups) {
			return nil, selectGroup, nil
		}
		crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)
		endGroupIndex := len(autoGroups)
		if param.AutoGroupSelected && !crossGroupRetry {
			endGroupIndex = startGroupIndex + 1
		}

		for i := startGroupIndex; i < endGroupIndex; i++ {
			autoGroup := autoGroups[i]
			logger.LogDebug(param.Ctx, "Auto selecting group: %s", autoGroup)

			channel, err = model.GetRandomSatisfiedChannelExcluding(autoGroup, param.ModelName, 0, param.RequestPath, param.EstimatedInputTokens, param.ExcludedChannelIDs)
			if err != nil {
				return nil, autoGroup, err
			}
			if channel == nil {
				logger.LogDebug(param.Ctx, "No available channel in group %s for model %s, trying next group", autoGroup, param.ModelName)
				if param.AutoGroupSelected && !crossGroupRetry {
					return nil, autoGroup, nil
				}
				param.AutoGroupIndex = i + 1
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				continue
			}
			param.AutoGroupIndex = i
			param.AutoGroupSelected = true
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
			selectGroup = autoGroup
			logger.LogDebug(param.Ctx, "Auto selected group: %s", autoGroup)
			break
		}
	} else {
		channel, err = model.GetRandomSatisfiedChannelExcluding(param.TokenGroup, param.ModelName, 0, param.RequestPath, param.EstimatedInputTokens, param.ExcludedChannelIDs)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	if channel != nil {
		// The final channel from the previous sweep is deferred only for the
		// first pick of the new sweep. Restore it immediately afterwards so the
		// new sweep still exhausts every eligible peer before another reset.
		param.restoreDeferredChannelAfterSelection()
	}
	return channel, selectGroup, nil
}
