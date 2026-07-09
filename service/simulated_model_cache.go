package service

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/go-redis/redis/v8"
)

const (
	simulatedModelCacheKeyPrefix              = "simulated_model_cache:v1"
	simulatedModelCacheMaxStored              = 2
	simulatedModelCacheMaxEntriesPerScope     = 100
	simulatedModelCacheMaxPartialMatchEntries = 256
	simulatedModelCacheMaxPartialMatchRunes   = 250000
	simulatedModelCacheDiskBodyDirName        = "simulated-model-cache"
	simulatedModelCacheBodyCompressionGzip    = "gzip"
)

var simulatedModelCacheDiskCleanupMu sync.Mutex
var simulatedModelCacheLastDiskCleanup time.Time

type SimulatedModelCacheUsageRewrite struct {
	Mode        string
	MatchRatio  float64
	ReplayCount int
}

type SimulatedModelCacheLookupRequest struct {
	UserID             int
	ChannelID          int
	UpstreamModel      string
	FinalRequestFormat types.RelayFormat
	RequestBody        []byte
	ReuseLimit         int
	TTLSeconds         int
}

type SimulatedModelCacheReplay struct {
	Found               bool
	Response            SimulatedModelCacheResponse
	ReplayCount         int
	LoadDurationSeconds float64
}

type SimulatedModelCacheStoreRequest struct {
	UserID             int
	ChannelID          int
	UpstreamModel      string
	FinalRequestFormat types.RelayFormat
	RequestBody        []byte
	PromptText         string
	Response           SimulatedModelCacheResponse
	TTLSeconds         int
}

type SimulatedModelCachePartialMatchRequest struct {
	UserID        int
	ChannelID     int
	UpstreamModel string
	PromptText    string
	MinMatchRatio float64
}

type SimulatedModelCachePartialMatch struct {
	Found      bool
	MatchRatio float64
	PromptText string
}

type simulatedModelCacheExactEntry struct {
	UserID        int                           `json:"user_id,omitempty"`
	ChannelID     int                           `json:"channel_id,omitempty"`
	UpstreamModel string                        `json:"upstream_model,omitempty"`
	PromptText    string                        `json:"prompt_text,omitempty"`
	RequestBody   []byte                        `json:"request_body,omitempty"`
	ReplayCount   int                           `json:"replay_count,omitempty"`
	UpdatedAt     int64                         `json:"updated_at,omitempty"`
	Responses     []SimulatedModelCacheResponse `json:"responses,omitempty"`
}

type SimulatedModelCacheResponse struct {
	StatusCode               int                             `json:"status_code,omitempty"`
	Headers                  map[string]string               `json:"headers,omitempty"`
	ContentType              string                          `json:"content_type,omitempty"`
	Body                     []byte                          `json:"body,omitempty"`
	BodyStorage              *SimulatedModelCacheBodyStorage `json:"body_storage,omitempty"`
	Usage                    dto.Usage                       `json:"usage,omitempty"`
	DurationSeconds          float64                         `json:"duration_seconds,omitempty"`
	StreamDelaysMS           []int64                         `json:"stream_delays_ms,omitempty"`
	ReplayPreparationSeconds float64                         `json:"-"`
}

type simulatedModelCacheResponse = SimulatedModelCacheResponse

type SimulatedModelCacheBodyStorage struct {
	Path           string `json:"path,omitempty"`
	Compression    string `json:"compression,omitempty"`
	Size           int    `json:"size,omitempty"`
	CompressedSize int64  `json:"compressed_size,omitempty"`
}

func (e *simulatedModelCacheExactEntry) appendResponse(response SimulatedModelCacheResponse) []SimulatedModelCacheResponse {
	e.Responses = append(e.Responses, response)
	if len(e.Responses) > simulatedModelCacheMaxStored {
		removed := append([]SimulatedModelCacheResponse(nil), e.Responses[:len(e.Responses)-simulatedModelCacheMaxStored]...)
		e.Responses = e.Responses[len(e.Responses)-simulatedModelCacheMaxStored:]
		return removed
	}
	return nil
}

func (e *simulatedModelCacheExactEntry) storeFreshResponse(response SimulatedModelCacheResponse) []SimulatedModelCacheResponse {
	e.ReplayCount = 0
	return e.appendResponse(response)
}

