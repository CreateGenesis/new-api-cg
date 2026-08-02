package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/abema/go-mp4"
	"github.com/shopspring/decimal"
)

const (
	simulatedModelCachePromptTextChunkRunes = 128
	simulatedModelCacheMediaProbeMaxBytes   = 16 * 1024 * 1024
)

type simulatedModelCachePromptBlock struct {
	hashHigh uint64
	hashLow  uint64
	weight   uint32
	media    bool
	barrier  bool
}

type SimulatedModelCachePrompt struct {
	Text            string
	blocks          []simulatedModelCachePromptBlock
	totalWeight     uint64
	estimatedTokens int
	scopeDigest     string
	identityDigest  string
	barrierReason   string
	fatalReason     string
}

func (p SimulatedModelCachePrompt) IsMultimodal() bool {
	return len(p.blocks) > 0
}

func (p SimulatedModelCachePrompt) IsEmpty() bool {
	if p.IsMultimodal() {
		return p.totalWeight == 0
	}
	return strings.TrimSpace(p.Text) == ""
}

func (p SimulatedModelCachePrompt) EstimatedTokens() int {
	if p.IsMultimodal() {
		if p.totalWeight > math.MaxInt32 {
			return math.MaxInt32
		}
		return int(p.totalWeight)
	}
	return p.estimatedTokens
}

func (p SimulatedModelCachePrompt) DiagnosticReason() string {
	if p.fatalReason != "" {
		return p.fatalReason
	}
	return p.barrierReason
}

type simulatedModelCachePromptBuilder struct {
	model         string
	settings      dto.SimulatedModelCacheMultimodalSettings
	blocks        []simulatedModelCachePromptBlock
	totalWeight   uint64
	hasMedia      bool
	barrierReason string
	fatalReason   string
}

func ExtractSimulatedModelCachePrompt(format types.RelayFormat, model string, body []byte, settings dto.SimulatedModelCacheSettings) SimulatedModelCachePrompt {
	text := ExtractSimulatedModelCachePromptText(format, body)
	pureText := SimulatedModelCachePrompt{
		Text:            text,
		estimatedTokens: EstimateTokenByModel(model, text),
	}
	if settings.Multimodal == nil || !settings.Multimodal.Enabled || settings.Multimodal.Validate() != nil {
		return pureText
	}

	var root map[string]any
	if common.Unmarshal(body, &root) != nil {
		return pureText
	}
	builder := &simulatedModelCachePromptBuilder{model: model, settings: *settings.Multimodal}
	builder.addControl("protocol", string(format))
	switch format {
	case types.RelayFormatOpenAI:
		buildOpenAIChatSimulatedModelCachePrompt(builder, root)
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		buildOpenAIResponsesSimulatedModelCachePrompt(builder, root)
	case types.RelayFormatClaude:
		buildClaudeSimulatedModelCachePrompt(builder, root)
	case types.RelayFormatGemini:
		buildGeminiSimulatedModelCachePrompt(builder, root)
	default:
		return pureText
	}
	if !builder.hasMedia {
		return pureText
	}

	prompt := SimulatedModelCachePrompt{
		blocks:          builder.blocks,
		totalWeight:     builder.totalWeight,
		scopeDigest:     simulatedModelCacheMultimodalSettingsDigest(*settings.Multimodal),
		barrierReason:   builder.barrierReason,
		fatalReason:     builder.fatalReason,
		estimatedTokens: int(min(builder.totalWeight, uint64(math.MaxInt32))),
	}
	prompt.identityDigest = simulatedModelCachePromptIdentityDigest(prompt)
	return prompt
}

func (b *simulatedModelCachePromptBuilder) addControl(label string, value string) {
	b.addWeightedBlock("control:"+label, []byte(value), uint64(max(1, EstimateTokenByModel(b.model, label+" "+value))), false, false)
}

func (b *simulatedModelCachePromptBuilder) addCanonical(label string, value any) {
	if value == nil {
		return
	}
	raw, err := common.Marshal(value)
	if err != nil {
		b.addBarrier("invalid_"+label, 1)
		return
	}
	b.addText("json:"+label, string(raw))
}

