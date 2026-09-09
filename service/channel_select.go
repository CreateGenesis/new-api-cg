package service

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func GetChannelConstraints(c *gin.Context) *dto.ChannelConstraints {
	if c == nil {
		return &dto.ChannelConstraints{}
	}
	if existing, ok := common.GetContextKeyType[*dto.ChannelConstraints](c, constant.ContextKeyChannelConstraints); ok && existing != nil {
		return existing
	}
	constraints := &dto.ChannelConstraints{}
	common.SetContextKey(c, constant.ContextKeyChannelConstraints, constraints)
	return constraints
}

func AppendTaskPluginIdentityFilter(c *gin.Context, pluginKey string) {
	if c == nil {
		return
	}
	GetChannelConstraints(c).AddFilter(dto.ChannelFilter{
		Kind:                   dto.FilterTaskPluginIdentity,
		TaskPluginKey:          pluginKey,
		TaskPluginChannelTypes: pinnedTaskPluginChannelTypes(c, pluginKey),
	})
}

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
	Ctx                 *gin.Context
	TokenGroup          string
	ModelName           string
	RequestPath         string
	InputTokenEstimates *kitdto.InputTokenEstimates
	ExcludedChannelIDs  map[int]struct{}
	MaxPriority         *int64
	AutoGroupIndex      int
	AutoGroupSelected   bool
	ClientRequestMode   types.RequestMode
	RequestModeFiltered bool
}

func ChannelAcceptsRequestMode(channel *model.Channel, requestMode types.RequestMode) bool {
	if channel == nil {
		return false
	}
	settings := channel.GetOtherSettings()
	switch requestMode {
	case types.RequestModeStream:
		return !settings.DisableStream
	case types.RequestModeNonStream:
		return !settings.DisableNonStream
	default:
		return true
	}
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
	for {
		channel, selectGroup, err := cacheGetRandomSatisfiedChannel(param)
		if err != nil || channel == nil || ChannelAcceptsRequestMode(channel, param.ClientRequestMode) {
			return channel, selectGroup, err
		}
		param.RequestModeFiltered = true
		param.ExcludeUnavailableChannel(channel)
		logger.LogInfo(param.Ctx, fmt.Sprintf("skipping channel %d because %s requests are disabled", channel.Id, param.ClientRequestMode.String()))
	}
}

func cacheGetRandomSatisfiedChannel(param *ChannelSelectParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
	filters := GetChannelConstraints(param.Ctx).Filters

	if param.TokenGroup == "auto" {
		autoGroups := GetRequestAutoGroups(param.Ctx, userGroup)
		if len(autoGroups) == 0 {
			return nil, selectGroup, errors.New("auto groups is not enabled")
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

			channel, err = model.GetRandomSatisfiedChannelExcludingPriority(autoGroup, param.ModelName, 0, param.RequestPath, param.InputTokenEstimates, param.ExcludedChannelIDs, param.MaxPriority, filters...)
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
		channel, err = model.GetRandomSatisfiedChannelExcludingPriority(param.TokenGroup, param.ModelName, 0, param.RequestPath, param.InputTokenEstimates, param.ExcludedChannelIDs, param.MaxPriority, filters...)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}

func pinnedTaskPluginChannelTypes(c *gin.Context, expected string) []int {
	if c == nil || expected == "" {
		return nil
	}
	if value, exists := c.Get(jsplugin.ContextKeyPinnedEndpoint); exists {
		pinned, ok := value.(jsplugin.PinnedEndpoint)
		if ok && pinned.Generation != nil && len(pinned.Candidates) > 1 {
			expectedFound := false
			channelTypes := make([]int, 0, len(pinned.Candidates))
			seen := make(map[int]struct{}, len(pinned.Candidates))
			for _, candidate := range pinned.Candidates {
				if candidate.Plugin == nil {
					continue
				}
				if candidate.Plugin.Meta.Key == expected {
					expectedFound = true
				}
				for _, channelType := range candidate.Plugin.Meta.ChannelTypes {
					if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
						continue
					}
					if _, duplicate := seen[channelType]; duplicate {
						continue
					}
					if plugin, indexed := pinned.Generation.GetByChannelType(channelType); indexed && plugin == candidate.Plugin {
						seen[channelType] = struct{}{}
						channelTypes = append(channelTypes, channelType)
					}
				}
			}
			if expectedFound {
				return channelTypes
			}
		}
	}
	value, exists := c.Get(jsplugin.ContextKeyPinnedPlugin)
	pinned, ok := value.(jsplugin.PinnedPlugin)
	if !exists || !ok || pinned.Generation == nil || pinned.Plugin == nil || pinned.Plugin.Meta.Key != expected {
		return nil
	}
	channelTypes := make([]int, 0, len(pinned.Plugin.Meta.ChannelTypes))
	for _, channelType := range pinned.Plugin.Meta.ChannelTypes {
		if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
			continue
		}
		channelTypes = append(channelTypes, channelType)
	}
	if len(channelTypes) == 0 {
		return nil
	}
	return channelTypes
}