func (e simulatedModelCacheExactEntry) pickResponse(rng *rand.Rand) (SimulatedModelCacheResponse, bool) {
	if len(e.Responses) == 0 {
		return SimulatedModelCacheResponse{}, false
	}
	if len(e.Responses) == 1 {
		return e.Responses[0], true
	}
	if rng == nil {
		return e.Responses[rand.Intn(len(e.Responses))], true
	}
	return e.Responses[rng.Intn(len(e.Responses))], true
}

func storeSimulatedModelCacheResponseDiskBody(response *SimulatedModelCacheResponse) error {
	if response == nil || len(response.Body) == 0 {
		return nil
	}
	dir := simulatedModelCacheDiskBodyDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	filename := fmt.Sprintf("%d-%d-%s.gz", time.Now().UnixNano(), rand.Int63(), sha256Hex(response.Body)[:16])
	path := filepath.Join(dir, filename)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	gzipWriter := gzip.NewWriter(file)
	if _, err = gzipWriter.Write(response.Body); err != nil {
		_ = gzipWriter.Close()
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err = gzipWriter.Close(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	response.BodyStorage = &SimulatedModelCacheBodyStorage{
		Path:           path,
		Compression:    simulatedModelCacheBodyCompressionGzip,
		Size:           len(response.Body),
		CompressedSize: info.Size(),
	}
	response.Body = nil
	common.IncrementDiskFiles(info.Size())
	return nil
}

func loadSimulatedModelCacheResponseBody(response *SimulatedModelCacheResponse) error {
	if response == nil || len(response.Body) > 0 {
		return nil
	}
	if response.BodyStorage == nil || response.BodyStorage.Path == "" {
		return fmt.Errorf("simulated model cache response body is missing")
	}
	if response.BodyStorage.Compression != simulatedModelCacheBodyCompressionGzip {
		return fmt.Errorf("unsupported simulated model cache body compression: %s", response.BodyStorage.Compression)
	}
	if !isSimulatedModelCacheDiskBodyPath(response.BodyStorage.Path) {
		return fmt.Errorf("invalid simulated model cache body path")
	}
	file, err := os.Open(response.BodyStorage.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	body, err := io.ReadAll(gzipReader)
	if err != nil {
		return err
	}
	if response.BodyStorage.Size > 0 && len(body) != response.BodyStorage.Size {
		return fmt.Errorf("simulated model cache body size mismatch")
	}
	response.Body = body
	return nil
}

func deleteSimulatedModelCacheResponseDiskBody(response SimulatedModelCacheResponse) {
	if response.BodyStorage == nil || response.BodyStorage.Path == "" {
		return
	}
	if !isSimulatedModelCacheDiskBodyPath(response.BodyStorage.Path) {
		return
	}
	info, statErr := os.Stat(response.BodyStorage.Path)
	if err := os.Remove(response.BodyStorage.Path); err == nil && statErr == nil {
		common.DecrementDiskFiles(info.Size())
	}
}

func deleteSimulatedModelCacheResponseDiskBodies(responses []SimulatedModelCacheResponse) {
	for _, response := range responses {
		deleteSimulatedModelCacheResponseDiskBody(response)
	}
}

func simulatedModelCacheDiskBodyDir() string {
	return filepath.Join(common.GetDiskCacheDir(), simulatedModelCacheDiskBodyDirName)
}

func isSimulatedModelCacheDiskBodyPath(path string) bool {
	if path == "" {
		return false
	}
	absDir, err := filepath.Abs(simulatedModelCacheDiskBodyDir())
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	return true
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
	if rewrite.Mode == "exact_replay" {
		ratio = 1
	}
	if ratio < 0 {
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
		ReplayCount:           rewrite.ReplayCount,
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

func SimulatedModelCacheMatchRatio(cachedPrompt string, currentPrompt string) float64 {
	currentRunes := []rune(currentPrompt)
	if len(currentRunes) == 0 {
		return 0
	}
	lcs := longestCommonSubstringLength([]rune(cachedPrompt), currentRunes)
	return float64(lcs) / float64(len(currentRunes))
}

func simulatedModelCachePartialMatchRatio(cachedPrompt string, currentPrompt string) (float64, bool) {
	cachedRunes := []rune(cachedPrompt)
	currentRunes := []rune(currentPrompt)
	if len(currentRunes) == 0 {
		return 0, false
	}
	if len(cachedRunes) > simulatedModelCacheMaxPartialMatchRunes || len(currentRunes) > simulatedModelCacheMaxPartialMatchRunes {
		return 0, false
	}
	lcs := longestCommonSubstringLength(cachedRunes, currentRunes)
	return float64(lcs) / float64(len(currentRunes)), true
}

func longestCommonSubstringLength(a []rune, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	states := make([]simulatedModelCacheSuffixState, 1, len(a)*2)
	states[0] = simulatedModelCacheSuffixState{Link: -1}
	last := 0
	for _, ch := range a {
		cur := len(states)
		states = append(states, simulatedModelCacheSuffixState{
			Length: states[last].Length + 1,
			Next:   map[rune]int{},
		})
		p := last
		for p != -1 {
			if states[p].Next == nil {
				states[p].Next = map[rune]int{}
			}
			if _, exists := states[p].Next[ch]; exists {
				break
			}
			states[p].Next[ch] = cur
			p = states[p].Link
		}
		if p == -1 {
			states[cur].Link = 0
		} else {
			q := states[p].Next[ch]
			if states[p].Length+1 == states[q].Length {
				states[cur].Link = q
			} else {
				cloneNext := make(map[rune]int, len(states[q].Next))
				for key, value := range states[q].Next {
					cloneNext[key] = value
				}
				clone := len(states)
				states = append(states, simulatedModelCacheSuffixState{
					Length: states[p].Length + 1,
					Link:   states[q].Link,
					Next:   cloneNext,
				})
				for p != -1 {
					next, exists := states[p].Next[ch]
					if !exists || next != q {
						break
					}
					states[p].Next[ch] = clone
					p = states[p].Link
				}
				states[q].Link = clone
				states[cur].Link = clone
			}
		}
		last = cur
	}

	best := 0
	state := 0
	length := 0
	for _, ch := range b {
		if next, exists := states[state].Next[ch]; exists {
			state = next
			length++
		} else {
			for state != -1 {
				state = states[state].Link
				if state == -1 {
					break
				}
				if next, exists := states[state].Next[ch]; exists {
					length = states[state].Length + 1
					state = next
					break
				}
			}
			if state == -1 {
				state = 0
				length = 0
			}
		}
		if length > best {
			best = length
		}
	}
	return best
}

type simulatedModelCacheSuffixState struct {
	Length int
	Link   int
	Next   map[rune]int
}

func GetSimulatedModelCacheReplay(ctx context.Context, req SimulatedModelCacheLookupRequest) (SimulatedModelCacheReplay, error) {
	if !common.RedisEnabled || common.RDB == nil {
		return SimulatedModelCacheReplay{}, nil
	}
	reuseLimit := req.ReuseLimit
	if reuseLimit <= 0 {
		reuseLimit = 3
	}
	key := simulatedModelCacheExactKey(req)
	ttl := ttlFromSeconds(req.TTLSeconds)
	for attempt := 0; attempt < 3; attempt++ {
		var replay SimulatedModelCacheReplay
		err := common.RDB.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Result()
			if err != nil {
				if err == redis.Nil {
					return nil
				}
				return err
			}
			var entry simulatedModelCacheExactEntry
			if err := common.UnmarshalJsonStr(raw, &entry); err != nil {
				return err
			}
			if entry.ReplayCount >= reuseLimit {
				return nil
			}
			response, ok := entry.pickResponse(nil)
			if !ok {
				return nil
			}
			loadStartedAt := time.Now()
			if err := loadSimulatedModelCacheResponseBody(&response); err != nil {
				return nil
			}
			loadDuration := time.Since(loadStartedAt)
			response.ReplayPreparationSeconds = loadDuration.Seconds()
			entry.ReplayCount++
			entry.UpdatedAt = time.Now().Unix()
			nextRaw, err := common.Marshal(entry)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, string(nextRaw), ttl)
				return nil
			})
			if err != nil {
				return err
			}
			replay = SimulatedModelCacheReplay{
				Found:               true,
				Response:            response,
				ReplayCount:         entry.ReplayCount,
				LoadDurationSeconds: loadDuration.Seconds(),
			}
			return nil
		}, key)
		if err == redis.TxFailedErr {
			continue
		}
		if err != nil {
			return SimulatedModelCacheReplay{}, err
		}
		return replay, nil
	}
	return SimulatedModelCacheReplay{}, redis.TxFailedErr
}