func (b *simulatedModelCachePromptBuilder) addText(label string, text string) {
	if text == "" || b.fatalReason != "" {
		return
	}
	start := 0
	runes := 0
	for position := range text {
		if runes < simulatedModelCachePromptTextChunkRunes {
			runes++
			continue
		}
		b.addTextChunk(label, text[start:position])
		start = position
		runes = 1
	}
	if start < len(text) {
		b.addTextChunk(label, text[start:])
	}
}

func (b *simulatedModelCachePromptBuilder) addTextChunk(label string, text string) {
	weight := max(1, EstimateTokenByModel(b.model, text))
	b.addWeightedBlock("text:"+label, []byte(text), uint64(weight), false, false)
}

func (b *simulatedModelCachePromptBuilder) addBarrier(reason string, weight uint64) {
	if b.barrierReason == "" {
		b.barrierReason = reason
	}
	b.addWeightedBlock("barrier", []byte(reason), max(uint64(1), weight), true, true)
}

func (b *simulatedModelCachePromptBuilder) addWeightedBlock(domain string, value []byte, weight uint64, media bool, barrier bool) {
	if b.fatalReason != "" {
		return
	}
	if weight == 0 || weight > math.MaxInt32 || b.totalWeight > math.MaxInt32-weight {
		b.fatalReason = "prompt_weight_overflow"
		return
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(value)
	sum := digest.Sum(nil)
	b.blocks = append(b.blocks, simulatedModelCachePromptBlock{
		hashHigh: binary.BigEndian.Uint64(sum[:8]),
		hashLow:  binary.BigEndian.Uint64(sum[8:16]),
		weight:   uint32(weight),
		media:    media,
		barrier:  barrier,
	})
	b.totalWeight += weight
}

func buildOpenAIChatSimulatedModelCachePrompt(builder *simulatedModelCachePromptBuilder, root map[string]any) {
	builder.addCanonical("tools", root["tools"])
	builder.addCanonical("tool_choice", root["tool_choice"])
	messages, _ := root["messages"].([]any)
	for index, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			builder.addBarrier("invalid_message", 1)
			continue
		}
		builder.addControl("message_index", strconv.Itoa(index))
		builder.addControl("role", stringValue(message["role"]))
		if name := stringValue(message["name"]); name != "" {
			builder.addControl("name", name)
		}
		if toolCallID := stringValue(message["tool_call_id"]); toolCallID != "" {
			builder.addControl("tool_call_id", toolCallID)
		}
		addSimulatedModelCacheContent(builder, message["content"])
		builder.addCanonical("tool_calls", message["tool_calls"])
	}
}

func buildOpenAIResponsesSimulatedModelCachePrompt(builder *simulatedModelCachePromptBuilder, root map[string]any) {
	addSimulatedModelCachePromptValue(builder, "instructions", root["instructions"])
	builder.addCanonical("tools", root["tools"])
	builder.addCanonical("tool_choice", root["tool_choice"])
	for _, field := range []string{"previous_response_id", "conversation", "prompt_cache_key"} {
		if value, exists := root[field]; exists {
			builder.addCanonical(field, value)
		}
	}
	addSimulatedModelCacheContent(builder, root["input"])
}

func buildClaudeSimulatedModelCachePrompt(builder *simulatedModelCachePromptBuilder, root map[string]any) {
	addSimulatedModelCacheContent(builder, root["system"])
	builder.addCanonical("tools", root["tools"])
	builder.addCanonical("tool_choice", root["tool_choice"])
	messages, _ := root["messages"].([]any)
	for index, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			builder.addBarrier("invalid_message", 1)
			continue
		}
		builder.addControl("message_index", strconv.Itoa(index))
		builder.addControl("role", stringValue(message["role"]))
		addSimulatedModelCacheContent(builder, message["content"])
	}
}

func buildGeminiSimulatedModelCachePrompt(builder *simulatedModelCachePromptBuilder, root map[string]any) {
	if system := firstMap(root, "systemInstruction", "system_instruction"); system != nil {
		addGeminiSimulatedModelCacheParts(builder, system["parts"])
	}
	builder.addCanonical("tools", root["tools"])
	builder.addCanonical("tool_config", firstValue(root, "toolConfig", "tool_config"))
	contents, _ := root["contents"].([]any)
	for index, value := range contents {
		content, ok := value.(map[string]any)
		if !ok {
			builder.addBarrier("invalid_content", 1)
			continue
		}
		builder.addControl("content_index", strconv.Itoa(index))
		builder.addControl("role", stringValue(content["role"]))
		addGeminiSimulatedModelCacheParts(builder, content["parts"])
	}
}

