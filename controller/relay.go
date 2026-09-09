package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const maxRelayErrorResponseLogBytes = 16 * 1024

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	case relayconstant.RelayModeAlphaSearch:
		err = relay.AlphaSearchHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {

	requestId := c.GetString(common.RequestIdKey)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		relayInfo   *relaycommon.RelayInfo
		ws          *websocket.Conn
	)

	defer func() {
		if newAPIError != nil {
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			if c.Writer.Written() && common.GetContextKeyBool(c, constant.ContextKeyIsStream) {
				return
			}
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				helper.WssError(c, ws, newAPIError.ToOpenAIError())
			case types.RelayFormatClaude:
				c.JSON(newAPIError.StatusCode, gin.H{
					"type":  "error",
					"error": newAPIError.ToClaudeError(),
				})
			default:
				c.JSON(newAPIError.StatusCode, gin.H{
					"error": newAPIError.ToOpenAIError(),
				})
			}
		}
	}()
	defer func() {
		recordFinalRelayError(c, newAPIError, relayInfo)
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry())
		}
		return
	}

	relayInfo, err = relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}
	if relayFormat != types.RelayFormatOpenAIRealtime {
		service.StartRelayDebug(c)
	}
	layeredRetryEnabled := controllerOwnsRelayOverloadLease(relayFormat, relayInfo.RelayMode)
	c.Set("layered_relay_retry", layeredRetryEnabled)
	service.SetChannelOverloadLeaseControllerOwned(c, layeredRetryEnabled)

	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	needCountToken := constant.CountToken
	needInputTokenRoutingEstimate := supportsInputTokenRouting(relayFormat, relayInfo.RelayMode)
	// Avoid building huge CombineText (strings.Join) when token counting and sensitive check are both disabled.
	var meta *types.TokenCountMeta
	if needSensitiveCheck || needCountToken || needInputTokenRoutingEstimate {
		meta = request.GetTokenCountMeta()
	} else {
		meta = fastTokenCountMetaForPricing(request)
	}

	if needSensitiveCheck && meta != nil {
		contains, words := service.CheckSensitiveText(meta.CombineText)
		if contains {
			logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))
			newAPIError = types.NewError(
				errors.New("sensitive words detected"),
				types.ErrorCodeSensitiveWordsDetected,
				types.ErrOptionWithStatusCode(http.StatusBadRequest),
				types.ErrOptionWithSkipRetry(),
			)
			return
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
		return
	}

	relayInfo.SetEstimatePromptTokens(tokens)

	priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		return
	}

	// common.SetContextKey(c, constant.ContextKeyTokenCountMeta, meta)

	if priceData.FreeModel {
		logger.LogInfo(c, fmt.Sprintf("模型 %s 免费，跳过预扣费", relayInfo.OriginModelName))
	} else {
		newAPIError = service.PreConsumeBilling(c, priceData.QuotaToPreConsume, relayInfo)
		if newAPIError != nil {
			return
		}
	}
	relayInfo.CaptureUpstreamAttemptBaseline()

	defer func() {
		// Only return quota if downstream failed and quota was actually pre-consumed
		if newAPIError != nil {
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil {
				relayInfo.Billing.Refund(c)
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
		}
	}()

	selectParam := &service.ChannelSelectParam{
		Ctx:                 c,
		TokenGroup:          relayInfo.TokenGroup,
		ModelName:           relayInfo.OriginModelName,
		RequestPath:         c.Request.URL.Path,
		InputTokenEstimates: inputTokenEstimatesForRouting(relayFormat, relayInfo.RelayMode, tokens, meta, relayInfo.OriginModelName),
		AutoGroupIndex:      common.GetContextKeyInt(c, constant.ContextKeyAutoGroupIndex),
		AutoGroupSelected:   relayInfo.TokenGroup == "auto" && common.GetContextKeyString(c, constant.ContextKeyAutoGroup) != "",
		ClientRequestMode:   relayInfo.ClientRequestMode,
	}
	interChannelRetryState := &service.InterChannelRetryState{}
	inputTokenRoutingUpgrades := 0
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil
	globalRetryPolicy := relayGlobalRetryPolicy()

	for {
		relayInfo.RetryIndex = interChannelRetryState.Count()
		channel, channelErr := getEligibleChannel(c, relayInfo, selectParam)
		if channelErr != nil {
			logger.LogError(c, channelErr.Error())
			newAPIError = channelErr
			service.RecordRelayDebugStageError(c, "channel_selection", service.RelayDebugAttemptMeta{}, channelErr, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
			break
		}
		if channel == nil {
			if service.HasRelayDebugFailures(c) {
				service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
			}
			break
		}
		actualChannelID := 0
		channelRetryPolicy := relayRetryPolicy{}
		sameChannelRetryState := &service.SameChannelRetryState{}
		internalRetryBlockedByOverload := false
		inputTokenRoutingUpgraded := false

		for {
			internalRetry := layeredRetryEnabled && actualChannelID != 0
			selectedChannel, overloadLease, acquireErr := acquireRelayOverloadLease(c, relayInfo, selectParam, channel, internalRetry)
			if acquireErr != nil {
				if internalRetry && acquireErr.GetErrorCode() == types.ErrorCodeChannelOverloaded {
					service.RecordRelayDebugStageError(c, "overload_admission", relayDebugAttemptMeta(c, channel), acquireErr, service.RelayDebugDecision{Action: "stop", Reason: "same_channel_overload"})
					// The channel became overloaded between its upstream attempts.
					// Preserve its last upstream error and let the outer loop decide
					// whether the independent cross-channel budget permits a switch.
					internalRetryBlockedByOverload = true
					break
				}
				newAPIError = acquireErr
				relayInfo.LastError = acquireErr
				service.RecordRelayDebugStageError(c, "overload_admission", relayDebugAttemptMeta(c, channel), acquireErr, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
				break
			}
			if actualChannelID == 0 {
				channel = selectedChannel
				actualChannelID = channel.Id
				channelRetryPolicy = relayRetryPolicyFromContext(c)
			} else {
				channel = selectedChannel
				if !layeredRetryEnabled {
					actualChannelID = channel.Id
				}
			}
			selectedGroup := selectParam.TokenGroup
			if selectedGroup == "auto" {
				selectedGroup = common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
			}
			service.ConfirmChannelAffinitySelection(c, selectedGroup, channel.Id)
			service.SetChannelOverloadLease(c, overloadLease)
			if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
				releaseOverloadLease(overloadLease)
				service.ClearChannelOverloadLease(c)
				newAPIError = billingErr
				break
			}
			addUsedChannel(c, channel.Id)
			bodyStorage, bodyErr := common.GetBodyStorage(c)
			if bodyErr != nil {
				releaseOverloadLease(overloadLease)
				service.ClearChannelOverloadLease(c)
				// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
				if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
					newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
				} else {
					newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
				}
				service.RecordRelayDebugStageError(c, "retry_setup", relayDebugAttemptMeta(c, channel), newAPIError, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
				break
			}
			c.Request.Body = io.NopCloser(bodyStorage)
			if relayFormat == types.RelayFormatOpenAIRealtime && ws == nil {
				ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
				if err != nil {
					service.ClearChannelOverloadLease(c)
					releaseOverloadLease(overloadLease)
					return
				}
				defer ws.Close()
				relayInfo.ClientWs = ws
			}

			relayInfo.BeginUpstreamAttempt(c)
			service.BeginRelayDebugAttempt(c, "relay", relayDebugAttemptMeta(c, channel))
			if modeErr := relaychannel.ValidateChannelRequestMode(relayInfo); modeErr != nil {
				newAPIError = types.NewError(modeErr, types.ErrorCodeDoRequestFailed)
			} else {
				switch relayFormat {
				case types.RelayFormatOpenAIRealtime:
					newAPIError = relay.WssHelper(c, relayInfo)
				case types.RelayFormatClaude:
					newAPIError = relay.ClaudeHelper(c, relayInfo)
				case types.RelayFormatGemini:
					newAPIError = geminiRelayHandler(c, relayInfo)
				default:
					newAPIError = relayHandler(c, relayInfo)
				}
			}
			if newAPIError != nil {
				newAPIError = service.NormalizeViolationFeeError(newAPIError)
			}
			service.CompleteRelayDebugAttempt(c, newAPIError)
			releaseOverloadLease(overloadLease)
			service.ClearChannelOverloadLease(c)

			if newAPIError == nil {
				relayInfo.LastError = nil
				return
			}

			relayInfo.LastError = newAPIError

			processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError, relayInfo)

			if upgradeInputTokenRoutingAfterUpstream400(c, newAPIError, channel, selectParam, inputTokenRoutingUpgrades, globalRetryPolicy, interChannelRetryState) {
				service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "reroute_input_tokens", Reason: "input_token_limit"})
				inputTokenRoutingUpgrades++
				inputTokenRoutingUpgraded = true
				break
			}

			sameChannelDecision := evaluateSameChannelRetryRelayErrorWithPolicy(c, newAPIError, channelRetryPolicy, sameChannelRetryState.Count())
			if !sameChannelDecision.retry {
				break
			}
			action := "retry_same_channel"
			if common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
				action = "rotate_key"
			}
			service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: action, Reason: sameChannelDecision.reason})
			sameChannelRetryState.Increase()
			if !waitBeforeRelayRetry(c, channelRetryPolicy.retryDelay) {
				break
			}
			channel, newAPIError = prepareMultiKeyChannelRetry(c, channel, relayInfo)
			if newAPIError != nil {
				relayInfo.LastError = newAPIError
				service.RecordRelayDebugStageError(c, "retry_setup", relayDebugAttemptMeta(c, channel), newAPIError, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
				break
			}
		}

		if inputTokenRoutingUpgraded {
			continue
		}

		if internalRetryBlockedByOverload {
			if shouldSwitchChannelAfterInternalRetryOverload(c, newAPIError, globalRetryPolicy, interChannelRetryState.Count()) {
				service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "switch_channel", Reason: "same_channel_overload"})
				selectParam.ExcludeAttemptedChannel(channel)
				interChannelRetryState.Increase()
				continue
			}
			service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "stop", Reason: "same_channel_overload"})
			recordInternalRetryOverloadBlocked(c, channel, newAPIError, globalRetryPolicy, interChannelRetryState.Count())
			break
		}

		globalDecision := evaluateRetryRelayErrorWithPolicy(c, newAPIError, globalRetryPolicy, interChannelRetryState.Count(), false, true)
		if !globalDecision.retry {
			service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "stop", Reason: globalDecision.reason})
			break
		}
		service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "switch_channel", Reason: globalDecision.reason})
		if actualChannelID > 0 {
			selectParam.ExcludeAttemptedChannel(channel)
		}
		interChannelRetryState.Increase()
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	if newAPIError != nil {
		gopool.Go(func() {
			perfmetrics.RecordRelaySample(relayInfo, false, 0)
		})
	}
}