func StoreSimulatedModelCacheResponse(ctx context.Context, req SimulatedModelCacheStoreRequest) error {
	if !common.RedisEnabled || common.RDB == nil {
		return nil
	}
	response := req.Response
	if err := storeSimulatedModelCacheResponseDiskBody(&response); err != nil {
		return nil
	}
	stored := false
	defer func() {
		if !stored {
			deleteSimulatedModelCacheResponseDiskBody(response)
		}
	}()
	key := simulatedModelCacheExactKey(SimulatedModelCacheLookupRequest{
		UserID:             req.UserID,
		ChannelID:          req.ChannelID,
		UpstreamModel:      req.UpstreamModel,
		FinalRequestFormat: req.FinalRequestFormat,
		RequestBody:        req.RequestBody,
	})
	entry, found, err := getSimulatedModelCacheExactEntry(ctx, key)
	if err != nil {
		return nil
	}
	if !found {
		entry = simulatedModelCacheExactEntry{
			UserID:        req.UserID,
			ChannelID:     req.ChannelID,
			UpstreamModel: req.UpstreamModel,
			PromptText:    req.PromptText,
		}
	}
	entry.UserID = req.UserID
	entry.ChannelID = req.ChannelID
	entry.UpstreamModel = req.UpstreamModel
	entry.PromptText = req.PromptText
	entry.RequestBody = nil
	now := time.Now()
	entry.UpdatedAt = now.Unix()
	removedResponses := entry.storeFreshResponse(response)
	ttl := ttlFromSeconds(req.TTLSeconds)
	if err := setSimulatedModelCacheExactEntry(ctx, key, entry, ttl); err != nil {
		return nil
	}
	stored = true
	deleteSimulatedModelCacheResponseDiskBodies(removedResponses)
	indexKey := simulatedModelCacheScopeIndexKey(req.UserID, req.ChannelID, req.UpstreamModel)
	if err := common.RDB.ZAdd(ctx, indexKey, &redis.Z{
		Score:  float64(now.UnixNano()),
		Member: key,
	}).Err(); err != nil {
		return nil
	}
	_ = common.RDB.Expire(ctx, indexKey, ttl).Err()
	_ = evictSimulatedModelCacheOldestScopeEntries(ctx, indexKey, simulatedModelCacheMaxEntriesPerScope)
	maybeCleanupExpiredSimulatedModelCacheDiskBodies(ttl)
	return nil
}