func addGeminiSimulatedModelCacheParts(builder *simulatedModelCachePromptBuilder, value any) {
	parts, _ := value.([]any)
	for index, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			builder.addBarrier("invalid_gemini_part", 1)
			continue
		}
		builder.addControl("part_index", strconv.Itoa(index))
		if text, ok := part["text"].(string); ok {
			builder.addText("gemini_text", text)
			continue
		}
		if inline := firstMap(part, "inlineData", "inline_data"); inline != nil {
			mimeType := firstString(inline, "mimeType", "mime_type")
			modality := simulatedModelCacheModalityForMime(mimeType, "file")
			metadata := []map[string]any{part, inline, firstMap(part, "videoMetadata", "video_metadata")}
			builder.addMedia(modality, simulatedModelCacheMediaReference{kind: "inline", value: stringValue(inline["data"])}, mimeType, "", "", metadata)
			continue
		}
		if fileData := firstMap(part, "fileData", "file_data"); fileData != nil {
			mimeType := firstString(fileData, "mimeType", "mime_type")
			modality := simulatedModelCacheModalityForMime(mimeType, "file")
			uri := firstString(fileData, "fileUri", "file_uri")
			builder.addMedia(modality, simulatedModelCacheReferenceForValue(uri, "file_id"), mimeType, "", "", []map[string]any{part, fileData})
			continue
		}
		builder.addCanonical("gemini_part", part)
	}
}

func addSimulatedModelCachePromptValue(builder *simulatedModelCachePromptBuilder, label string, value any) {
	if text, ok := value.(string); ok {
		builder.addText(label, text)
		return
	}
	builder.addCanonical(label, value)
}

func addSimulatedModelCacheContent(builder *simulatedModelCachePromptBuilder, value any) {
	switch content := value.(type) {
	case nil:
		return
	case string:
		builder.addText("content", content)
	case []any:
		for index, item := range content {
			builder.addControl("content_index", strconv.Itoa(index))
			addSimulatedModelCacheContentItem(builder, item)
		}
	case map[string]any:
		addSimulatedModelCacheContentItem(builder, content)
	default:
		builder.addBarrier("invalid_content", 1)
	}
}

func addSimulatedModelCacheContentItem(builder *simulatedModelCachePromptBuilder, value any) {
	item, ok := value.(map[string]any)
	if !ok {
		if text, textOK := value.(string); textOK {
			builder.addText("content", text)
			return
		}
		builder.addBarrier("invalid_content_item", 1)
		return
	}
	contentType := strings.ToLower(stringValue(item["type"]))
	if contentType != "" {
		builder.addControl("content_type", contentType)
	}
	switch contentType {
	case "text", "input_text", "output_text":
		builder.addText("content", stringValue(item["text"]))
	case "message":
		builder.addControl("role", stringValue(item["role"]))
		if name := stringValue(item["name"]); name != "" {
			builder.addControl("name", name)
		}
		addSimulatedModelCacheContent(builder, item["content"])
	case "image_url":
		media := item["image_url"]
		if imageMap, ok := media.(map[string]any); ok {
			builder.addMedia("image", simulatedModelCacheReferenceForValue(stringValue(imageMap["url"]), "url"), firstString(imageMap, "mime_type", "mimeType"), stringValue(imageMap["detail"]), "", []map[string]any{item, imageMap})
		} else {
			builder.addMedia("image", simulatedModelCacheReferenceForValue(stringValue(media), "url"), "", stringValue(item["detail"]), "", []map[string]any{item})
		}
	case "input_image", "image":
		if source := firstMap(item, "source"); source != nil {
			addSimulatedModelCacheMediaItem(builder, source, "image", item)
		} else {
			addSimulatedModelCacheMediaItem(builder, item, "image")
		}
	case "input_audio", "audio":
		if audio := firstMap(item, "input_audio", "source"); audio != nil {
			addSimulatedModelCacheMediaItem(builder, audio, "audio", item)
		} else {
			addSimulatedModelCacheMediaItem(builder, item, "audio")
		}
	case "video_url", "input_video", "video":
		addSimulatedModelCacheMediaItem(builder, item, "video")
	case "input_file", "file", "document":
		if file := firstMap(item, "file", "source"); file != nil {
			addSimulatedModelCacheMediaItem(builder, file, "file", item)
		} else {
			addSimulatedModelCacheMediaItem(builder, item, "file")
		}
	case "tool_result":
		builder.addControl("tool_use_id", stringValue(item["tool_use_id"]))
		addSimulatedModelCacheContent(builder, item["content"])
	case "tool_use", "function_call", "function_call_output":
		builder.addCanonical(contentType, item)
	default:
		if strings.Contains(contentType, "image") || strings.Contains(contentType, "audio") || strings.Contains(contentType, "video") || strings.Contains(contentType, "file") || strings.Contains(contentType, "document") {
			builder.hasMedia = true
			builder.addBarrier("unsupported_media_block", uint64(builder.fallbackTokens(simulatedModelCacheModalityForType(contentType))))
			return
		}
		builder.addCanonical("content_item", item)
	}
}