func acquireRelayOverloadLease(c *gin.Context, info *relaycommon.RelayInfo, selectParam *service.ChannelSelectParam, channel *model.Channel, channelLocked bool) (*model.Channel, *service.OverloadLease, *types.NewAPIError) {
	for {
		if !service.ChannelAcceptsRequestMode(channel, selectParam.ClientRequestMode) {
			if requestPinsChannel(c) || channelLocked {
				return channel, nil, channelRequestModeError(selectParam.ClientRequestMode, true)
			}
			selectParam.RequestModeFiltered = true
			selectParam.ExcludeUnavailableChannel(channel)
			next, selectGroup, selectErr := service.CacheGetRandomSatisfiedChannel(selectParam)
			if selectErr != nil {
				return channel, nil, types.NewError(selectErr, types.ErrorCodeGetChannelFailed)
			}
			if next == nil {
				return channel, nil, channelRequestModeError(selectParam.ClientRequestMode, false)
			}
			service.ClearRequestChannelAffinitySelection(c)
			if setupErr := middleware.SetupContextForSelectedChannel(c, next, info.UpstreamAttemptModelName()); setupErr != nil {
				return channel, nil, setupErr
			}
			if selectParam.TokenGroup == "auto" {
				common.SetContextKey(c, constant.ContextKeyAutoGroup, selectGroup)
			}
			info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
			info.InitChannelMeta(c)
			channel = next
			continue
		}
		if requestPinsChannel(c) {
			return channel, nil, nil
		}
		keyIndex := common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
		lease, scope, err := service.AcquireChannelOverloadLease(c.Request.Context(), channel, keyIndex)
		if err != nil {
			return channel, nil, types.NewError(err, types.ErrorCodeChannelOverloaded, types.ErrOptionWithStatusCode(http.StatusServiceUnavailable), types.ErrOptionWithSkipRetry())
		}
		if lease != nil || scope == "" {
			return channel, lease, nil
		}
		if scope == service.OverloadScopeMultiKey {
			c.Set("overload_key_selection", true)
			common.SetContextKey(c, constant.ContextKeyChannelMultiKeyOverload, true)
			setupErr := middleware.SetupContextForSelectedChannel(c, channel, info.UpstreamAttemptModelName())
			c.Set("overload_key_selection", false)
			common.SetContextKey(c, constant.ContextKeyChannelMultiKeyOverload, false)
			if setupErr == nil {
				info.InitChannelMeta(c)
				continue
			}
		}
		if channelLocked {
			return channel, nil, channelOverloadedError()
		}

		selectParam.ExcludeUnavailableChannel(channel)
		next, selectGroup, selectErr := service.CacheGetRandomSatisfiedChannel(selectParam)
		if selectErr != nil {
			return channel, nil, types.NewError(selectErr, types.ErrorCodeGetChannelFailed)
		}
		if next == nil {
			return channel, nil, channelOverloadedError()
		}
		service.ClearRequestChannelAffinitySelection(c)
		if setupErr := middleware.SetupContextForSelectedChannel(c, next, info.UpstreamAttemptModelName()); setupErr != nil {
			return channel, nil, setupErr
		}
		if selectParam.TokenGroup == "auto" {
			common.SetContextKey(c, constant.ContextKeyAutoGroup, selectGroup)
		}
		newGroupRatio := helper.HandleGroupRatio(c, info)
		info.PriceData.GroupRatioInfo = newGroupRatio
		info.InitChannelMeta(c)
		channel = next
	}
}

