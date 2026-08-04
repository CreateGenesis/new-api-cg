package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

const (
	simulatedModelCacheKeyPrefix          = "simulated_model_cache:v5"
	SimulatedModelCacheFingerprintVersion = "v5"
	legacySimulatedModelCacheReplayDir    = "simulated-model-cache"
)

var cleanupLegacySimulatedModelCacheReplayOnce sync.Once

type SimulatedModelCacheUsageRewrite struct {
	Mode               string
	MatchRatio         float64
	FingerprintVersion string
	CandidateCount     int
	MatchDuration      time.Duration
}

type SimulatedModelCachePartialMatchRequest struct {
	ChannelID             int
	UserID                int
	Model                 string
	PromptText            string
	Prompt                SimulatedModelCachePrompt
	MinMatchRatio         float64
	TTLSeconds            int
	KeyDigest             string
	AllowedKeyDigests     map[string]struct{}
	currentMemoryReserved bool
}

type SimulatedModelCachePartialMatch struct {
	Found               bool
	MatchRatio          float64
	FingerprintVersion  string
	CandidateCount      int
	MatchDuration       time.Duration
	BypassReason        string
	PreferredKeyDigests []string
	prepared            *SimulatedModelCachePreparedFingerprint
}

func cleanupLegacySimulatedModelCacheReplayFiles() {
	cleanupLegacySimulatedModelCacheReplayOnce.Do(func() {
		_ = os.RemoveAll(filepath.Join(common.GetDiskCacheDir(), legacySimulatedModelCacheReplayDir))
	})
}

func PatchSimulatedModelCacheResponseBody(format types.RelayFormat, contentType string, body []byte, usage *dto.Usage, responseModel ...string) []byte {
	if len(body) == 0 || usage == nil {
		return body
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return patchSimulatedModelCacheSSEBody(format, body, usage, responseModel...)
	}
	patched, ok := patchSimulatedModelCacheJSONBody(format, body, usage, responseModel...)
	if !ok {
		return body
	}
	return patched
}

func patchSimulatedModelCacheSSEBody(format types.RelayFormat, body []byte, usage *dto.Usage, responseModel ...string) []byte {
	lines := strings.SplitAfter(string(body), "\n")
	for i, line := range lines {
		prefixLen := len(line) - len(strings.TrimLeft(line, " \t"))
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		patched, ok := patchSimulatedModelCacheJSONBody(format, []byte(data), usage, responseModel...)
		if !ok {
			continue
		}
		prefix := line[:prefixLen]
		lineEnding := ""
		if strings.HasSuffix(line, "\n") {
			lineEnding = "\n"
			if strings.HasSuffix(line, "\r\n") {
				lineEnding = "\r\n"
			}
		}
		lines[i] = prefix + "data: " + string(patched) + lineEnding
	}
	return []byte(strings.Join(lines, ""))
}

func patchSimulatedModelCacheJSONBody(format types.RelayFormat, body []byte, usage *dto.Usage, responseModel ...string) ([]byte, bool) {
	var payload map[string]any
	if common.Unmarshal(body, &payload) != nil {
		return nil, false
	}
	patched := false
	if model := simulatedModelCacheResponseModel(format, responseModel...); model != "" {
		if _, ok := payload["model"]; ok {
			payload["model"] = model
			patched = true
		}
		if responseAny, ok := payload["response"]; ok {
			if responseMap, ok := responseAny.(map[string]any); ok {
				if _, ok := responseMap["model"]; ok {
					responseMap["model"] = model
					patched = true
				}
			}
		}
	}
	if usageAny, ok := payload["usage"]; ok {
		if usageMap, ok := usageAny.(map[string]any); ok {
			switch format {
			case types.RelayFormatClaude:
				patchClaudeStyleUsageMap(usageMap, usage)
			default:
				patchOpenAIStyleUsageMap(usageMap, usage)
			}
			patched = true
		}
	}
	if metadataAny, ok := payload["usageMetadata"]; ok {
		if metadata, ok := metadataAny.(map[string]any); ok {
			patchGeminiUsageMetadataMap(metadata, usage)
			patched = true
		}
	}
	if !patched {
		return nil, false
	}
	out, err := common.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return out, true
}

func simulatedModelCacheResponseModel(format types.RelayFormat, responseModel ...string) string {
	if len(responseModel) == 0 || strings.TrimSpace(responseModel[0]) == "" {
		return ""
	}
	switch format {
	case types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		return responseModel[0]
	default:
		return ""
	}
}