func addSimulatedModelCacheMediaItem(builder *simulatedModelCachePromptBuilder, item map[string]any, modality string, parents ...map[string]any) {
	mimeType := firstString(item, "mime_type", "media_type", "mimeType")
	detail := firstString(item, "detail", "format")
	filename := firstString(item, "filename", "file_name")
	reference := simulatedModelCacheMediaReference{}
	for _, field := range []string{"data", "file_data"} {
		if value := stringValue(item[field]); value != "" {
			reference = simulatedModelCacheReferenceForValue(value, "inline")
			break
		}
	}
	if reference.value == "" {
		for _, field := range []string{"url", "file_url", "image_url", "video_url"} {
			if value := stringValue(item[field]); value != "" {
				reference = simulatedModelCacheReferenceForValue(value, "url")
				break
			}
		}
	}
	if reference.value == "" {
		if value := firstString(item, "file_id", "id"); value != "" {
			reference = simulatedModelCacheMediaReference{kind: "file_id", value: value}
		}
	}
	metadata := append([]map[string]any{item}, parents...)
	builder.addMedia(modality, reference, mimeType, detail, filename, metadata)
}

type simulatedModelCacheMediaReference struct {
	kind  string
	value string
}

type simulatedModelCacheMediaMetadata struct {
	width      int64
	height     int64
	seconds    float64
	bytes      int64
	hasWidth   bool
	hasHeight  bool
	hasSeconds bool
	hasBytes   bool
	invalid    bool
}

func (b *simulatedModelCachePromptBuilder) addMedia(modality string, reference simulatedModelCacheMediaReference, mimeType string, detail string, filename string, metadataMaps []map[string]any) {
	b.hasMedia = true
	fallback := uint64(b.fallbackTokens(modality))
	identity, decoded, decodedBytes, normalizedMime, invalid := simulatedModelCacheMediaIdentity(reference, mimeType)
	if normalizedMime != "" {
		mimeType = normalizedMime
	}
	metadata := simulatedModelCacheExplicitMediaMetadata(metadataMaps)
	if invalid || metadata.invalid {
		b.addBarrier("invalid_"+modality, fallback)
		return
	}
	if reference.kind == "inline" && decoded != nil {
		probed, probeInvalid := simulatedModelCacheProbeMedia(modality, mimeType, detail, decoded, decodedBytes)
		if probeInvalid {
			b.addBarrier("invalid_"+modality+"_metadata", fallback)
			return
		}
		metadata = metadata.mergeMissing(probed)
	}
	weight, valid := b.mediaWeight(modality, metadata, fallback)
	if !valid {
		b.addBarrier("invalid_"+modality+"_weight", fallback)
		return
	}
	attributes, err := common.Marshal(map[string]any{
		"detail":   detail,
		"filename": filename,
		"kind":     reference.kind,
		"mime":     strings.ToLower(strings.TrimSpace(mimeType)),
		"modality": modality,
		"metadata": map[string]any{
			"bytes":       metadata.bytes,
			"has_bytes":   metadata.hasBytes,
			"has_height":  metadata.hasHeight,
			"has_seconds": metadata.hasSeconds,
			"has_width":   metadata.hasWidth,
			"height":      metadata.height,
			"seconds":     metadata.seconds,
			"width":       metadata.width,
		},
	})
	if err != nil {
		b.addBarrier("invalid_"+modality+"_attributes", fallback)
		return
	}
	value := make([]byte, 0, len(identity)+len(attributes)+1)
	value = append(value, identity...)
	value = append(value, 0)
	value = append(value, attributes...)
	b.addWeightedBlock("media:"+modality, value, weight, true, false)
}

