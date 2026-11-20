package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	cacheWrite5mHeader = "Msh-Usage-Cache-Write-Tokens-5m"
	cacheWrite1hHeader = "Msh-Usage-Cache-Write-Tokens-1h"
)

func simulatedCacheWriteEnabled(info *relaycommon.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil || info.ChannelOtherSettings.SimulatedModelCache == nil ||
		!info.ChannelOtherSettings.SimulatedModelCache.WriteBilling {
		return false
	}
	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		return info.RelayMode == relayconstant.RelayModeChatCompletions || info.RelayMode == relayconstant.RelayModeResponses
	case types.RelayFormatOpenAIResponses:
		return info.RelayMode == relayconstant.RelayModeResponses && info.ClientWs == nil
	case types.RelayFormatClaude:
		return true
	}
	return false
}

func nativeMoonshotCache(info *relaycommon.RelayInfo) bool {
	target, _ := url.Parse(info.ChannelBaseUrl)
	if target == nil || target.Hostname() == "" {
		return info.ChannelType == constant.ChannelTypeMoonshot
	}
	switch target.Hostname() {
	case "api.moonshot.ai", "api.moonshot.cn", "api.kimi.ai", "api.kimi.com":
		return true
	}
	return false
}

func removeLocalCacheOptions(info *relaycommon.RelayInfo, body []byte) ([]byte, error) {
	if !simulatedCacheWriteEnabled(info) || nativeMoonshotCache(info) || !gjson.GetBytes(body, "prompt_cache_options").Exists() {
		return body, nil
	}
	return sjson.DeleteBytes(body, "prompt_cache_options")
}