func FindSimulatedModelCachePartialMatch(ctx context.Context, req SimulatedModelCachePartialMatchRequest) (SimulatedModelCachePartialMatch, error) {
	if !common.RedisEnabled || common.RDB == nil || strings.TrimSpace(req.PromptText) == "" {
		return SimulatedModelCachePartialMatch{}, nil
	}
	minRatio := req.MinMatchRatio
	if minRatio <= 0 {
		minRatio = 0.01
	}
	if minRatio > 1 {
		minRatio = 1
	}
	indexKey := simulatedModelCacheScopeIndexKey(req.UserID, req.ChannelID, req.UpstreamModel)
	best := SimulatedModelCachePartialMatch{}
	keys, err := common.RDB.ZRevRange(ctx, indexKey, 0, int64(simulatedModelCacheMaxPartialMatchEntries-1)).Result()
	if err != nil {
		return SimulatedModelCachePartialMatch{}, nil
	}
	for _, key := range keys {
		entry, found, err := getSimulatedModelCacheExactEntry(ctx, key)
		if err != nil || !found || strings.TrimSpace(entry.PromptText) == "" {
			continue
		}
		if entry.ChannelID != req.ChannelID || entry.UpstreamModel != req.UpstreamModel {
			continue
		}
		ratio, ok := simulatedModelCachePartialMatchRatio(entry.PromptText, req.PromptText)
		if ok && ratio >= minRatio && (!best.Found || ratio > best.MatchRatio) {
			best = SimulatedModelCachePartialMatch{
				Found:      true,
				MatchRatio: ratio,
				PromptText: entry.PromptText,
			}
		}
	}
	return best, nil
}