func (b *simulatedModelCachePromptBuilder) fallbackTokens(modality string) int {
	switch modality {
	case "image":
		return *b.settings.ImageFallbackTokens
	case "video":
		return *b.settings.VideoFallbackTokens
	case "audio":
		return *b.settings.AudioFallbackTokens
	default:
		return *b.settings.FileFallbackTokens
	}
}

func (b *simulatedModelCachePromptBuilder) mediaWeight(modality string, metadata simulatedModelCacheMediaMetadata, fallback uint64) (uint64, bool) {
	var value decimal.Decimal
	switch modality {
	case "image":
		if !metadata.hasWidth || !metadata.hasHeight {
			return fallback, true
		}
		value = decimal.NewFromInt(metadata.width).Mul(decimal.NewFromInt(metadata.height)).Mul(decimal.NewFromFloat(*b.settings.ImageTokensPerMegapixel)).Div(decimal.NewFromInt(1_000_000))
	case "video":
		if !metadata.hasWidth || !metadata.hasHeight || !metadata.hasSeconds {
			return fallback, true
		}
		value = decimal.NewFromInt(metadata.width).Mul(decimal.NewFromInt(metadata.height)).Mul(decimal.NewFromFloat(metadata.seconds)).Mul(decimal.NewFromFloat(*b.settings.VideoTokensPerSecondMegapixel)).Div(decimal.NewFromInt(1_000_000))
	case "audio":
		if !metadata.hasSeconds {
			return fallback, true
		}
		value = decimal.NewFromFloat(metadata.seconds).Mul(decimal.NewFromFloat(*b.settings.AudioTokensPerSecond))
	default:
		if !metadata.hasBytes {
			return fallback, true
		}
		value = decimal.NewFromInt(metadata.bytes).Mul(decimal.NewFromFloat(*b.settings.FileTokensPerMiB)).Div(decimal.NewFromInt(1_048_576))
	}
	value = value.Ceil()
	if value.LessThan(decimal.NewFromInt(1)) || value.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
		return 0, false
	}
	weight := value.IntPart()
	if weight < 1 || weight > math.MaxInt32 {
		return 0, false
	}
	return uint64(weight), true
}

func simulatedModelCacheMediaIdentity(reference simulatedModelCacheMediaReference, declaredMime string) (identity []byte, probe []byte, decodedBytes int64, normalizedMime string, invalid bool) {
	if strings.TrimSpace(reference.value) == "" {
		return nil, nil, 0, "", true
	}
	switch reference.kind {
	case "inline":
		decoded, mimeType, err := decodeSimulatedModelCacheInlineMedia(reference.value)
		if err != nil {
			return nil, nil, 0, "", true
		}
		if declaredMime != "" && mimeType != "" && !strings.EqualFold(strings.TrimSpace(declaredMime), mimeType) {
			return nil, nil, 0, "", true
		}
		return decoded.digest, decoded.probe, decoded.size, mimeType, false
	case "url":
		normalized, err := normalizeSimulatedModelCacheMediaURL(reference.value)
		if err != nil {
			return nil, nil, 0, "", true
		}
		digest := sha256.Sum256([]byte(normalized))
		return digest[:], nil, 0, "", false
	case "file_id":
		digest := sha256.Sum256([]byte(reference.value))
		return digest[:], nil, 0, "", false
	default:
		return nil, nil, 0, "", true
	}
}

type simulatedModelCacheDecodedMedia struct {
	digest []byte
	probe  []byte
	size   int64
}

