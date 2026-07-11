package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"

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
		ws          *websocket.Conn
	)

	defer func() {
		if newAPIError != nil {
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
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

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest)
		}
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
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
			newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)
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
		Ctx:                  c,
		TokenGroup:           relayInfo.TokenGroup,
		ModelName:            relayInfo.OriginModelName,
		RequestPath:          c.Request.URL.Path,
		EstimatedInputTokens: estimatedInputTokensForRouting(relayFormat, relayInfo.RelayMode, tokens, meta, relayInfo.OriginModelName),
		AutoGroupIndex:       common.GetContextKeyInt(c, constant.ContextKeyAutoGroupIndex),
		AutoGroupSelected:    relayInfo.TokenGroup == "auto" && common.GetContextKeyString(c, constant.ContextKeyAutoGroup) != "",
	}
	interChannelRetryState := &service.InterChannelRetryState{}
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil
	globalRetryPolicy := relayGlobalRetryPolicy()

	for {
		relayInfo.RetryIndex = interChannelRetryState.Count()
		channel, channelErr := getChannel(c, relayInfo, selectParam)
		if channelErr != nil {
			logger.LogError(c, channelErr.Error())
			newAPIError = channelErr
			break
		}
		actualChannelID := 0
		channelRetryPolicy := relayRetryPolicy{}
		sameChannelRetryState := &service.SameChannelRetryState{}
		internalRetryBlockedByOverload := false

		for {
			internalRetry := layeredRetryEnabled && actualChannelID != 0
			selectedChannel, overloadLease, acquireErr := acquireRelayOverloadLease(c, relayInfo, selectParam, channel, internalRetry)
			if acquireErr != nil {
				if internalRetry && acquireErr.GetErrorCode() == types.ErrorCodeChannelOverloaded {
					// The channel became overloaded between its upstream attempts.
					// Preserve its last upstream error and let the outer loop decide
					// whether the independent cross-channel budget permits a switch.
					internalRetryBlockedByOverload = true
					break
				}
				newAPIError = acquireErr
				relayInfo.LastError = acquireErr
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
			releaseOverloadLease(overloadLease)
			service.ClearChannelOverloadLease(c)

			if newAPIError == nil {
				relayInfo.LastError = nil
				return
			}

			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			relayInfo.LastError = newAPIError

			processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError)

			if !shouldRetrySameChannelWithPolicy(c, newAPIError, channelRetryPolicy, sameChannelRetryState.Count()) {
				break
			}
			sameChannelRetryState.Increase()
			waitBeforeRelayRetry(c, channelRetryPolicy.retryDelay)
			channel, newAPIError = prepareMultiKeyChannelRetry(c, channel, relayInfo)
			if newAPIError != nil {
				relayInfo.LastError = newAPIError
				break
			}
		}

		if internalRetryBlockedByOverload {
			if shouldSwitchChannelAfterInternalRetryOverload(c, newAPIError, globalRetryPolicy, interChannelRetryState.Count()) {
				selectParam.ExcludeAttemptedChannel(actualChannelID)
				interChannelRetryState.Increase()
				continue
			}
			recordInternalRetryOverloadBlocked(c, channel, newAPIError, globalRetryPolicy, interChannelRetryState.Count())
			break
		}

		if !shouldRetryWithPolicy(c, newAPIError, globalRetryPolicy, interChannelRetryState.Count()) {
			break
		}
		if actualChannelID > 0 {
			selectParam.ExcludeAttemptedChannel(actualChannelID)
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
	_, specificChannel := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId)
	channelLocked = channelLocked || specificChannel
	for {
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
			setupErr := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
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

		selectParam.ExcludeUnavailableChannel(channel.Id)
		next, selectGroup, selectErr := service.CacheGetRandomSatisfiedChannel(selectParam)
		if selectErr != nil {
			return channel, nil, types.NewError(selectErr, types.ErrorCodeGetChannelFailed)
		}
		if next == nil {
			return channel, nil, channelOverloadedError()
		}
		service.ClearRequestChannelAffinitySelection(c)
		if setupErr := middleware.SetupContextForSelectedChannel(c, next, info.OriginModelName); setupErr != nil {
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
			if selectParam == nil || selectParam.EstimatedInputTokens == nil {
				return currentChannel, nil
			}
			if _, specificChannel := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId); !specificChannel {
				if !currentChannel.MatchesInputTokenRouting(selectParam.EstimatedInputTokens) {
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
	if newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName); newAPIError != nil {
		return channel, newAPIError
	}
	info.InitChannelMeta(c)
	return channel, nil
}

func selectChannelByInputTokenRouting(c *gin.Context, info *relaycommon.RelayInfo, selectParam *service.ChannelSelectParam) (*model.Channel, *types.NewAPIError) {
	channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(selectParam)
	if err == nil && channel == nil && selectParam.BeginNextSelectionSweep() {
		channel, selectGroup, err = service.CacheGetRandomSatisfiedChannel(selectParam)
		if err == nil && channel == nil {
			// A single eligible channel must be reusable after a sweep. With
			// multiple channels, keeping the last failure excluded avoids an
			// immediate repeat at the start of the next sweep.
			selectParam.AllowLastAttemptedChannel()
			channel, selectGroup, err = service.CacheGetRandomSatisfiedChannel(selectParam)
		}
	}

	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

	if err != nil {
		return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if channel == nil {
		return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
	if newAPIError != nil {
		return nil, newAPIError
	}
	return channel, nil
}

func estimatedInputTokensForRouting(relayFormat types.RelayFormat, relayMode int, tokens int, meta *types.TokenCountMeta, modelName string) *int {
	if !supportsInputTokenRouting(relayFormat, relayMode) {
		return nil
	}
	if tokens < 0 {
		tokens = 0
	}
	if routingTokens := conservativeInputTokensForRouting(relayFormat, meta, modelName); routingTokens > tokens {
		tokens = routingTokens
	}
	return &tokens
}

func conservativeInputTokensForRouting(relayFormat types.RelayFormat, meta *types.TokenCountMeta, modelName string) int {
	if meta == nil {
		return 0
	}

	tokens := 0
	if meta.CombineText != "" {
		textTokens := service.CountTextToken(meta.CombineText, modelName)
		byteTokens := (len(meta.CombineText) + 3) / 4
		if byteTokens > textTokens {
			textTokens = byteTokens
		}
		tokens += textTokens
	}
	if relayFormat == types.RelayFormatOpenAI {
		tokens += meta.ToolsCount * 8
		tokens += meta.MessagesCount * 3
		tokens += meta.NameCount * 3
		tokens += 3
	}
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
	if !policy.channelOverride {
		return false
	}
	return shouldRetryRelayErrorWithPolicy(c, openaiErr, policy, currentRetry, true, false)
}

func shouldRetryRelayErrorWithPolicy(c *gin.Context, openaiErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int, allowSpecificChannelRetry bool, respectAffinitySkip bool) bool {
	if openaiErr == nil {
		return false
	}
	if respectAffinitySkip && service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if types.IsSkipRetryError(openaiErr) {
		return false
	}
	if policy.retryTimes-currentRetry <= 0 {
		return false
	}
	if !allowSpecificChannelRetry {
		if _, ok := c.Get("specific_channel_id"); ok {
			return false
		}
	}
	if types.IsChannelError(openaiErr) {
		if allowSpecificChannelRetry && c.GetBool("layered_relay_retry") {
			return openaiErr.GetErrorCode() == types.ErrorCodeChannelResponseTimeExceeded
		}
		return true
	}
	code := openaiErr.StatusCode
	if code >= 200 && code < 300 {
		return false
	}
	if code < 100 || code > 599 {
		return true
	}
	if operation_setting.IsAlwaysSkipRetryCode(openaiErr.GetErrorCode()) {
		return false
	}
	return operation_setting.ShouldRetryByStatusCodeRanges(policy.statusCodeRanges, code)
}

func shouldSwitchChannelAfterInternalRetryOverload(c *gin.Context, lastUpstreamErr *types.NewAPIError, policy relayRetryPolicy, currentRetry int) bool {
	if lastUpstreamErr == nil || types.IsSkipRetryError(lastUpstreamErr) {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if _, specificChannel := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId); specificChannel {
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
	} else if _, specificChannel := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId); specificChannel {
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
	if !constant.ErrorLogEnabled || !types.IsRecordErrorLog(lastUpstreamErr) {
		return
	}
	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	model.RecordErrorLog(
		c,
		c.GetInt("id"),
		channel.Id,
		c.GetString("original_model"),
		c.GetString("token_name"),
		lastUpstreamErr.MaskSensitiveError(),
		c.GetInt("token_id"),
		int(time.Since(startTime).Seconds()),
		common.GetContextKeyBool(c, constant.ContextKeyIsStream),
		c.GetString("group"),
		buildRelayErrorLogDetails(c, lastUpstreamErr, channel.Id),
	)
}

func waitBeforeRelayRetry(c *gin.Context, delay time.Duration) {
	if delay <= 0 || c.Request == nil {
		return
	}
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
	case <-c.Request.Context().Done():
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError) {
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	if service.ShouldDisableChannel(err) && channelError.AutoBan {
		gopool.Go(func() {
			service.DisableChannel(channelError, err.ErrorWithStatusCode())
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := c.GetInt("channel_id")
		other := buildRelayErrorLogDetails(c, err, channelId)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.MaskSensitiveError(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}

}

func buildRelayErrorLogDetails(c *gin.Context, err *types.NewAPIError, channelId int) map[string]interface{} {
	other := make(map[string]interface{})
	if c.Request != nil && c.Request.URL != nil {
		other["request_path"] = c.Request.URL.Path
	}
	other["error_type"] = err.GetErrorType()
	other["error_code"] = err.GetErrorCode()
	other["status_code"] = err.StatusCode
	other["channel_id"] = channelId
	other["channel_name"] = c.GetString("channel_name")
	other["channel_type"] = c.GetInt("channel_type")

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
	service.AppendChannelAffinityAdminInfo(c, adminInfo)
	if marker, ok := c.Get("internal_retry_overload_blocked"); ok {
		adminInfo["internal_retry_overload_blocked"] = marker
	}
	other["admin_info"] = adminInfo
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

	var mjErr *dto.MidjourneyResponse
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
		if mjErr.Description == string(types.ErrorCodeChannelOverloaded) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"description": mjErr.Result,
				"type":        "new_api_error",
				"code":        types.ErrorCodeChannelOverloaded,
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

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
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

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}

	var result *relay.TaskSubmitResult
	var taskErr *dto.TaskError
	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	selectParam := &service.ChannelSelectParam{
		Ctx:               c,
		TokenGroup:        relayInfo.TokenGroup,
		ModelName:         relayInfo.OriginModelName,
		RequestPath:       c.Request.URL.Path,
		AutoGroupIndex:    common.GetContextKeyInt(c, constant.ContextKeyAutoGroupIndex),
		AutoGroupSelected: relayInfo.TokenGroup == "auto" && common.GetContextKeyString(c, constant.ContextKeyAutoGroup) != "",
	}
	interChannelRetryState := &service.InterChannelRetryState{}

	for interChannelRetryState.Count() <= common.RetryTimes {
		var channel *model.Channel
		var channelErr *types.NewAPIError

		if lockedCh, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedCh != nil {
			channel = lockedCh
			if interChannelRetryState.Count() > 0 {
				if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
					taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
					break
				}
			}
		} else {
			channel, channelErr = getChannel(c, relayInfo, selectParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				break
			}
		}

		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		var overloadLease *service.OverloadLease
		locked := relayInfo.LockedChannel != nil
		channel, overloadLease, channelErr = acquireRelayOverloadLease(c, relayInfo, selectParam, channel, locked)
		if channelErr != nil {
			taskErr = service.TaskErrorWrapperLocal(channelErr.Err, string(types.ErrorCodeChannelOverloaded), http.StatusServiceUnavailable)
			break
		}
		service.SetChannelOverloadLease(c, overloadLease)
		addUsedChannel(c, channel.Id)
		result, taskErr = relay.RelayTaskSubmit(c, relayInfo)
		releaseOverloadLease(overloadLease)
		service.ClearChannelOverloadLease(c)
		if taskErr == nil {
			break
		}

		if !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode))
		}

		if !shouldRetryTaskRelay(c, channel.Id, taskErr, common.RetryTimes-interChannelRetryState.Count()) {
			break
		}
		selectParam.ExcludeAttemptedChannel(channel.Id)
		interChannelRetryState.Increase()
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	// ── 成功：结算 + 日志 + 插入任务 ──
	if taskErr == nil {
		if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
			common.SysError("settle task billing error: " + settleErr.Error())
		}
		service.LogTaskConsumption(c, relayInfo)

		task := model.InitTask(result.Platform, relayInfo)
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
		}
		task.Quota = result.Quota
		task.Data = result.TaskData
		task.Action = relayInfo.Action
		if insertErr := task.Insert(); insertErr != nil {
			common.SysError("insert task error: " + insertErr.Error())
		}
	}

	if taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *dto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *dto.TaskError, retryTimes int) bool {
	if taskErr == nil {
		return false
	}
	if taskErr.Code == string(types.ErrorCodeChannelOverloaded) {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if taskErr.StatusCode == 307 {
		return true
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			return false
		}
		return true
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return false
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if taskErr.LocalError {
		return false
	}
	if taskErr.StatusCode/100 == 2 {
		return false
	}
	return true
}