func channelOverloadedError() *types.NewAPIError {
	return types.NewError(errors.New("all available channels are overloaded"), types.ErrorCodeChannelOverloaded, types.ErrOptionWithStatusCode(http.StatusServiceUnavailable), types.ErrOptionWithSkipRetry())
}

func controllerOwnsRelayOverloadLease(relayFormat types.RelayFormat, relayMode int) bool {
	if relayFormat == types.RelayFormatClaude {
		return true
	}
	if relayFormat == types.RelayFormatOpenAIResponses && relayMode == relayconstant.RelayModeResponses {
		return true
	}
	if relayFormat != types.RelayFormatOpenAI {
		return false
	}
	return relayMode == relayconstant.RelayModeChatCompletions || relayMode == relayconstant.RelayModeCompletions
}

func releaseOverloadLease(lease *service.OverloadLease) {
	if lease == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease.Release(ctx)
}

// CountClaudeTokens implements Anthropic's token-counting utility endpoint.
// It deliberately skips upstream generation and billing; callers use this
// endpoint to size prompts before creating a Message.
func CountClaudeTokens(c *gin.Context) {
	request, err := helper.GetAndValidateClaudeRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": common.MessageWithRequestId(err.Error(), c.GetString(common.RequestIdKey)),
			},
		})
		return
	}

	info := relaycommon.GenRelayInfoClaude(c, request)
	inputTokens, err := service.CountRequestToken(c, request.GetTokenCountMeta(), info)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "api_error",
				"message": common.MessageWithRequestId(err.Error(), c.GetString(common.RequestIdKey)),
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"input_tokens": inputTokens})
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func addUsedChannel(c *gin.Context, channelId int) {
	useChannel := c.GetStringSlice("use_channel")
	useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
	c.Set("use_channel", useChannel)
}

func relayDebugAttemptMeta(c *gin.Context, channel *model.Channel) service.RelayDebugAttemptMeta {
	meta := service.RelayDebugAttemptMeta{}
	if channel != nil {
		meta.ChannelId = channel.Id
		meta.ChannelName = channel.Name
		meta.ChannelType = channel.Type
	}
	meta.MultiKey = common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey)
	meta.MultiKeyIndex = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	return meta
}

type relayRetryPolicy struct {
	retryTimes       int
	statusCodeRanges []operation_setting.StatusCodeRange
	channelOverride  bool
	retryDelay       time.Duration
}

func relayGlobalRetryPolicy() relayRetryPolicy {
	return relayRetryPolicy{
		retryTimes:       common.RetryTimes,
		statusCodeRanges: operation_setting.AutomaticRetryStatusCodeRanges,
	}
}

func relayRetryPolicyFromContext(c *gin.Context) relayRetryPolicy {
	policy := relayGlobalRetryPolicy()

	settings, ok := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	if !ok || settings.StatusCodeRetry == nil || !settings.StatusCodeRetry.Enabled {
		return policy
	}

	normalized := settings.StatusCodeRetry.Normalize()
	ranges, err := operation_setting.ParseHTTPStatusCodeRanges(normalized.StatusCodes)
	if err != nil {
		logger.LogWarn(c, fmt.Sprintf("invalid channel status code retry settings, using global retry policy: %s", err.Error()))
		return policy
	}

	policy.retryTimes = normalized.RetryTimes
	policy.statusCodeRanges = ranges
	policy.channelOverride = true
	policy.retryDelay = time.Duration(normalized.RetryIntervalMS) * time.Millisecond
	return policy
}

func fastTokenCountMetaForPricing(request dto.Request) *types.TokenCountMeta {
	if request == nil {
		return &types.TokenCountMeta{}
	}
	meta := &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
		maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
		if maxCompletionTokens > maxTokens {
			meta.MaxTokens = int(maxCompletionTokens)
		} else {
			meta.MaxTokens = int(maxTokens)
		}
	case *dto.OpenAIResponsesRequest:
		meta.MaxTokens = int(lo.FromPtrOr(r.MaxOutputTokens, uint(0)))
	case *dto.ClaudeRequest:
		meta.MaxTokens = int(lo.FromPtr(r.MaxTokens))
	case *dto.ImageRequest:
		// Pricing for image requests depends on ImagePriceRatio; safe to compute even when CountToken is disabled.
		return r.GetTokenCountMeta()
	default:
		// Best-effort: leave CombineText empty to avoid large allocations.
	}
	return meta
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, selectParam *service.ChannelSelectParam) (*model.Channel, *types.NewAPIError) {
	if info.ChannelMeta == nil {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		channelID := c.GetInt("channel_id")
		currentChannel, currentChannelErr := model.CacheGetChannel(channelID)
		if currentChannelErr == nil && currentChannel != nil {
			if selectParam != nil {
				if _, excluded := selectParam.ExcludedChannelIDs[currentChannel.Id]; excluded {
					return selectChannelByInputTokenRouting(c, info, selectParam)
				}
			}
			if selectParam == nil || selectParam.InputTokenEstimates == nil {
				return currentChannel, nil
			}
			if !requestPinsChannel(c) {
				if !currentChannel.MatchesInputTokenRouting(selectParam.InputTokenEstimates) {
					service.ClearRequestChannelAffinitySelection(c)
					channel, channelErr := selectChannelByInputTokenRouting(c, info, selectParam)
					if channelErr != nil {
						return nil, channelErr
					}
					return channel, nil
				}
			}
			return currentChannel, nil
		}
		return &model.Channel{
			Id:      channelID,
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}, nil
	}
	return selectChannelByInputTokenRouting(c, info, selectParam)
}

func getEligibleChannel(c *gin.Context, info *relaycommon.RelayInfo, selectParam *service.ChannelSelectParam) (*model.Channel, *types.NewAPIError) {
	skippedDisabledMode := false
	for {
		channel, channelErr := getChannel(c, info, selectParam)
		if channelErr != nil || channel == nil {
			if channelErr == nil && (skippedDisabledMode || selectParam.RequestModeFiltered) {
				return nil, channelRequestModeError(info.ClientRequestMode, false)
			}
			return channel, channelErr
		}
		if info.ClientRequestMode == types.RequestModeUnknown || service.ChannelAcceptsRequestMode(channel, info.ClientRequestMode) {
			return channel, nil
		}
		if requestPinsChannel(c) {
			return nil, channelRequestModeError(info.ClientRequestMode, true)
		}

		skippedDisabledMode = true
		selectParam.ExcludeUnavailableChannel(channel)
		service.ClearRequestChannelAffinitySelection(c)
		logger.LogInfo(c, fmt.Sprintf("skipping channel %d because %s requests are disabled", channel.Id, info.ClientRequestMode.String()))
	}
}

