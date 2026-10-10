package service

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
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
	channelTypes, pluginKeys := pinnedTaskPluginIdentities(c, pluginKey)
	GetChannelConstraints(c).AddFilter(dto.ChannelFilter{
		Kind:                   dto.FilterTaskPluginIdentity,
		TaskPluginKey:          pluginKey,
		TaskPluginChannelTypes: channelTypes,
		TaskPluginKeys:         pluginKeys,
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

// RetryParam retains the official HTTP/WebSocket selection API.
type RetryParam = ChannelSelectParam

type ChannelSelectParam struct {
	Retry *int

	// Revalidation keeps the scheduling pool already chosen by distribution.
	SchedulingAnchor      *model.Channel
	SchedulingAnchorGroup string
	Ctx                   *gin.Context
	TokenGroup            string
	ModelName             string
	RequestPath           string
	InputTokenEstimates   *kitdto.InputTokenEstimates
	// PrepareCandidate runs outside model cache locks, before routing admission.
	PrepareCandidate    func(*model.Channel) (*kitdto.InputTokenEstimates, error)
	ExcludedChannelIDs  map[int]struct{}
	MaxPriority         *int64
	AutoGroupIndex      int
	AutoGroupSelected   bool
	ClientRequestMode   types.RequestMode
	RequestModeFiltered bool
}

func (p *ChannelSelectParam) RoutingEstimates() *kitdto.InputTokenEstimates {
	if p.PrepareCandidate != nil {
		return nil
	}
	return p.InputTokenEstimates
}

// CandidatePreparationError distinguishes local request preparation from selection failures.
type CandidatePreparationError struct {
	Err error
}

func (e *CandidatePreparationError) Error() string { return e.Err.Error() }
func (e *CandidatePreparationError) Unwrap() error { return e.Err }

func (p *ChannelSelectParam) CandidateAcceptsInput(channel *model.Channel) (bool, error) {
	estimates := p.InputTokenEstimates
	if p.PrepareCandidate != nil {
		var err error
		estimates, err = p.PrepareCandidate(channel)
		if err != nil {
			return false, &CandidatePreparationError{Err: err}
		}
	}
	return channel.MatchesInputTokenRouting(estimates), nil
}

// selectPreparedRandomChannel prepares only candidates at the active priority.
// No decode or image encoding happens while a model cache lock is held.
func (p *ChannelSelectParam) selectPreparedRandomChannel(group string, excluded map[int]struct{}, filters []dto.ChannelFilter) (*model.Channel, error) {
	for {
		channel, err := model.GetRandomSatisfiedChannelExcludingPriority(group, p.ModelName, 0, p.RequestPath, p.RoutingEstimates(), excluded, p.MaxPriority, filters...)
		if err != nil || channel == nil || p.PrepareCandidate == nil {
			return channel, err
		}
		if !ChannelAcceptsRequestMode(channel, p.ClientRequestMode) {
			p.RequestModeFiltered = true
		} else {
			accepted, err := p.CandidateAcceptsInput(channel)
			if err != nil {
				return nil, err
			}
			if accepted {
				return channel, nil
			}
		}
		if excluded == nil {
			excluded = make(map[int]struct{})
		}
		excluded[channel.Id] = struct{}{}
	}
}

func (p *ChannelSelectParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}
func (p *ChannelSelectParam) SetRetry(retry int) { p.Retry = &retry }
func (p *ChannelSelectParam) IncreaseRetry()     { p.SetRetry(p.GetRetry() + 1) }

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
	if channel.SchedulingPriority != nil {
		priority = *channel.SchedulingPriority
	} else if p.Ctx != nil {
		if value, ok := p.Ctx.Get("group_scheduling_decision"); ok {
			if decision, ok := value.(GroupSchedulingDecision); ok && decision.ChannelID == channel.Id {
				priority = decision.Priority
			}
		}
	}
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

			channel, err = selectGroupScheduledChannel(param, autoGroup, filters)
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
		channel, err = selectGroupScheduledChannel(param, param.TokenGroup, filters)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}

func pinnedTaskPluginIdentities(c *gin.Context, expected string) ([]int, []string) {
	if c == nil || expected == "" {
		return nil, nil
	}
	if value, exists := c.Get(jsplugin.ContextKeyPinnedEndpoint); exists {
		pinned, ok := value.(jsplugin.PinnedEndpoint)
		if ok && pinned.Generation != nil && len(pinned.Candidates) > 1 {
			expectedFound := false
			channelTypes := make([]int, 0, len(pinned.Candidates))
			pluginKeys := make([]string, 0, len(pinned.Candidates))
			seen := make(map[int]struct{}, len(pinned.Candidates))
			for _, candidate := range pinned.Candidates {
				if candidate.Plugin == nil {
					continue
				}
				if candidate.Plugin.Meta.Key == expected {
					expectedFound = true
				}
				pluginKeys = append(pluginKeys, candidate.Plugin.Meta.Key)
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
				return channelTypes, pluginKeys
			}
		}
	}
	value, exists := c.Get(jsplugin.ContextKeyPinnedPlugin)
	pinned, ok := value.(jsplugin.PinnedPlugin)
	if !exists || !ok || pinned.Generation == nil || pinned.Plugin == nil || pinned.Plugin.Meta.Key != expected {
		return nil, nil
	}
	channelTypes := make([]int, 0, len(pinned.Plugin.Meta.ChannelTypes))
	for _, channelType := range pinned.Plugin.Meta.ChannelTypes {
		if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
			continue
		}
		channelTypes = append(channelTypes, channelType)
	}
	return channelTypes, []string{expected}
}