func patchOpenAIStyleUsageMap(usageMap map[string]any, usage *dto.Usage) {
	normalized := NormalizeUsageForSemantic(usage, UsageSemanticOpenAI)
	usageMap["prompt_tokens"] = normalized.PromptTokens
	usageMap["completion_tokens"] = normalized.CompletionTokens
	usageMap["total_tokens"] = normalized.TotalTokens
	usageMap["input_tokens"] = normalized.InputTokens
	usageMap["output_tokens"] = normalized.OutputTokens

	promptDetails, _ := usageMap["prompt_tokens_details"].(map[string]any)
	if promptDetails == nil {
		promptDetails = map[string]any{}
		usageMap["prompt_tokens_details"] = promptDetails
	}
	promptDetails["cached_tokens"] = normalized.PromptTokensDetails.CachedTokens

	inputDetails, _ := usageMap["input_tokens_details"].(map[string]any)
	if inputDetails == nil {
		inputDetails = map[string]any{}
		usageMap["input_tokens_details"] = inputDetails
	}
	inputDetails["cached_tokens"] = normalized.PromptTokensDetails.CachedTokens
}

func patchClaudeStyleUsageMap(usageMap map[string]any, usage *dto.Usage) {
	normalized := NormalizeUsageForSemantic(usage, UsageSemanticAnthropic)
	usageMap["input_tokens"] = normalized.InputTokens
	usageMap["cache_read_input_tokens"] = normalized.PromptTokensDetails.CachedTokens
	usageMap["cache_creation_input_tokens"] = normalized.PromptTokensDetails.CachedCreationTokens
	usageMap["claude_cache_creation_5_m_tokens"] = normalized.ClaudeCacheCreation5mTokens
	usageMap["claude_cache_creation_1_h_tokens"] = normalized.ClaudeCacheCreation1hTokens
	usageMap["output_tokens"] = normalized.OutputTokens
}

func patchGeminiUsageMetadataMap(metadata map[string]any, usage *dto.Usage) {
	normalized := NormalizeUsageForSemantic(usage, UsageSemanticOpenAI)
	metadata["promptTokenCount"] = normalized.PromptTokens
	metadata["candidatesTokenCount"] = normalized.CompletionTokens
	metadata["totalTokenCount"] = normalized.TotalTokens
	metadata["cachedContentTokenCount"] = normalized.PromptTokensDetails.CachedTokens
}

// ApplySimulatedModelCacheMissingInputEstimate fills an upstream usage record
// only when it contains no input or cache tokens at all.
func ApplySimulatedModelCacheMissingInputEstimate(usage *dto.Usage, estimatedTokens int) bool {
	if usage == nil || estimatedTokens <= 0 {
		return false
	}
	if estimatedTokens > math.MaxInt32 {
		estimatedTokens = math.MaxInt32
	}

	reportedInput := NormalizeInputTokens(usage)
	if reportedInput.TotalInputTokens > 0 ||
		reportedInput.CacheReadInputTokens > 0 ||
		reportedInput.CacheCreationInputTokens > 0 {
		return false
	}
	normalized := NormalizeUsageForBilling(usage)
	if normalized.InputTokens.TotalInputTokens > 0 ||
		normalized.InputTokens.CacheReadInputTokens > 0 ||
		normalized.InputTokens.CacheCreationInputTokens > 0 {
		return false
	}

	usage.PromptTokens = estimatedTokens
	usage.InputTokens = estimatedTokens
	usage.TotalTokens = saturatingTokenAdd(estimatedTokens, normalized.OutputTokens)
	usage.Estimated = true

	if usage.BillingUsage == nil {
		return true
	}
	usage.BillingUsage.Estimated = true
	switch {
	case usage.BillingUsage.OpenAIUsage != nil:
		openAIUsage := usage.BillingUsage.OpenAIUsage
		openAIUsage.PromptTokens = estimatedTokens
		openAIUsage.InputTokens = estimatedTokens
		outputTokens := positiveTokenCount(openAIUsage.CompletionTokens)
		if openAIUsage.OutputTokens > outputTokens {
			outputTokens = openAIUsage.OutputTokens
		}
		openAIUsage.TotalTokens = saturatingTokenAdd(estimatedTokens, outputTokens)
		openAIUsage.Estimated = true
	case usage.BillingUsage.ClaudeUsage != nil:
		usage.BillingUsage.ClaudeUsage.InputTokens = estimatedTokens
	case usage.BillingUsage.GeminiUsageMetadata != nil:
		metadata := usage.BillingUsage.GeminiUsageMetadata
		metadata.PromptTokenCount = estimatedTokens
		outputTokens := saturatingTokenAdd(
			positiveTokenCount(metadata.CandidatesTokenCount),
			positiveTokenCount(metadata.ThoughtsTokenCount),
		)
		metadata.TotalTokenCount = saturatingTokenAdd(estimatedTokens, outputTokens)
	}
	return true
}