func channelRequestModeError(requestMode types.RequestMode, selected bool) *types.NewAPIError {
	errorCode := types.ErrorCodeChannelNonStreamDisabled
	if requestMode == types.RequestModeStream {
		errorCode = types.ErrorCodeChannelStreamDisabled
	}
	message := fmt.Sprintf("no eligible channel accepts %s requests", requestMode.String())
	if selected {
		message = fmt.Sprintf("the selected channel does not accept %s requests", requestMode.String())
	}
	return types.NewErrorWithStatusCode(errors.New(message), errorCode, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

func requestPinsChannel(c *gin.Context) bool {
	if c == nil {
		return false
	}
	_, pinned, _ := service.GetChannelConstraints(c).ResolvedPin()
	return pinned
}

func prepareMultiKeyChannelRetry(c *gin.Context, channel *model.Channel, info *relaycommon.RelayInfo) (*model.Channel, *types.NewAPIError) {
	if !common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
		return channel, nil
	}
	if channel == nil || !channel.ChannelInfo.IsMultiKey {
		channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
		loadedChannel, err := model.CacheGetChannel(channelID)
		if err != nil {
			return channel, types.NewError(err, types.ErrorCodeGetChannelFailed)
		}
		channel = loadedChannel
	}
	if newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.UpstreamAttemptModelName()); newAPIError != nil {
		return channel, newAPIError
	}
	info.InitChannelMeta(c)
	return channel, nil
}

