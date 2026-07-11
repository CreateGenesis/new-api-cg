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
	MaxPriority          *int64
	AutoGroupIndex       int
	AutoGroupSelected    bool
}

func (p *ChannelSelectParam) ExcludeAttemptedChannel(channel *model.Channel) {
	p.excludeSelectedChannel(channel)
}

func (p *ChannelSelectParam) ExcludeUnavailableChannel(channel *model.Channel) {
	p.excludeSelectedChannel(channel)
}

func (p *ChannelSelectParam) excludeSelectedChannel(channel *model.Channel) {
	if channel == nil || channel.Id <= 0 {
		return
	}
	if p.ExcludedChannelIDs == nil {
		p.ExcludedChannelIDs = make(map[int]struct{})
	}
	p.ExcludedChannelIDs[channel.Id] = struct{}{}
	priority := channel.GetPriority()
	if p.MaxPriority == nil || priority < *p.MaxPriority {
		p.MaxPriority = common.GetPointer(priority)
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

			channel, err = model.GetRandomSatisfiedChannelExcludingPriority(autoGroup, param.ModelName, 0, param.RequestPath, param.EstimatedInputTokens, param.ExcludedChannelIDs, param.MaxPriority)
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
		channel, err = model.GetRandomSatisfiedChannelExcludingPriority(param.TokenGroup, param.ModelName, 0, param.RequestPath, param.EstimatedInputTokens, param.ExcludedChannelIDs, param.MaxPriority)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}