func decodeSimulatedModelCacheInlineMedia(value string) (simulatedModelCacheDecodedMedia, string, error) {
	mimeType := ""
	encoded := value
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		comma := strings.IndexByte(value, ',')
		if comma < 0 {
			return simulatedModelCacheDecodedMedia{}, "", fmt.Errorf("invalid data URL")
		}
		header := value[5:comma]
		encoded = value[comma+1:]
		parts := strings.Split(header, ";")
		if len(parts) > 0 {
			mimeType = strings.ToLower(strings.TrimSpace(parts[0]))
		}
		base64Encoded := false
		for _, part := range parts[1:] {
			if strings.EqualFold(strings.TrimSpace(part), "base64") {
				base64Encoded = true
				break
			}
		}
		if !base64Encoded {
			decoded, err := url.PathUnescape(encoded)
			if err != nil {
				return simulatedModelCacheDecodedMedia{}, "", err
			}
			data := []byte(decoded)
			if len(data) == 0 {
				return simulatedModelCacheDecodedMedia{}, "", fmt.Errorf("empty inline media")
			}
			digest := sha256.Sum256(data)
			return simulatedModelCacheDecodedMedia{digest: digest[:], probe: append([]byte(nil), data[:min(len(data), simulatedModelCacheMediaProbeMaxBytes)]...), size: int64(len(data))}, mimeType, nil
		}
	}

	var cleaned strings.Builder
	cleaned.Grow(len(encoded))
	for _, r := range encoded {
		if !unicode.IsSpace(r) {
			cleaned.WriteRune(r)
		}
	}
	base64Text := cleaned.String()
	padding := len(base64Text) - len(strings.TrimRight(base64Text, "="))
	if padding > 2 {
		return simulatedModelCacheDecodedMedia{}, "", fmt.Errorf("invalid base64 padding")
	}
	base64Text = strings.TrimRight(base64Text, "=")
	if strings.Contains(base64Text, "=") {
		return simulatedModelCacheDecodedMedia{}, "", fmt.Errorf("invalid base64 padding")
	}
	encoding := base64.RawStdEncoding
	if strings.ContainsAny(base64Text, "-_") {
		encoding = base64.RawURLEncoding
	}
	decoder := base64.NewDecoder(encoding, strings.NewReader(base64Text))
	hasher := sha256.New()
	probe := make([]byte, 0, min(base64.StdEncoding.DecodedLen(len(base64Text)), simulatedModelCacheMediaProbeMaxBytes))
	buffer := make([]byte, 32*1024)
	var decodedBytes int64
	for {
		read, err := decoder.Read(buffer)
		if read > 0 {
			_, _ = hasher.Write(buffer[:read])
			decodedBytes += int64(read)
			remaining := simulatedModelCacheMediaProbeMaxBytes - len(probe)
			if remaining > 0 {
				probe = append(probe, buffer[:min(read, remaining)]...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return simulatedModelCacheDecodedMedia{}, "", err
		}
	}
	if decodedBytes == 0 {
		return simulatedModelCacheDecodedMedia{}, "", fmt.Errorf("empty inline media")
	}
	return simulatedModelCacheDecodedMedia{digest: hasher.Sum(nil), probe: probe, size: decodedBytes}, mimeType, nil
}

func normalizeSimulatedModelCacheMediaURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid media URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported media URL scheme")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else {
		parsed.Host = hostname
	}
	parsed.Fragment = ""
	query := parsed.Query()
	simulatedModelCacheStripSignedURLAuthentication(query)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func simulatedModelCacheStripSignedURLAuthentication(query url.Values) {
	present := make(map[string]bool, len(query))
	for key := range query {
		present[strings.ToLower(key)] = true
	}
	sets := [][]string{
		{"x-amz-algorithm", "x-amz-credential", "x-amz-date", "x-amz-expires", "x-amz-signedheaders", "x-amz-signature", "x-amz-security-token"},
		{"x-goog-algorithm", "x-goog-credential", "x-goog-date", "x-goog-expires", "x-goog-signedheaders", "x-goog-signature"},
		{"sv", "st", "se", "sp", "spr", "sr", "sig", "skoid", "sktid", "skt", "ske", "sks", "skv"},
		{"ossaccesskeyid", "expires", "signature", "security-token", "x-oss-signature-version", "x-oss-credential", "x-oss-date", "x-oss-expires", "x-oss-signed-headers", "x-oss-signature", "x-oss-security-token"},
		{"q-sign-algorithm", "q-ak", "q-sign-time", "q-key-time", "q-header-list", "q-url-param-list", "q-signature", "x-cos-security-token"},
	}
	recognized := []bool{
		present["x-amz-signature"] && present["x-amz-credential"] && present["x-amz-date"],
		present["x-goog-signature"] && present["x-goog-credential"] && present["x-goog-date"],
		present["sig"] && (present["sv"] || present["se"] || present["sp"]),
		(present["signature"] && present["ossaccesskeyid"]) || (present["x-oss-signature"] && present["x-oss-credential"]),
		present["q-signature"] && present["q-ak"] && present["q-key-time"],
	}
	for index, keys := range sets {
		if !recognized[index] {
			continue
		}
		for originalKey := range query {
			lower := strings.ToLower(originalKey)
			for _, authKey := range keys {
				if lower == authKey {
					query.Del(originalKey)
					break
				}
			}
		}
		return
	}
}