func selectChannelByInputTokenRouting(c *gin.Context, info *relaycommon.RelayInfo, selectParam *service.ChannelSelectParam) (*model.Channel, *types.NewAPIError) {
	channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(selectParam)

	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
	if err != nil {
		return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if channel == nil {
		if len(selectParam.ExcludedChannelIDs) > 0 {
			return nil, nil
		}
		return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.UpstreamAttemptModelName())
	if newAPIError != nil {
		return nil, newAPIError
	}
	return channel, nil
}

func inputTokenEstimatesForRouting(relayFormat types.RelayFormat, relayMode int, tokens int, meta *types.TokenCountMeta, modelName string) *dto.InputTokenEstimates {
	if !supportsInputTokenRouting(relayFormat, relayMode) {
		return nil
	}
	if tokens < 0 {
		tokens = 0
	}
	if routingTokens := conservativeInputTokensForRouting(relayFormat, meta, modelName); routingTokens > tokens {
		tokens = routingTokens
	}
	return &dto.InputTokenEstimates{
		Default: tokens,
		GLM52:   glm52InputTokensForRouting(relayFormat, meta),
		KimiK3:  kimiK3InputTokensForRouting(relayFormat, meta),
	}
}

func conservativeInputTokensForRouting(relayFormat types.RelayFormat, meta *types.TokenCountMeta, modelName string) int {
	if meta == nil {
		return 0
	}

	tokens := inputTokenRoutingOverhead(relayFormat, meta)
	if meta.CombineText != "" {
		textTokens := service.CountTextToken(meta.CombineText, modelName)
		byteTokens := (len(meta.CombineText) + 3) / 4
		if byteTokens > textTokens {
			textTokens = byteTokens
		}
		tokens += textTokens
	}
	return tokens
}

func glm52InputTokensForRouting(relayFormat types.RelayFormat, meta *types.TokenCountMeta) int {
	return service.EstimateModelFamilyInputTokens(dto.UsageEstimationModelFamilyGLM, relayFormat, meta)
}

func kimiK3InputTokensForRouting(relayFormat types.RelayFormat, meta *types.TokenCountMeta) int {
	return service.EstimateModelFamilyInputTokens(dto.UsageEstimationModelFamilyKimi, relayFormat, meta)
}

func inputTokenRoutingOverhead(relayFormat types.RelayFormat, meta *types.TokenCountMeta) int {
	if meta == nil {
		return 0
	}
	tokens := inputTokenRoutingFramingOverhead(relayFormat, meta)
	for _, file := range meta.Files {
		if file == nil {
			continue
		}
		switch file.FileType {
		case types.FileTypeImage:
			tokens += 520
		case types.FileTypeAudio:
			tokens += 256
		case types.FileTypeVideo:
			tokens += 8192
		case types.FileTypeFile:
			tokens += 4096
		}
	}
	return tokens
}

func inputTokenRoutingFramingOverhead(relayFormat types.RelayFormat, meta *types.TokenCountMeta) int {
	if meta == nil {
		return 0
	}
	tokens := 0
	if relayFormat == types.RelayFormatOpenAI {
		tokens += meta.ToolsCount * 8
		tokens += meta.MessagesCount * 3
		tokens += meta.NameCount * 3
		tokens += 3
	}
	return tokens
}

func upgradeInputTokenRoutingAfterUpstream400(
	c *gin.Context,
	err *types.NewAPIError,
	channel *model.Channel,
	selectParam *service.ChannelSelectParam,
	upgradeCount int,
	policy relayRetryPolicy,
	retryState *service.InterChannelRetryState,
) bool {
	if err == nil || err.GetUpstreamStatusCode() != http.StatusBadRequest || channel == nil || selectParam == nil || selectParam.InputTokenEstimates == nil {
		return false
	}
	if requestPinsChannel(c) {
		return false
	}
	if _, specificChannel := c.Get("specific_channel_id"); specificChannel {
		return false
	}
	match := channel.MatchInputTokenRouting(selectParam.InputTokenEstimates)
	if !match.Enabled || !match.Matched || match.MaxTokens <= 0 || match.MaxTokens == math.MaxInt {
		return false
	}
	if upgradeCount > 0 {
		if retryState == nil || retryState.Count() >= policy.retryTimes {
			return false
		}
		retryState.Increase()
	}

	upgradedEstimate := match.MaxTokens + 1
	mode := "default"
	if match.KimiK3Mode {
		selectParam.InputTokenEstimates.KimiK3 = upgradedEstimate
		mode = "kimi_k3"
	} else if match.GLM52Mode {
		selectParam.InputTokenEstimates.GLM52 = upgradedEstimate
		mode = "glm_5_2"
	} else {
		selectParam.InputTokenEstimates.Default = upgradedEstimate
	}
	selectParam.ExcludeAttemptedChannel(channel)
	service.ClearRequestChannelAffinitySelection(c)
	logger.LogInfo(c, fmt.Sprintf(
		"input token routing upgraded after upstream 400: channel=%d mode=%s estimate=%d range_max=%d upgraded_estimate=%d",
		channel.Id,
		mode,
		match.EstimatedTokens,
		match.MaxTokens,
		upgradedEstimate,
	))
	return true
}

func supportsInputTokenRouting(relayFormat types.RelayFormat, relayMode int) bool {
	if relayFormat == types.RelayFormatClaude {
		return true
	}
	switch relayMode {
	case relayconstant.RelayModeChatCompletions,
		relayconstant.RelayModeCompletions,
		relayconstant.RelayModeResponses,
		relayconstant.RelayModeResponsesCompact,
		relayconstant.RelayModeGemini:
		return true
	default:
		return false
	}
}

func shouldRetryWithPolicy(c *gin.Context, openaiErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int) bool {
	return shouldRetryRelayErrorWithPolicy(c, openaiErr, policy, currentRetry, false, true)
}

func shouldRetrySameChannelWithPolicy(c *gin.Context, openaiErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int) bool {
	return evaluateSameChannelRetryRelayErrorWithPolicy(c, openaiErr, policy, currentRetry).retry
}

func evaluateSameChannelRetryRelayErrorWithPolicy(c *gin.Context, openaiErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int) relayRetryEvaluation {
	if !policy.channelOverride {
		return relayRetryEvaluation{reason: "channel_override_disabled"}
	}
	return evaluateRetryRelayErrorWithPolicy(c, openaiErr, policy, currentRetry, true, false)
}

func shouldRetryRelayErrorWithPolicy(c *gin.Context, openaiErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int, allowSpecificChannelRetry bool, respectAffinitySkip bool) bool {
	return evaluateRetryRelayErrorWithPolicy(c, openaiErr, policy, currentRetry, allowSpecificChannelRetry, respectAffinitySkip).retry
}

type relayRetryEvaluation struct {
	retry  bool
	reason string
}

func evaluateRetryRelayErrorWithPolicy(c *gin.Context, openaiErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int, allowSpecificChannelRetry bool, respectAffinitySkip bool) relayRetryEvaluation {
	if openaiErr == nil {
		return relayRetryEvaluation{reason: "no_error"}
	}
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return relayRetryEvaluation{reason: "client_canceled"}
	}
	if respectAffinitySkip && service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return relayRetryEvaluation{reason: "affinity_locked"}
	}
	if types.IsSkipRetryError(openaiErr) {
		return relayRetryEvaluation{reason: "skip_retry"}
	}
	if service.GetChannelConstraints(c).SuppressesRetry() {
		return relayRetryEvaluation{reason: "channel_pin_single_attempt"}
	}
	if !allowSpecificChannelRetry && requestPinsChannel(c) {
		return relayRetryEvaluation{reason: "specific_channel"}
	}
	if policy.retryTimes-currentRetry <= 0 {
		return relayRetryEvaluation{reason: "budget_exhausted"}
	}

	if openaiErr.GetErrorCode() == types.ErrorCodeChannelStreamError {
		if allowSpecificChannelRetry {
			// A stream error is returned only before any upstream payload was
			// committed, so a configured channel override can safely replay the
			// request on the same channel before cross-channel fallback.
			return relayRetryEvaluation{retry: policy.channelOverride, reason: "stream_error"}
		}
		return relayRetryEvaluation{retry: true, reason: "stream_error"}
	}
	if openaiErr.GetErrorCode() == types.ErrorCodeChannelResponseHeaderTimeout {
		if allowSpecificChannelRetry {
			return relayRetryEvaluation{retry: policy.channelOverride, reason: "response_header_timeout"}
		}
		return relayRetryEvaluation{retry: true, reason: "response_header_timeout"}
	}
	if openaiErr.GetErrorCode() == types.ErrorCodeChannelResponseBodyTimeout {
		if allowSpecificChannelRetry {
			return relayRetryEvaluation{retry: policy.channelOverride, reason: "response_body_timeout"}
		}
		return relayRetryEvaluation{retry: true, reason: "response_body_timeout"}
	}
	// Response-content fallback is a retryable upstream response, not an
	// admission/transport failure. Let its status code use the normal retry
	// policy, including a configured channel-local override.
	if types.IsChannelError(openaiErr) && openaiErr.GetErrorCode() != types.ErrorCodeChannelResponseContentMatch {
		if allowSpecificChannelRetry && c.GetBool("layered_relay_retry") {
			return relayRetryEvaluation{retry: openaiErr.GetErrorCode() == types.ErrorCodeChannelResponseTimeExceeded, reason: "channel_error"}
		}
		return relayRetryEvaluation{retry: true, reason: "channel_error"}
	}
	code := openaiErr.StatusCode
	if code >= 200 && code < 300 {
		return relayRetryEvaluation{reason: "status_code_excluded"}
	}
	if code < 100 || code > 599 {
		return relayRetryEvaluation{retry: true, reason: "status_code_match"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(openaiErr.GetErrorCode()) {
		return relayRetryEvaluation{reason: "always_skip_code"}
	}
	if operation_setting.ShouldRetryByStatusCodeRanges(policy.statusCodeRanges, code) {
		return relayRetryEvaluation{retry: true, reason: "status_code_match"}
	}
	return relayRetryEvaluation{reason: "status_code_excluded"}
}

func shouldSwitchChannelAfterInternalRetryOverload(c *gin.Context, lastUpstreamErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int) bool {
	if lastUpstreamErr == nil || types.IsSkipRetryError(lastUpstreamErr) {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if requestPinsChannel(c) {
		return false
	}
	return currentRetry < policy.retryTimes
}

func recordInternalRetryOverloadBlocked(c *gin.Context, channel *model.Channel, lastUpstreamErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int) {
	if c == nil || channel == nil || lastUpstreamErr == nil {
		return
	}
	reason := "retry_budget_exhausted"
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		reason = "affinity"
	} else if requestPinsChannel(c) {
		reason = "specific_channel"
	} else if types.IsSkipRetryError(lastUpstreamErr) {
		reason = "skip_retry"
	}
	marker := map[string]interface{}{
		"reason":                  reason,
		"channel_id":              channel.Id,
		"inter_channel_retry":     currentRetry,
		"inter_channel_retry_max": policy.retryTimes,
	}
	c.Set("internal_retry_overload_blocked", marker)
	logger.LogWarn(c, fmt.Sprintf("same-channel retry blocked by overload: channel=%d reason=%s inter_retry=%d/%d", channel.Id, reason, currentRetry, policy.retryTimes))
}

func waitBeforeRelayRetry(c *gin.Context, delay time.Duration) bool {
	if c == nil || c.Request == nil {
		return true
	}
	if c.Request.Context().Err() != nil {
		return false
	}
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	completed := true
	select {
	case <-timer.C:
	case <-c.Request.Context().Done():
		completed = false
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	return completed
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	if service.HandleMoonshotQuotaErrorWithContext(c.Request.Context(), channelError, err) {
		return
	}
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	if service.ShouldDisableChannel(err) && channelError.AutoBan {
		gopool.Go(func() {
			service.DisableChannel(channelError, err.ErrorWithStatusCode())
		})
	}
}

func recordFinalRelayError(c *gin.Context, err *types.NewAPIError, relayInfos ...*relaycommon.RelayInfo) {
	if c == nil || !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) {
		return
	}

	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	channelId := c.GetInt("channel_id")
	other := buildRelayErrorLogDetails(c, err, channelId)
	if len(relayInfos) > 0 && relayInfos[0] != nil {
		service.AppendRelayLogAdminInfo(c, relayInfos[0], other)
	}
	model.RecordErrorLog(
		c,
		c.GetInt("id"),
		channelId,
		c.GetString("original_model"),
		c.GetString("token_name"),
		err.MaskSensitiveError(),
		c.GetInt("token_id"),
		int(time.Since(startTime).Seconds()),
		common.GetContextKeyBool(c, constant.ContextKeyIsStream),
		c.GetString("group"),
		other,
	)
}

func buildRelayErrorLogDetails(c *gin.Context, err *types.NewAPIError, channelId int) *model.LogOther {
	other := model.NewLogOther()
	if c.Request != nil && c.Request.URL != nil {
		other.SetPublic("request_path", c.Request.URL.Path)
	}
	other.SetPublic("error_type", err.GetErrorType())
	other.SetPublic("error_code", err.GetErrorCode())
	other.SetPublic("status_code", err.StatusCode)
	other.SetAdmin("channel_id", channelId)
	other.SetAdmin("channel_name", c.GetString("channel_name"))
	other.SetAdmin("channel_type", c.GetInt("channel_type"))

	adminInfo := make(map[string]interface{})
	adminInfo["use_channel"] = c.GetStringSlice("use_channel")
	if upstreamStatusCode := err.GetUpstreamStatusCode(); upstreamStatusCode != 0 {
		adminInfo["upstream_status_code"] = upstreamStatusCode
	}
	if upstreamResponse := err.GetUpstreamResponse(); upstreamResponse != "" {
		upstreamResponse = common.MaskSensitiveInfo(upstreamResponse)
		if len(upstreamResponse) > maxRelayErrorResponseLogBytes {
			originalLength := len(upstreamResponse)
			upstreamResponse = fmt.Sprintf(
				"%s... [truncated, original_length=%d, limit=%d]",
				strings.ToValidUTF8(upstreamResponse[:maxRelayErrorResponseLogBytes], "\uFFFD"),
				originalLength,
				maxRelayErrorResponseLogBytes,
			)
		}
		adminInfo["upstream_response"] = upstreamResponse
	}
	if common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
		adminInfo["is_multi_key"] = true
		adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	}
	service.AppendChannelAffinityAdminInfo(c, other)
	if marker, ok := c.Get("internal_retry_overload_blocked"); ok {
		adminInfo["internal_retry_overload_blocked"] = marker
	}
	other.MergeAdmin(adminInfo)
	service.AppendRelayDebugAdminInfo(c, other)
	return other
}