func ApplySimulatedModelCacheUsageRewrite(usage *dto.Usage, rewrite SimulatedModelCacheUsageRewrite) *relaycommon.SimulatedModelCacheInfo {
	if usage == nil {
		return nil
	}
	ratio := rewrite.MatchRatio
	if ratio < 0 || math.IsNaN(ratio) {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}

	originalPromptTokens, cachedTokens, simulatedPromptTokens := applySimulatedModelCacheRatioToUsage(usage, ratio)
	applySimulatedModelCacheRatioToBillingUsage(usage.BillingUsage, ratio)

	return &relaycommon.SimulatedModelCacheInfo{
		Mode:                  rewrite.Mode,
		MatchRatio:            ratio,
		OriginalPromptTokens:  originalPromptTokens,
		SimulatedPromptTokens: simulatedPromptTokens,
		SimulatedCachedTokens: cachedTokens,
		FingerprintVersion:    rewrite.FingerprintVersion,
		CandidateCount:        rewrite.CandidateCount,
		MatchDurationMS:       rewrite.MatchDuration.Milliseconds(),
	}
}

func applySimulatedModelCacheRatioToUsage(usage *dto.Usage, ratio float64) (int, int, int) {
	if usage == nil {
		return 0, 0, 0
	}
	originalPromptTokens := NormalizeInputTokens(usage).TotalInputTokens
	cachedTokens, _ := common.QuotaFromDecimalChecked(
		decimal.NewFromInt(int64(originalPromptTokens)).Mul(decimal.NewFromFloat(ratio)).Ceil(),
	)
	if cachedTokens > originalPromptTokens {
		cachedTokens = originalPromptTokens
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	simulatedPromptTokens := originalPromptTokens - cachedTokens

	usage.PromptTokensDetails.CachedCreationTokens = 0
	usage.PromptTokensDetails.CacheWriteTokens = 0
	usage.PromptTokensDetails.CachedTokens = cachedTokens
	if usage.InputTokensDetails == nil {
		usage.InputTokensDetails = &dto.InputTokenDetails{}
	}
	usage.InputTokensDetails.CachedCreationTokens = 0
	usage.InputTokensDetails.CacheWriteTokens = 0
	usage.ClaudeCacheCreation5mTokens = 0
	usage.ClaudeCacheCreation1hTokens = 0
	if usage.UsageSemantic == UsageSemanticAnthropic {
		usage.PromptTokens = simulatedPromptTokens
		usage.InputTokens = simulatedPromptTokens
	} else {
		usage.UsageSemantic = UsageSemanticOpenAI
		usage.PromptTokens = originalPromptTokens
		usage.InputTokens = originalPromptTokens
	}
	usage.InputTokensDetails.CachedTokens = cachedTokens
	usage.TotalTokens = saturatingTokenAdd(originalPromptTokens, usage.CompletionTokens)
	if usage.OutputTokens == 0 && usage.CompletionTokens > 0 {
		usage.OutputTokens = usage.CompletionTokens
	}
	return originalPromptTokens, cachedTokens, simulatedPromptTokens
}

func applySimulatedModelCacheRatioToBillingUsage(billingUsage *dto.BillingUsage, ratio float64) {
	if billingUsage == nil {
		return
	}
	wrapper := &dto.Usage{BillingUsage: dto.CloneBillingUsage(billingUsage)}
	normalized, ok := usageFromBillingUsage(wrapper)
	if !ok {
		return
	}
	applySimulatedModelCacheRatioToUsage(normalized, ratio)
	semantic := strings.ToLower(strings.TrimSpace(billingUsage.Semantic))
	source := strings.ToLower(strings.TrimSpace(billingUsage.Source))
	switch {
	case billingUsage.OpenAIUsage != nil && (semantic == dto.BillingUsageSemanticOpenAI || source == dto.BillingUsageSourceOAIChat || source == dto.BillingUsageSourceOAIResponses):
		rewritten := NormalizeUsageForSemantic(normalized, UsageSemanticOpenAI)
		rewritten.BillingUsage = nil
		*billingUsage.OpenAIUsage = rewritten
	case billingUsage.ClaudeUsage != nil && (semantic == dto.BillingUsageSemanticAnthropic || source == dto.BillingUsageSourceClaudeMessages):
		rewritten := NormalizeUsageForSemantic(normalized, UsageSemanticAnthropic)
		billingUsage.ClaudeUsage.InputTokens = rewritten.PromptTokens
		billingUsage.ClaudeUsage.OutputTokens = rewritten.CompletionTokens
		billingUsage.ClaudeUsage.CacheReadInputTokens = rewritten.PromptTokensDetails.CachedTokens
		billingUsage.ClaudeUsage.CacheCreationInputTokens = 0
		billingUsage.ClaudeUsage.CacheCreation = nil
		billingUsage.ClaudeUsage.ClaudeCacheCreation5mTokens = 0
		billingUsage.ClaudeUsage.ClaudeCacheCreation1hTokens = 0
	case billingUsage.GeminiUsageMetadata != nil && (semantic == dto.BillingUsageSemanticGemini || source == dto.BillingUsageSourceGeminiChat):
		rewritten := NormalizeUsageForSemantic(normalized, UsageSemanticOpenAI)
		metadata := billingUsage.GeminiUsageMetadata
		metadata.PromptTokenCount = rewritten.PromptTokens
		metadata.PromptTokensDetails = append(metadata.PromptTokensDetails, metadata.ToolUsePromptTokensDetails...)
		metadata.ToolUsePromptTokenCount = 0
		metadata.ToolUsePromptTokensDetails = nil
		metadata.CachedContentTokenCount = rewritten.PromptTokensDetails.CachedTokens
		metadata.CandidatesTokenCount = rewritten.CompletionTokens - metadata.ThoughtsTokenCount
		if metadata.CandidatesTokenCount < 0 {
			metadata.CandidatesTokenCount = 0
		}
		metadata.TotalTokenCount = rewritten.TotalTokens
	}
}

func ExtractSimulatedModelCachePromptText(format types.RelayFormat, body []byte) string {
	var texts []string
	switch format {
	case types.RelayFormatOpenAI:
		var req dto.GeneralOpenAIRequest
		if common.Unmarshal(body, &req) != nil {
			return ""
		}
		for _, message := range req.Messages {
			appendPromptText(&texts, message.StringContent())
		}
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		var req dto.OpenAIResponsesRequest
		if common.Unmarshal(body, &req) != nil {
			return ""
		}
		appendRawPromptText(&texts, req.Instructions)
		for _, input := range req.ParseInput() {
			appendPromptText(&texts, input.Text)
		}
	case types.RelayFormatClaude:
		var req dto.ClaudeRequest
		if common.Unmarshal(body, &req) != nil {
			return ""
		}
		if req.System != nil {
			if req.IsStringSystem() {
				appendPromptText(&texts, req.GetStringSystem())
			} else {
				for _, content := range req.ParseSystem() {
					if content.Type == dto.ContentTypeText {
						appendPromptText(&texts, content.GetText())
					}
				}
			}
		}
		for _, message := range req.Messages {
			if message.IsStringContent() {
				appendPromptText(&texts, message.GetStringContent())
				continue
			}
			contents, err := message.ParseContent()
			if err != nil {
				continue
			}
			for _, content := range contents {
				if content.Type == dto.ContentTypeText {
					appendPromptText(&texts, content.GetText())
				}
			}
		}
	case types.RelayFormatGemini:
		var req dto.GeminiChatRequest
		if common.Unmarshal(body, &req) != nil {
			return ""
		}
		if req.SystemInstructions != nil {
			appendGeminiPartsPromptText(&texts, req.SystemInstructions.Parts)
		}
		for _, content := range req.Contents {
			appendGeminiPartsPromptText(&texts, content.Parts)
		}
	}
	return strings.Join(texts, "\n")
}

func appendPromptText(texts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text != "" {
		*texts = append(*texts, text)
	}
}

func appendRawPromptText(texts *[]string, raw []byte) {
	if len(raw) == 0 {
		return
	}
	if common.GetJsonType(raw) == "string" {
		var text string
		if common.Unmarshal(raw, &text) == nil {
			appendPromptText(texts, text)
			return
		}
	}
	appendPromptText(texts, string(raw))
}

func appendGeminiPartsPromptText(texts *[]string, parts []dto.GeminiPart) {
	for _, part := range parts {
		appendPromptText(texts, part.Text)
	}
}

func ttlFromSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 86400
	}
	return time.Duration(seconds) * time.Second
}

func simulatedModelCacheScopeIndexKey(channelID int, userID int, model string, scopeDigests ...string) string {
	scope := "text"
	if len(scopeDigests) > 0 && scopeDigests[0] != "" {
		scope = scopeDigests[0]
	}
	return fmt.Sprintf("%s:scope_index:%d:%d:%s:%s",
		simulatedModelCacheKeyPrefix,
		channelID,
		userID,
		sha256Hex([]byte(model)),
		sha256Hex([]byte(scope)),
	)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