func simulatedModelCacheExplicitMediaMetadata(maps []map[string]any) simulatedModelCacheMediaMetadata {
	metadata := simulatedModelCacheMediaMetadata{}
	for _, values := range maps {
		if values == nil {
			continue
		}
		if width, exists := firstNumber(values, "width"); exists {
			if width <= 0 || width > math.MaxUint32 || math.Trunc(width) != width {
				metadata.invalid = true
			} else {
				metadata.width, metadata.hasWidth = int64(width), true
			}
		}
		if height, exists := firstNumber(values, "height"); exists {
			if height <= 0 || height > math.MaxUint32 || math.Trunc(height) != height {
				metadata.invalid = true
			} else {
				metadata.height, metadata.hasHeight = int64(height), true
			}
		}
		if seconds, exists := firstNumber(values, "seconds", "duration"); exists {
			if seconds <= 0 || seconds > relaycommon.MaxTaskDurationSeconds || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
				metadata.invalid = true
			} else {
				metadata.seconds, metadata.hasSeconds = seconds, true
			}
		}
		if size, exists := firstNumber(values, "bytes", "size"); exists {
			if size <= 0 || size >= float64(math.MaxInt64) || math.Trunc(size) != size {
				metadata.invalid = true
			} else {
				metadata.bytes, metadata.hasBytes = int64(size), true
			}
		}
	}
	return metadata
}

func simulatedModelCacheProbeMedia(modality string, mimeType string, detail string, data []byte, decodedBytes int64) (simulatedModelCacheMediaMetadata, bool) {
	metadata := simulatedModelCacheMediaMetadata{bytes: decodedBytes, hasBytes: decodedBytes > 0}
	if data == nil {
		return metadata, false
	}
	probeComplete := int64(len(data)) == decodedBytes
	switch modality {
	case "image":
		config, _, err := getImageConfig(bytes.NewReader(data))
		if err != nil {
			return metadata, probeComplete && simulatedModelCacheKnownImageMime(mimeType)
		}
		if config.Width <= 0 || config.Height <= 0 {
			return metadata, true
		}
		metadata.width, metadata.height = int64(config.Width), int64(config.Height)
		metadata.hasWidth, metadata.hasHeight = true, true
	case "video":
		if !strings.EqualFold(strings.TrimSpace(mimeType), "video/mp4") && !strings.HasSuffix(strings.ToLower(mimeType), "/mp4") {
			return metadata, false
		}
		info, err := mp4.Probe(bytes.NewReader(data))
		if err != nil || info.Timescale == 0 || info.Duration == 0 {
			return metadata, probeComplete
		}
		seconds := float64(info.Duration) / float64(info.Timescale)
		if seconds <= 0 || seconds > relaycommon.MaxTaskDurationSeconds || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return metadata, true
		}
		metadata.seconds, metadata.hasSeconds = seconds, true
		for _, track := range info.Tracks {
			if track.AVC != nil && track.AVC.Width > 0 && track.AVC.Height > 0 {
				metadata.width, metadata.height = int64(track.AVC.Width), int64(track.AVC.Height)
				metadata.hasWidth, metadata.hasHeight = true, true
				break
			}
		}
	case "audio":
		format := strings.ToLower(strings.TrimSpace(detail))
		if format == "pcm16" || format == "g711_ulaw" || format == "g711_alaw" {
			bytesPerSample, sampleRate := int64(1), float64(8000)
			if format == "pcm16" {
				bytesPerSample, sampleRate = 2, 24000
			}
			seconds := float64(decodedBytes) / float64(bytesPerSample) / sampleRate
			if seconds <= 0 || seconds > relaycommon.MaxTaskDurationSeconds || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
				return metadata, true
			}
			metadata.seconds, metadata.hasSeconds = seconds, true
			return metadata, false
		}
		ext := simulatedModelCacheAudioExtension(mimeType)
		if ext == "" {
			return metadata, false
		}
		duration, err := common.GetAudioDuration(context.Background(), bytes.NewReader(data), ext)
		if err != nil {
			return metadata, false
		}
		if duration <= 0 || duration > relaycommon.MaxTaskDurationSeconds || math.IsNaN(duration) || math.IsInf(duration, 0) {
			return metadata, true
		}
		metadata.seconds, metadata.hasSeconds = duration, true
	}
	return metadata, false
}