func RelayMidjourney(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"description": fmt.Sprintf("failed to generate relay info: %s", err.Error()),
			"type":        "upstream_error",
			"code":        4,
		})
		return
	}

	protectedSubmission := relayInfo.RelayMode != relayconstant.RelayModeMidjourneyNotify &&
		relayInfo.RelayMode != relayconstant.RelayModeMidjourneyTaskFetch &&
		relayInfo.RelayMode != relayconstant.RelayModeMidjourneyTaskFetchByCondition &&
		relayInfo.RelayMode != relayconstant.RelayModeMidjourneyTaskImageSeed
	if protectedSubmission {
		c.Set("overload_admission", func(channel *model.Channel, locked bool) *types.NewAPIError {
			selectParam := &service.ChannelSelectParam{
				Ctx: c, TokenGroup: relayInfo.TokenGroup, ModelName: relayInfo.OriginModelName,
				RequestPath:       c.Request.URL.Path,
				AutoGroupIndex:    common.GetContextKeyInt(c, constant.ContextKeyAutoGroupIndex),
				AutoGroupSelected: relayInfo.TokenGroup == "auto" && common.GetContextKeyString(c, constant.ContextKeyAutoGroup) != "",
				ClientRequestMode: relayInfo.ClientRequestMode,
			}
			selected, lease, newAPIError := acquireRelayOverloadLease(c, relayInfo, selectParam, channel, locked)
			if newAPIError != nil {
				return newAPIError
			}
			channel = selected
			service.SetChannelOverloadLease(c, lease)
			relayInfo.InitChannelMeta(c)
			addUsedChannel(c, channel.Id)
			return nil
		})
		c.Set("overload_admit_current", func() *types.NewAPIError {
			channel, channelErr := model.CacheGetChannel(common.GetContextKeyInt(c, constant.ContextKeyChannelId))
			if channelErr != nil {
				return types.NewError(channelErr, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
			}
			admit := c.MustGet("overload_admission").(func(*model.Channel, bool) *types.NewAPIError)
			return admit(channel, false)
		})
		defer func() {
			lease := service.GetChannelOverloadLease(c)
			releaseOverloadLease(lease)
			service.ClearChannelOverloadLease(c)
		}()
	}

	var mjErr *taskdto.MidjourneyResponse
	switch relayInfo.RelayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		mjErr = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		mjErr = relay.RelayMidjourneyTask(c, relayInfo.RelayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		mjErr = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		mjErr = relay.RelaySwapFace(c, relayInfo)
	default:
		mjErr = relay.RelayMidjourneySubmit(c, relayInfo)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(mjErr)
	if mjErr != nil {
		if mjErr.Description == string(types.ErrorCodeChannelOverloaded) ||
			mjErr.Description == string(types.ErrorCodeChannelStreamDisabled) ||
			mjErr.Description == string(types.ErrorCodeChannelNonStreamDisabled) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"description": mjErr.Result,
				"type":        "new_api_error",
				"code":        mjErr.Description,
			})
			return
		}
		statusCode := http.StatusBadRequest
		if mjErr.Code == 30 {
			mjErr.Result = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result),
			"type":        "upstream_error",
			"code":        mjErr.Code,
		})
		channelId := c.GetInt("channel_id")
		logger.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result)))
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := types.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	// The web fallback may already have applied static-asset cache headers.
	// A missing API or asset can appear after an upgrade; never cache its 404.
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

// RelayTaskPluginEndpoint keeps unclaimed shared-endpoint traffic on its
// existing handler while claimed requests enter the generation-pinned
// host-owned protocol bridge.
func RelayTaskPluginEndpoint(c *gin.Context, fallback gin.HandlerFunc) {
	pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedEndpoint)
	if !exists {
		fallback(c)
		return
	}
	pinned, ok := pinnedValue.(pluginruntime.PinnedEndpoint)
	if !ok || pinned.Plugin == nil || pinned.Generation == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Task protocol request failed",
				"type":    "new_api_error",
				"code":    "task_protocol_error",
			},
		})
		return
	}
	if pinned.Protocol != "openai_responses" {
		fallback(c)
		return
	}
	serveTaskPluginProtocol(c, pinned, defaultPluginProtocolBridgeDeps())
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

type taskSubmissionOutcome struct {
	Result    *relay.TaskSubmitResult
	Task      *model.Task
	RelayInfo *relaycommon.RelayInfo
}

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		respondTaskSubmissionError(c, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	service.StartRelayDebug(c)
	if action := c.GetString("task_action"); action != "" {
		relayInfo.Action = action
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskSubmissionError(c, taskErr)
		return
	}
	if taskErr := relay.ApplyOriginTaskAffinity(c, relayInfo); taskErr != nil {
		respondTaskSubmissionError(c, taskErr)
		return
	}
	relayInfo.CaptureUpstreamAttemptBaseline()

	outcome, taskErr := executeTaskSubmission(c, relayInfo)
	if taskErr != nil {
		respondTaskSubmissionError(c, taskErr)
		return
	}
	presentTaskSubmission(c, outcome)
}

// executeTaskSubmission owns the retry, billing, and persistence lifecycle.
// It deliberately performs no client response writes so JSON and protocol
// presenters share the same durable task barrier. Its cancellation semantics
// come from c.Request.Context: native task endpoints use the client context,
// while the Responses bridge supplies an independently bounded context.
func executeTaskSubmission(c *gin.Context, relayInfo *relaycommon.RelayInfo) (*taskSubmissionOutcome, *taskdto.TaskError) {
	return executeTaskSubmissionWith(c, relayInfo, relay.RelayTaskSubmit)
}

type taskSubmitAttempt func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *taskdto.TaskError)