func simulatedCacheWriteTTL(info *relaycommon.RelayInfo) (int, *types.NewAPIError) {
	var raw json.RawMessage
	field, discriminator, expected := "prompt_cache_options", "mode", "implicit"
	switch request := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		raw = request.PromptCacheOptions
	case *dto.OpenAIResponsesRequest:
		raw = request.PromptCacheOptions
	case *dto.ClaudeRequest:
		field, discriminator, expected = "cache_control", "type", "ephemeral"
		raw = request.CacheControl
	}
	if len(raw) == 0 {
		return 300, nil
	}
	var options map[string]json.RawMessage
	if common.Unmarshal(raw, &options) == nil && options != nil {
		var mode, ttl string
		if value, exists := options[discriminator]; !exists || common.Unmarshal(value, &mode) == nil && mode == expected {
			if value, exists := options["ttl"]; !exists {
				return 300, nil
			} else if common.Unmarshal(value, &ttl) == nil {
				switch ttl {
				case "5m":
					return 300, nil
				case "1h":
					return 3600, nil
				}
			}
		}
	}
	return 0, types.NewErrorWithStatusCode(fmt.Errorf("%s must use %s=%q and a ttl of 5m or 1h", field, discriminator, expected),
		types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

func (w *channelOutputRecorder) settleCacheWrite(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, payload []byte) *dto.Usage {
	if usage == nil || c.Request.Context().Err() != nil || w.status != http.StatusOK ||
		(info.StreamStatus != nil && (info.StreamStatus.IsClientGone() || info.StreamStatus.IsInterrupted())) {
		return nil
	}
	if outcome := info.StreamStatus.ResponseOutcome(); outcome != "" && outcome != string(relaycommon.ResponseOutcomeCompleted) {
		return nil
	}
	if !w.stream {
		response := gjson.ParseBytes(payload)
		status := response.Get("status").String()
		if !gjson.ValidBytes(payload) || !response.IsObject() || response.Get("error").IsObject() ||
			w.format == types.RelayFormatOpenAIResponses && status != "" && status != "completed" {
			return nil
		}
	}
	write := w.cacheWrite
	audit := map[string]any{"source": "upstream", "ttl": write.TTL()}
	info.SimulatedCacheWriteAudit = audit
	source := service.SimulatedCacheWriteBaseUsage(info, usage)
	input := service.NormalizeUsageForBilling(source).InputTokens
	if input.TotalInputTokens <= 0 || input.CacheReadInputTokens > input.TotalInputTokens ||
		input.CacheCreationInputTokens > input.TotalInputTokens-input.CacheReadInputTokens {
		audit["bypass_reason"] = "invalid_upstream_input"
		return nil
	}
	view := service.SimulatedCacheWriteUsage(source, write, false)
	upstreamTTL := gjson.GetBytes(payload, "prompt_cache_options.ttl").String()
	if w.stream {
		for _, event := range splitSimulatedModelCacheSSEChunks(payload) {
			if body, ok := simulatedModelCacheSSEData(event); ok {
				if ttl := gjson.GetBytes(body, "response.prompt_cache_options.ttl").String(); ttl != "" {
					upstreamTTL = ttl
				}
			}
		}
	}
	availableInput := input.TotalInputTokens - input.CacheReadInputTokens
	write5m, err5m := strconv.Atoi(w.upstreamCacheHeaders.Get(cacheWrite5mHeader))
	write1h, err1h := strconv.Atoi(w.upstreamCacheHeaders.Get(cacheWrite1hHeader))
	headersValid := err5m == nil && err1h == nil && write5m >= 0 && write1h >= 0 &&
		write5m <= availableInput && write1h <= availableInput-write5m
	if input.CacheCreationInputTokens > 0 || headersValid || upstreamTTL == "5m" || upstreamTTL == "1h" || nativeMoonshotCache(info) {
		// Existing upstream writes retain precedence, even for a matching
		// local prefix. The transport applies the actual upstream split.
		total := input.CacheCreationInputTokens
		if total == 0 && headersValid {
			total = write5m + write1h
		}
		if headersValid && write5m == total-write1h {
			view.ClaudeCacheCreation5mTokens = write5m
			view.ClaudeCacheCreation1hTokens = write1h
		} else if upstreamTTL == "1h" && input.CacheCreation5mInputTokens == 0 && input.CacheCreation1hInputTokens == 0 {
			view.ClaudeCacheCreation1hTokens = total
		}
		if upstreamTTL == "1h" || total > 0 && view.ClaudeCacheCreation1hTokens == total {
			write.SetTTL(3600)
		} else if upstreamTTL == "5m" || total > 0 {
			write.SetTTL(300)
		}
		audit["ttl"] = write.TTL()
		if total != input.CacheCreationInputTokens || view.ClaudeCacheCreation1hTokens != input.CacheCreation1hInputTokens {
			view.PromptTokensDetails.CacheWriteTokens = total
			view.PromptTokensDetails.CachedCreationTokens = total
			*view.InputTokensDetails = view.PromptTokensDetails
			*usage = *view
		}
		return view
	}
	if usage.EstimatedInput || usage.Estimated || source.EstimatedInput || source.Estimated || !w.inputUsageObserved && !usage.UpstreamInputReported {
		audit["bypass_reason"] = "missing_upstream_input"
		return view
	}
	if info.PriceData.UsePrice || info.PriceData.FreeModel {
		audit["bypass_reason"] = "fixed_or_free_pricing"
		return view
	}
	if w.format == types.RelayFormatOpenAI &&
		(source.PromptTokensDetails.AudioTokens > 0 || source.CompletionTokenDetails.AudioTokens > 0) &&
		(ratio_setting.ContainsAudioRatio(info.OriginModelName) || ratio_setting.ContainsAudioCompletionRatio(info.OriginModelName)) {
		audit["bypass_reason"] = "separate_audio_pricing"
		return view
	}
	reason, err := write.Claim(c.Request.Context(), info, input)
	audit["ttl"] = write.TTL()
	if reason != "" {
		audit["bypass_reason"] = reason
		if err != nil {
			logger.LogWarn(c, common.LogText("simulated cache write claim failed: %s", err.Error()))
		}
		return view
	}
	countHits := info.ChannelOtherSettings.SimulatedModelCache.CountWriteCacheHits
	simulated := service.SimulatedCacheWriteUsage(source, write, countHits)
	additionalReads := simulated.PromptTokensDetails.CachedTokens - view.PromptTokensDetails.CachedTokens
	if write.Write == 0 && additionalReads <= 0 {
		return view
	}
	audit["source"] = "simulated"
	attempted, settled := service.PostSimulatedCacheWriteQuota(c, info, simulated)
	if err := write.Confirm(c.Request.Context(), settled, !attempted); err != nil {
		logger.LogWarn(c, common.LogText("simulated cache write confirmation failed: %s", err.Error()))
	}
	if !attempted {
		audit["source"] = "upstream"
		audit["bypass_reason"] = "pricing_or_reservation"
	}
	if !settled {
		return view
	}
	*usage = *simulated
	return simulated
}

func cacheWrite5m(usage *dto.Usage) int {
	return max(usage.PromptTokensDetails.CacheWriteTokens-usage.ClaudeCacheCreation1hTokens-usage.PromptTokensDetails.CacheWrite12hTokens(), 0)
}

func patchCacheWritePayload(format types.RelayFormat, body []byte, usage *dto.Usage, ttl string) []byte {
	if usage == nil || !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return body
	}
	var fields map[string]any
	input := usage.PromptTokens
	switch format {
	case types.RelayFormatOpenAI:
		fields = map[string]any{
			"usage.prompt_tokens":                            input,
			"usage.completion_tokens":                        usage.CompletionTokens,
			"usage.total_tokens":                             usage.TotalTokens,
			"usage.prompt_tokens_details.cached_tokens":      usage.PromptTokensDetails.CachedTokens,
			"usage.prompt_tokens_details.cache_write_tokens": usage.PromptTokensDetails.CacheWriteTokens,
		}
	case types.RelayFormatOpenAIResponses:
		prefix := ""
		if gjson.GetBytes(body, "response").IsObject() {
			prefix = "response."
		}
		fields = map[string]any{
			prefix + "usage.input_tokens":                            input,
			prefix + "usage.output_tokens":                           usage.CompletionTokens,
			prefix + "usage.total_tokens":                            usage.TotalTokens,
			prefix + "usage.input_tokens_details.cached_tokens":      usage.PromptTokensDetails.CachedTokens,
			prefix + "usage.input_tokens_details.cache_write_tokens": usage.PromptTokensDetails.CacheWriteTokens,
		}
		if !gjson.GetBytes(body, prefix+"prompt_cache_options").IsObject() {
			fields[prefix+"prompt_cache_options.mode"] = "implicit"
			fields[prefix+"prompt_cache_options.ttl"] = ttl
		}
	case types.RelayFormatClaude:
		fields = map[string]any{
			"usage.input_tokens":                             max(input-usage.PromptTokensDetails.CachedTokens-usage.PromptTokensDetails.CacheWriteTokens, 0),
			"usage.output_tokens":                            usage.CompletionTokens,
			"usage.cache_read_input_tokens":                  usage.PromptTokensDetails.CachedTokens,
			"usage.cache_creation_input_tokens":              usage.PromptTokensDetails.CacheWriteTokens,
			"usage.cache_creation.ephemeral_5m_input_tokens": cacheWrite5m(usage),
			"usage.cache_creation.ephemeral_1h_input_tokens": usage.ClaudeCacheCreation1hTokens,
		}
	}
	patched := body
	for path, value := range fields {
		next, err := sjson.SetBytes(patched, path, value)
		if err != nil {
			return body
		}
		patched = next
	}
	return patched
}