// ChannelSelectError explains why SelectChannelForRequest found no channel.
// Callers render it for their transport: the HTTP distributor localizes
// MessageID with its own helpers and the Responses WebSocket relay wraps it in
// a NewAPIError. Message is set instead of MessageID when the text is a fixed
// error code that clients match on.
type ChannelSelectError struct {
	Cause      error
	StatusCode int
	Code       types.ErrorCode
	MessageID  string
	Params     map[string]any
	Message    string
	// FilterKind and Channel identify a candidate rejected by request filters.
	FilterKind dto.ChannelFilterKind
	Channel    *model.Channel
	// NoAvailableChannel marks the "no channel for this group and model"
	// outcome so the distributor can name the claiming task plugin.
	NoAvailableChannel bool
}

// SelectChannelForRequest resolves the channel for one attempt with the rules
// shared by the HTTP distributor and the Responses WebSocket relay: a pinned
// channel wins, then session affinity (first attempt only), then a random
// eligible channel; every candidate must satisfy the request's channel
// filters. The group the channel was chosen from is returned for auto-group
// callers. The caller still applies SetupContextForSelectedChannel.
func SelectChannelForRequest(c *gin.Context, modelName string, retry *RetryParam) (*model.Channel, string, *ChannelSelectError) {
	constraints := GetChannelConstraints(c)
	if pin, found, overridden := constraints.ResolvedPin(); found {
		for _, lost := range overridden {
			logger.LogWarn(c, common.LogText(
				"channel pin overridden: winning_source=%s winning_channel_id=%d overridden_source=%s overridden_channel_id=%d",
				pin.Source, pin.ChannelId, lost.Source, lost.ChannelId,
			))
		}
		channel, err := model.CacheGetChannel(pin.ChannelId)
		if err != nil {
			return nil, "", pinnedChannelUnavailable(pin, http.StatusBadRequest, i18n.MsgDistributorInvalidChannelId)
		}
		if channel.Status != common.ChannelStatusEnabled {
			return nil, "", pinnedChannelUnavailable(pin, http.StatusForbidden, i18n.MsgDistributorChannelDisabled)
		}
		if ok, kind := model.ChannelSatisfiesFilters(channel, modelName, constraints.Filters); !ok {
			return nil, "", &ChannelSelectError{
				StatusCode: http.StatusBadRequest, Code: types.ErrorCode(kind), MessageID: i18n.MsgDistributorNoAvailableChannel,
				Params:     map[string]any{"Group": common.GetContextKeyString(c, constant.ContextKeyUsingGroup), "Model": modelName},
				FilterKind: kind, Channel: channel,
			}
		}
		return channel, "", nil
	}

	usingGroup := retry.TokenGroup
	var channel *model.Channel
	var selectGroup string
	if retry.GetRetry() == 0 {
		if preferredChannelID, found := GetPreferredChannelByAffinity(c, modelName, usingGroup); found {
			affinityUsable := false
			preferred, err := model.CacheGetChannel(preferredChannelID)
			affinitySatisfied := false
			if _, excluded := retry.ExcludedChannelIDs[preferredChannelID]; !excluded && err == nil && preferred != nil &&
				preferred.Status == common.ChannelStatusEnabled && ChannelAcceptsRequestMode(preferred, retry.ClientRequestMode) &&
				(retry.PrepareCandidate != nil || preferred.MatchesInputTokenRouting(retry.InputTokenEstimates)) {
				filters := constraints.Filters
				if retry.RequestPath != "" {
					filters = append(append([]dto.ChannelFilter{}, filters...), dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: retry.RequestPath})
				}
				affinitySatisfied, _ = model.ChannelSatisfiesFilters(preferred, modelName, filters)
			}
			if affinitySatisfied && retry.PrepareCandidate != nil {
				accepted, prepareErr := retry.CandidateAcceptsInput(preferred)
				if prepareErr != nil {
					return nil, "", &ChannelSelectError{StatusCode: http.StatusServiceUnavailable, Message: prepareErr.Error(), Cause: prepareErr}
				}
				affinitySatisfied = accepted
			}
			if affinitySatisfied {
				if usingGroup == "auto" {
					userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
					for index, g := range GetRequestAutoGroups(c, userGroup) {
						if !model.GroupSchedulingApplies(preferred, g, modelName) && model.IsChannelEnabledForGroupModel(g, modelName, preferred.Id) {
							selectGroup = g
							common.SetContextKey(c, constant.ContextKeyAutoGroup, g)
							common.SetContextKey(c, constant.ContextKeyAutoGroupIndex, index)
							retry.AutoGroupIndex = index
							retry.AutoGroupSelected = true
							channel = preferred
							affinityUsable = true
							MarkChannelAffinityCandidate(c, preferred.Id)
							break
						}
					}
				} else if !model.GroupSchedulingApplies(preferred, usingGroup, modelName) && model.IsChannelEnabledForGroupModel(usingGroup, modelName, preferred.Id) {
					channel = preferred
					selectGroup = usingGroup
					affinityUsable = true
					MarkChannelAffinityCandidate(c, preferred.Id)
				}
			}
			if !affinityUsable && !ShouldKeepChannelAffinityOnChannelDisabled() {
				ClearCurrentChannelAffinityCache(c)
			}
			if !affinityUsable && RequestPolicy(c).SessionMode == "strict" {
				return nil, "", &ChannelSelectError{StatusCode: http.StatusServiceUnavailable, Message: "strict_session_binding_unavailable"}
			}
		}
	}

	if channel == nil {
		var err error
		channel, selectGroup, err = CacheGetRandomSatisfiedChannel(retry)
		if err != nil {
			showGroup := usingGroup
			if usingGroup == "auto" {
				showGroup = fmt.Sprintf("auto(%s)", selectGroup)
			}
			return nil, selectGroup, &ChannelSelectError{
				Cause: err, StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeModelNotFound, MessageID: i18n.MsgDistributorGetChannelFailed,
				Params: map[string]any{"Group": showGroup, "Model": modelName, "Error": err.Error()},
			}
		}
		if channel == nil {
			return nil, selectGroup, &ChannelSelectError{
				StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeModelNotFound, MessageID: i18n.MsgDistributorNoAvailableChannel,
				Params: map[string]any{"Group": usingGroup, "Model": modelName}, NoAvailableChannel: true,
			}
		}
	}
	if ok, kind := model.ChannelSatisfiesFilters(channel, modelName, constraints.Filters); !ok {
		return nil, selectGroup, &ChannelSelectError{
			StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeModelNotFound, MessageID: i18n.MsgDistributorNoAvailableChannel,
			Params:     map[string]any{"Group": common.GetContextKeyString(c, constant.ContextKeyUsingGroup), "Model": modelName},
			FilterKind: kind, Channel: channel, NoAvailableChannel: true,
		}
	}
	return channel, selectGroup, nil
}

// Origin-task pins report a fixed code so task polling can tell a retired
// channel from a malformed request.
func pinnedChannelUnavailable(pin dto.ChannelPin, statusCode int, messageID string) *ChannelSelectError {
	if pin.Source == dto.PinSourceOriginTask {
		return &ChannelSelectError{StatusCode: http.StatusBadRequest, Code: "origin_task_channel_disabled", Message: "origin_task_channel_disabled"}
	}
	return &ChannelSelectError{StatusCode: statusCode, MessageID: messageID}
}

// AppendUsedChannel records an attempted channel in the request's channel
// trail, which the retry log and the consume log's admin_info both read.
func AppendUsedChannel(c *gin.Context, channelID int) {
	c.Set("use_channel", append(c.GetStringSlice("use_channel"), fmt.Sprintf("%d", channelID)))
}