func executeTaskSubmissionWith(
	c *gin.Context,
	relayInfo *relaycommon.RelayInfo,
	submit taskSubmitAttempt,
) (*taskSubmissionOutcome, *taskdto.TaskError) {
	diagnostics := newTaskPluginSubmitDiagnostics(c)
	diagnostics.start(relayInfo)
	var result *relay.TaskSubmitResult
	var taskErr *taskdto.TaskError
	durable := false
	stage := "start"
	defer func() {
		if !durable && relayInfo.Billing != nil {
			diagnostics.refund(stage)
			relayInfo.Billing.Refund(c)
		}
	}()
	stage = "before_attempt"
	if requestErr := c.Request.Context().Err(); requestErr != nil {
		diagnostics.cancelled("before_attempt", 0)
		return nil, service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
	}

	selectParam := &service.ChannelSelectParam{
		Ctx:               c,
		TokenGroup:        relayInfo.TokenGroup,
		ModelName:         relayInfo.OriginModelName,
		RequestPath:       c.Request.URL.Path,
		AutoGroupIndex:    common.GetContextKeyInt(c, constant.ContextKeyAutoGroupIndex),
		AutoGroupSelected: relayInfo.TokenGroup == "auto" && common.GetContextKeyString(c, constant.ContextKeyAutoGroup) != "",
		ClientRequestMode: relayInfo.ClientRequestMode,
	}
	interChannelRetryState := &service.InterChannelRetryState{}

	for interChannelRetryState.Count() <= common.RetryTimes {
		stage = "select_channel"
		if requestErr := c.Request.Context().Err(); requestErr != nil {
			diagnostics.cancelled("before_attempt", interChannelRetryState.Count()+1)
			taskErr = service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
			break
		}
		var channel *model.Channel
		var channelErr *types.NewAPIError

		if lockedCh, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedCh != nil {
			channel = lockedCh
			if !service.ChannelAcceptsRequestMode(channel, relayInfo.ClientRequestMode) {
				modeErr := channelRequestModeError(relayInfo.ClientRequestMode, true)
				taskErr = service.TaskErrorFromAPIError(modeErr)
				service.RecordRelayDebugStageError(c, "channel_selection", relayDebugAttemptMeta(c, channel), modeErr, service.RelayDebugDecision{Action: "stop", Reason: "request_mode_disabled"})
				break
			}
			if interChannelRetryState.Count() > 0 {
				if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.UpstreamAttemptModelName()); setupErr != nil {
					taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
					service.RecordRelayDebugStageError(c, "retry_setup", relayDebugAttemptMeta(c, channel), setupErr, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
					break
				}
			}
		} else {
			channel, channelErr = getEligibleChannel(c, relayInfo, selectParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				if channelErr.GetErrorCode() == types.ErrorCodeChannelStreamDisabled || channelErr.GetErrorCode() == types.ErrorCodeChannelNonStreamDisabled {
					taskErr = service.TaskErrorFromAPIError(channelErr)
				} else {
					taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				}
				service.RecordRelayDebugStageError(c, "channel_selection", service.RelayDebugAttemptMeta{}, channelErr, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
				break
			}
			if channel == nil {
				break
			}
		}
		diagnostics.attempt(interChannelRetryState.Count()+1, channel, relayInfo.LockedChannel != nil)

		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			stage = "read_body"
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			service.BeginRelayDebugAttempt(c, "retry_setup", relayDebugAttemptMeta(c, channel))
			service.CompleteRelayDebugTaskAttempt(c, taskErr)
			service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "stop", Reason: "retry_setup_failed"})
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		var overloadLease *service.OverloadLease
		locked := relayInfo.LockedChannel != nil
		channel, overloadLease, channelErr = acquireRelayOverloadLease(c, relayInfo, selectParam, channel, locked)
		if channelErr != nil {
			taskErr = service.TaskErrorWrapperLocal(channelErr.Err, string(types.ErrorCodeChannelOverloaded), http.StatusServiceUnavailable)
			service.RecordRelayDebugStageError(c, "overload_admission", relayDebugAttemptMeta(c, channel), channelErr, service.RelayDebugDecision{Action: "stop", Reason: "same_channel_overload"})
			break
		}
		service.SetChannelOverloadLease(c, overloadLease)
		addUsedChannel(c, channel.Id)
		relayInfo.BeginUpstreamAttempt(c)
		service.BeginRelayDebugAttempt(c, "task_submit", relayDebugAttemptMeta(c, channel))
		stage = "submit"
		result, taskErr = submit(c, relayInfo)
		service.CompleteRelayDebugTaskAttempt(c, taskErr)
		releaseOverloadLease(overloadLease)
		service.ClearChannelOverloadLease(c)
		if requestErr := c.Request.Context().Err(); requestErr != nil {
			diagnostics.cancelled("after_submit", interChannelRetryState.Count()+1)
			taskErr = service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
			break
		}
		if taskErr == nil {
			diagnostics.attemptSucceeded(interChannelRetryState.Count()+1, result)
			break
		}

		if !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode),
				relayInfo)
		}

		taskDecision := evaluateTaskRelayRetry(c, channel.Id, taskErr, common.RetryTimes-interChannelRetryState.Count())
		diagnostics.attemptFailed(interChannelRetryState.Count()+1, channel, taskErr, taskDecision.retry)
		if !taskDecision.retry {
			service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "stop", Reason: taskDecision.reason})
			break
		}
		service.SetRelayDebugDecision(c, service.RelayDebugDecision{Action: "switch_channel", Reason: taskDecision.reason})
		selectParam.ExcludeAttemptedChannel(channel)
		interChannelRetryState.Increase()
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	if taskErr != nil {
		finalErr := taskErr.Error
		if finalErr == nil {
			finalErr = errors.New(taskErr.Message)
		}
		recordFinalRelayError(c, types.NewOpenAIError(finalErr, types.ErrorCode(taskErr.Code), taskErr.StatusCode), relayInfo)
		diagnostics.failed(stage, "task_error", taskErr, false)
		return nil, taskErr
	}
	if result == nil {
		taskErr = service.TaskErrorWrapperLocal(errors.New("task submission returned no result"), "task_submit_failed", http.StatusInternalServerError)
		diagnostics.failed("submit", "missing_result", taskErr, false)
		return nil, taskErr
	}
	if requestErr := c.Request.Context().Err(); requestErr != nil {
		diagnostics.cancelled("before_reserve", interChannelRetryState.Count()+1)
		return nil, service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
	}

	// Reserve any submit-time upward billing adjustment before persistence.
	// This keeps insertion failures fully refundable while ensuring settlement
	// after the barrier normally has a zero positive delta.
	if relayInfo.Billing != nil {
		stage = "reserve"
		diagnostics.reserve("reserve_start", result.Quota)
		if reserveErr := relayInfo.Billing.Reserve(result.Quota); reserveErr != nil {
			common.SysError("reserve adjusted task billing error: " + reserveErr.Error())
			taskErr = service.TaskErrorWrapperLocal(errors.New("insufficient quota for adjusted task cost"), string(types.ErrorCodeInsufficientUserQuota), http.StatusForbidden)
			diagnostics.failed("reserve", "insufficient_quota", taskErr, false)
			return nil, taskErr
		}
		diagnostics.reserve("reserve_complete", result.Quota)
	}
	if requestErr := c.Request.Context().Err(); requestErr != nil {
		diagnostics.cancelled("before_insert", interChannelRetryState.Count()+1)
		return nil, service.TaskErrorWrapperLocal(requestErr, "request_cancelled", http.StatusRequestTimeout)
	}

	stage = "insert"
	task := model.InitTask(result.Platform, relayInfo)
	task.PrivateData.Execution = service.TaskExecutionSnapshotFromContext(c)
	task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
	task.PrivateData.BillingSource = relayInfo.BillingSource
	task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
	task.PrivateData.TokenId = relayInfo.TokenId
	task.PrivateData.NodeName = common.NodeName
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		ModelPrice:      relayInfo.PriceData.ModelPrice,
		GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
		ModelRatio:      relayInfo.PriceData.ModelRatio,
		OtherRatios:     relayInfo.PriceData.OtherRatios(),
		OriginModelName: relayInfo.OriginModelName,
		PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		TieredSnapshot:  relayInfo.TieredBillingSnapshot,
	}
	task.Quota = result.Quota
	task.Data = result.TaskData
	if len(result.PluginState) > 0 {
		task.PrivateData.PluginState = result.PluginState
	}
	task.Action = relayInfo.Action
	if immediate := result.Immediate; immediate != nil {
		task.Status = model.TaskStatus(immediate.Status)
		task.Progress = immediate.Progress
		if immediate.Status == model.TaskStatusSuccess || immediate.Status == model.TaskStatusFailure {
			task.FinishTime = time.Now().Unix()
		}
		if immediate.Status == model.TaskStatusFailure {
			task.FailReason = immediate.Reason
		}
		if immediate.Url != "" {
			task.PrivateData.ResultURL = immediate.Url
		} else if immediate.Status == model.TaskStatusSuccess {
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
	}
	diagnostics.insertStart(task)
	if insertErr := task.InsertWithContext(c.Request.Context()); insertErr != nil {
		common.SysError("insert task error: " + insertErr.Error())
		taskErr = service.TaskErrorWrapperLocal(errors.New("failed to persist task"), "task_insert_failed", http.StatusInternalServerError)
		diagnostics.failed("insert", "database_error", taskErr, false)
		return nil, taskErr
	}
	durable = true
	stage = "settle"
	diagnostics.durable(task)
	diagnostics.settleStart(task, result.Quota)

	if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
		common.SysError("settle task billing error: " + settleErr.Error())
		taskErr = service.TaskErrorWrapperLocal(errors.New("failed to settle task billing"), "task_billing_settlement_failed", http.StatusInternalServerError)
		diagnostics.failed("settle", "billing_error", taskErr, true)
		return nil, taskErr
	}
	service.LogTaskConsumption(c, relayInfo, task)
	diagnostics.complete(task, result.Quota)

	return &taskSubmissionOutcome{Result: result, Task: task, RelayInfo: relayInfo}, nil
}