func evictSimulatedModelCacheOldestScopeEntries(ctx context.Context, indexKey string, limit int) error {
	if limit <= 0 {
		return nil
	}
	count, err := common.RDB.ZCard(ctx, indexKey).Result()
	if err != nil || count <= int64(limit) {
		return err
	}
	evictCount := count - int64(limit)
	keys, err := common.RDB.ZRange(ctx, indexKey, 0, evictCount-1).Result()
	if err != nil || len(keys) == 0 {
		return err
	}
	responsesToDelete := make([]SimulatedModelCacheResponse, 0)
	for _, key := range keys {
		entry, found, err := getSimulatedModelCacheExactEntry(ctx, key)
		if err == nil && found {
			responsesToDelete = append(responsesToDelete, entry.Responses...)
		}
	}
	members := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		members = append(members, key)
	}
	_, err = common.RDB.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZRem(ctx, indexKey, members...)
		pipe.Del(ctx, keys...)
		return nil
	})
	if err == nil {
		deleteSimulatedModelCacheResponseDiskBodies(responsesToDelete)
	}
	return err
}

func maybeCleanupExpiredSimulatedModelCacheDiskBodies(ttl time.Duration) {
	now := time.Now()
	simulatedModelCacheDiskCleanupMu.Lock()
	if now.Sub(simulatedModelCacheLastDiskCleanup) < 10*time.Minute {
		simulatedModelCacheDiskCleanupMu.Unlock()
		return
	}
	simulatedModelCacheLastDiskCleanup = now
	simulatedModelCacheDiskCleanupMu.Unlock()

	if ttl <= 0 {
		ttl = ttlFromSeconds(0)
	}
	maxAge := ttl + time.Hour
	dir := simulatedModelCacheDiskBodyDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err == nil {
			common.DecrementDiskFiles(info.Size())
		}
	}
}

func getSimulatedModelCacheExactEntry(ctx context.Context, key string) (simulatedModelCacheExactEntry, bool, error) {
	raw, err := common.RDB.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return simulatedModelCacheExactEntry{}, false, nil
		}
		return simulatedModelCacheExactEntry{}, false, err
	}
	var entry simulatedModelCacheExactEntry
	if err := common.UnmarshalJsonStr(raw, &entry); err != nil {
		return simulatedModelCacheExactEntry{}, false, err
	}
	return entry, true, nil
}

func setSimulatedModelCacheExactEntry(ctx context.Context, key string, entry simulatedModelCacheExactEntry, ttl time.Duration) error {
	raw, err := common.Marshal(entry)
	if err != nil {
		return err
	}
	return common.RDB.Set(ctx, key, string(raw), ttl).Err()
}

func ttlFromSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 86400
	}
	return time.Duration(seconds) * time.Second
}

func simulatedModelCacheExactKey(req SimulatedModelCacheLookupRequest) string {
	return fmt.Sprintf("%s:exact:%d:%d:%s:%s:%s",
		simulatedModelCacheKeyPrefix,
		req.UserID,
		req.ChannelID,
		sha256Hex([]byte(req.UpstreamModel)),
		req.FinalRequestFormat,
		sha256Hex(normalizeSimulatedModelCacheRequestBody(req.RequestBody)),
	)
}

func simulatedModelCacheScopeIndexKey(userID int, channelID int, upstreamModel string) string {
	return fmt.Sprintf("%s:scope_index:%d:%d:%s",
		simulatedModelCacheKeyPrefix,
		userID,
		channelID,
		sha256Hex([]byte(upstreamModel)),
	)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeSimulatedModelCacheRequestBody(body []byte) []byte {
	var payload any
	if err := common.Unmarshal(body, &payload); err != nil {
		return body
	}
	normalized, err := common.Marshal(payload)
	if err != nil {
		return body
	}
	return normalized
}