func (m simulatedModelCacheMediaMetadata) mergeMissing(other simulatedModelCacheMediaMetadata) simulatedModelCacheMediaMetadata {
	if !m.hasWidth && other.hasWidth {
		m.width, m.hasWidth = other.width, true
	}
	if !m.hasHeight && other.hasHeight {
		m.height, m.hasHeight = other.height, true
	}
	if !m.hasSeconds && other.hasSeconds {
		m.seconds, m.hasSeconds = other.seconds, true
	}
	if !m.hasBytes && other.hasBytes {
		m.bytes, m.hasBytes = other.bytes, true
	}
	return m
}

func simulatedModelCacheKnownImageMime(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/heic", "image/heif":
		return true
	default:
		return false
	}
}

func simulatedModelCacheAudioExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/mpeg", "audio/mp3", "mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav", "wav":
		return ".wav"
	case "audio/flac", "flac":
		return ".flac"
	case "audio/mp4", "audio/m4a", "m4a":
		return ".m4a"
	case "audio/ogg", "ogg":
		return ".ogg"
	case "audio/opus", "opus":
		return ".opus"
	case "audio/aiff", "aiff":
		return ".aiff"
	case "audio/aac", "aac":
		return ".aac"
	case "audio/webm", "webm":
		return ".webm"
	default:
		if extension := filepath.Ext(mimeType); extension != "" {
			return extension
		}
		return ""
	}
}

func simulatedModelCacheReferenceForValue(value string, defaultKind string) simulatedModelCacheMediaReference {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return simulatedModelCacheMediaReference{kind: "inline", value: trimmed}
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
		scheme := strings.ToLower(parsed.Scheme)
		if scheme == "http" || scheme == "https" {
			return simulatedModelCacheMediaReference{kind: "url", value: trimmed}
		}
	}
	return simulatedModelCacheMediaReference{kind: defaultKind, value: value}
}

func simulatedModelCacheModalityForMime(mimeType string, fallback string) string {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(lower, "image/"):
		return "image"
	case strings.HasPrefix(lower, "video/"):
		return "video"
	case strings.HasPrefix(lower, "audio/"):
		return "audio"
	default:
		return fallback
	}
}

func simulatedModelCacheModalityForType(contentType string) string {
	switch {
	case strings.Contains(contentType, "image"):
		return "image"
	case strings.Contains(contentType, "video"):
		return "video"
	case strings.Contains(contentType, "audio"):
		return "audio"
	default:
		return "file"
	}
}

func simulatedModelCacheMultimodalSettingsDigest(settings dto.SimulatedModelCacheMultimodalSettings) string {
	raw, _ := common.Marshal(settings)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:16])
}

func simulatedModelCachePromptIdentityDigest(prompt SimulatedModelCachePrompt) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(prompt.scopeDigest))
	var buffer [21]byte
	for _, block := range prompt.blocks {
		binary.BigEndian.PutUint64(buffer[0:8], block.hashHigh)
		binary.BigEndian.PutUint64(buffer[8:16], block.hashLow)
		binary.BigEndian.PutUint32(buffer[16:20], block.weight)
		if block.barrier {
			buffer[20] = 1
		} else {
			buffer[20] = 0
		}
		_, _ = hasher.Write(buffer[:])
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func firstString(values map[string]any, fields ...string) string {
	for _, field := range fields {
		if value := stringValue(values[field]); value != "" {
			return value
		}
	}
	return ""
}

func firstMap(values map[string]any, fields ...string) map[string]any {
	for _, field := range fields {
		if value, ok := values[field].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func firstValue(values map[string]any, fields ...string) any {
	for _, field := range fields {
		if value, exists := values[field]; exists {
			return value
		}
	}
	return nil
}

func firstNumber(values map[string]any, fields ...string) (float64, bool) {
	for _, field := range fields {
		value, exists := values[field]
		if !exists {
			continue
		}
		switch number := value.(type) {
		case float64:
			return number, true
		case string:
			text := strings.TrimSpace(strings.TrimSuffix(number, "s"))
			parsed, err := strconv.ParseFloat(text, 64)
			return parsed, err == nil
		}
	}
	return 0, false
}