func presentTaskSubmission(c *gin.Context, outcome *taskSubmissionOutcome) {
	diagnostics := newTaskPluginSubmitDiagnostics(c)
	otherRatios := outcome.RelayInfo.PriceData.OtherRatios()
	if otherRatios == nil {
		otherRatios = map[string]float64{}
	}
	if ratiosJSON, err := common.Marshal(otherRatios); err == nil {
		c.Header("X-New-Api-Other-Ratios", string(ratiosJSON))
	}
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedRoute); exists {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedRoute); ok && pinned.Plugin != nil && pinned.Route.Render != "" {
			view, err := service.BuildTaskPluginView(outcome.Task)
			requestValue, _ := c.Get(pluginruntime.ContextKeyRouteRequest)
			requestContext, _ := requestValue.(pluginruntime.RouteRequestContext)
			if err == nil {
				viewValue, valueErr := taskPluginProtocolJSONValue(view)
				if valueErr == nil {
					if body, callErr := pinned.Plugin.Engine.CallPath(c.Request.Context(), "native", []string{pinned.Route.Render}, requestContext.JSValue(), viewValue); callErr == nil {
						diagnostics.present(outcome.Task, "native_presenter")
						c.JSON(http.StatusOK, body)
						return
					} else {
						logger.LogError(c, "task plugin native submit presenter failed: "+callErr.Error())
					}
				} else {
					logger.LogError(c, "encode task plugin native submit view failed: "+valueErr.Error())
				}
			} else {
				logger.LogError(c, "build task plugin native submit view failed: "+err.Error())
			}
		}
	}
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedEndpoint); exists {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedEndpoint); ok && pinned.Protocol == "openai_video" && pinned.Operation.Name == "create" {
			diagnostics.present(outcome.Task, "openai_video_create")
			c.JSON(http.StatusOK, outcome.Task.ToOpenAIVideo())
			return
		}
	}
	createdAt := outcome.Task.CreatedAt
	if createdAt == 0 {
		createdAt = outcome.Task.SubmitTime
	}
	diagnostics.present(outcome.Task, "host_fallback")
	c.JSON(http.StatusOK, map[string]any{
		"id":         outcome.Task.TaskID,
		"task_id":    outcome.Task.TaskID,
		"status":     "queued",
		"model":      outcome.RelayInfo.OriginModelName,
		"created_at": createdAt,
	})
}

func respondTaskSubmissionError(c *gin.Context, taskErr *taskdto.TaskError) {
	newTaskPluginSubmitDiagnostics(c).presentError(taskErr)
	if middleware.RespondTaskPluginError(c, taskErr) {
		return
	}
	respondTaskError(c, taskErr)
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *taskdto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *taskdto.TaskError, retryTimes int) bool {
	return evaluateTaskRelayRetry(c, channelId, taskErr, retryTimes).retry
}

func evaluateTaskRelayRetry(c *gin.Context, channelId int, taskErr *taskdto.TaskError, retryTimes int) relayRetryEvaluation {
	if taskErr == nil {
		return relayRetryEvaluation{reason: "no_error"}
	}
	if taskErr.Code == string(types.ErrorCodeChannelOverloaded) {
		return relayRetryEvaluation{reason: "same_channel_overload"}
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return relayRetryEvaluation{reason: "affinity_locked"}
	}
	if retryTimes <= 0 {
		return relayRetryEvaluation{reason: "budget_exhausted"}
	}
	if service.GetChannelConstraints(c).SuppressesRetry() {
		return relayRetryEvaluation{reason: "channel_pin_single_attempt"}
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return relayRetryEvaluation{retry: true, reason: "status_code_match"}
	}
	if taskErr.StatusCode == 307 {
		return relayRetryEvaluation{retry: true, reason: "status_code_match"}
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			return relayRetryEvaluation{reason: "always_skip_code"}
		}
		return relayRetryEvaluation{retry: true, reason: "status_code_match"}
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return relayRetryEvaluation{reason: "status_code_excluded"}
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return relayRetryEvaluation{reason: "always_skip_code"}
	}
	if taskErr.LocalError {
		return relayRetryEvaluation{reason: "task_local_error"}
	}
	if taskErr.StatusCode/100 == 2 {
		return relayRetryEvaluation{reason: "status_code_excluded"}
	}
	return relayRetryEvaluation{retry: true, reason: "channel_error"}
}
