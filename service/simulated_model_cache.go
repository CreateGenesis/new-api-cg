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
)

const (
	simulatedModelCacheKeyPrefix          = "simulated_model_cache:v3"
	SimulatedModelCacheFingerprintVersion = "v3"
	SimulatedModelCacheMinInputTokens     = 512
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
	UserID                int
	Model                 string
	PromptText            string
	MinMatchRatio         float64
	TTLSeconds            int
	currentMemoryReserved bool
}

type SimulatedModelCachePartialMatch struct {
	Found              bool
	MatchRatio         float64
	FingerprintVersion string
	CandidateCount     int
	MatchDuration      time.Duration
	BypassReason       string
	prepared           *SimulatedModelCachePreparedFingerprint
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
			patchOpenAIStyleUsageMap(usageMap, usage)
			if format == types.RelayFormatClaude {
				patchClaudeStyleUsageMap(usageMap, usage)
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
	usageMap["prompt_tokens"] = usage.PromptTokens
	usageMap["completion_tokens"] = usage.CompletionTokens
	usageMap["total_tokens"] = usage.TotalTokens
	usageMap["input_tokens"] = usage.InputTokens
	usageMap["output_tokens"] = usage.OutputTokens

	promptDetails, _ := usageMap["prompt_tokens_details"].(map[string]any)
	if promptDetails == nil {
		promptDetails = map[string]any{}
		usageMap["prompt_tokens_details"] = promptDetails
	}
	promptDetails["cached_tokens"] = usage.PromptTokensDetails.CachedTokens

	inputDetails, _ := usageMap["input_tokens_details"].(map[string]any)
	if inputDetails == nil {
		inputDetails = map[string]any{}
		usageMap["input_tokens_details"] = inputDetails
	}
	inputDetails["cached_tokens"] = usage.PromptTokensDetails.CachedTokens
}

func patchClaudeStyleUsageMap(usageMap map[string]any, usage *dto.Usage) {
	usageMap["input_tokens"] = usage.PromptTokens
	usageMap["cache_read_input_tokens"] = usage.PromptTokensDetails.CachedTokens
	usageMap["output_tokens"] = usage.CompletionTokens
	if _, ok := usageMap["cache_creation_input_tokens"]; !ok {
		usageMap["cache_creation_input_tokens"] = usage.PromptTokensDetails.CachedCreationTokens
	}
	if _, ok := usageMap["claude_cache_creation_5_m_tokens"]; !ok {
		usageMap["claude_cache_creation_5_m_tokens"] = usage.ClaudeCacheCreation5mTokens
	}
	if _, ok := usageMap["claude_cache_creation_1_h_tokens"]; !ok {
		usageMap["claude_cache_creation_1_h_tokens"] = usage.ClaudeCacheCreation1hTokens
	}
}

func patchGeminiUsageMetadataMap(metadata map[string]any, usage *dto.Usage) {
	metadata["promptTokenCount"] = usage.PromptTokens
	metadata["candidatesTokenCount"] = usage.CompletionTokens
	metadata["totalTokenCount"] = usage.TotalTokens
	metadata["cachedContentTokenCount"] = usage.PromptTokensDetails.CachedTokens
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

	originalPromptTokens := usage.PromptTokens
	if originalPromptTokens == 0 && usage.InputTokens > 0 {
		originalPromptTokens = usage.InputTokens
		usage.PromptTokens = usage.InputTokens
	}
	cachedTokens := int(math.Ceil(float64(originalPromptTokens) * ratio))
	if cachedTokens > originalPromptTokens {
		cachedTokens = originalPromptTokens
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	simulatedPromptTokens := originalPromptTokens - cachedTokens

	usage.PromptTokensDetails.CachedTokens = cachedTokens
	if usage.InputTokensDetails == nil {
		usage.InputTokensDetails = &dto.InputTokenDetails{}
	}
	if usage.UsageSemantic == "anthropic" {
		usage.PromptTokens = simulatedPromptTokens
		usage.InputTokens = simulatedPromptTokens
	} else {
		usage.InputTokens = originalPromptTokens
	}
	usage.InputTokensDetails.CachedTokens = cachedTokens
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if usage.OutputTokens == 0 && usage.CompletionTokens > 0 {
		usage.OutputTokens = usage.CompletionTokens
	}

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

func simulatedModelCacheScopeIndexKey(userID int, model string) string {
	return fmt.Sprintf("%s:scope_index:%d:%s",
		simulatedModelCacheKeyPrefix,
		userID,
		sha256Hex([]byte(model)),
	)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