func patchCacheWriteStreamTail(format types.RelayFormat, tail []byte, usage *dto.Usage, ttl string, includeUsage bool) []byte {
	if format == types.RelayFormatOpenAI && !includeUsage {
		return tail
	}
	var output strings.Builder
	usageWritten := false
	for _, event := range splitSimulatedModelCacheSSEChunks(tail) {
		switch simulatedModelCacheStreamEventType(format, event) {
		case simulatedModelCacheStreamEventClaudeDelta, simulatedModelCacheStreamEventOpenAIUsage, simulatedModelCacheStreamEventResponsesTerminal:
			if body, ok := simulatedModelCacheSSEData(event); ok {
				event = replaceSimulatedModelCacheSSEData(event, patchCacheWritePayload(format, body, usage, ttl))
				usageWritten = true
			}
		case simulatedModelCacheStreamEventOpenAIDone:
			if !usageWritten {
				eventWithUsage, err := simulatedModelCacheOpenAIUsageEvent(usage, event)
				if err == nil {
					if body, ok := simulatedModelCacheSSEData(eventWithUsage); ok {
						output.Write(replaceSimulatedModelCacheSSEData(eventWithUsage, patchCacheWritePayload(format, body, usage, ttl)))
						usageWritten = true
					}
				}
			}
		}
		output.Write(event)
	}
	return []byte(output.String())
}
