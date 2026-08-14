package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func simulatedModelCacheMultimodalTestSettings() dto.SimulatedModelCacheSettings {
	imageRate := 520.0
	videoRate := 820.0
	audioRate := 25.0
	fileRate := 4096.0
	imageFallback := 520
	videoFallback := 8192
	audioFallback := 256
	fileFallback := 4096
	return dto.SimulatedModelCacheSettings{
		Enabled: true,
		Multimodal: &dto.SimulatedModelCacheMultimodalSettings{
			Enabled:                       true,
			ImageTokensPerMegapixel:       &imageRate,
			VideoTokensPerSecondMegapixel: &videoRate,
			AudioTokensPerSecond:          &audioRate,
			FileTokensPerMiB:              &fileRate,
			ImageFallbackTokens:           &imageFallback,
			VideoFallbackTokens:           &videoFallback,
			AudioFallbackTokens:           &audioFallback,
			FileFallbackTokens:            &fileFallback,
		},
	}
}

func simulatedModelCachePNGDataURL(t *testing.T, width int, height int) string {
	t.Helper()
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, width, height))))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func simulatedModelCacheMediaWeight(prompt SimulatedModelCachePrompt) uint64 {
	var weight uint64
	for _, block := range prompt.blocks {
		if block.media && !block.barrier {
			weight += uint64(block.weight)
		}
	}
	return weight
}

func TestExtractSimulatedModelCachePromptText(t *testing.T) {
	tests := []struct {
		name   string
		format types.RelayFormat
		body   string
		want   string
	}{
		{
			name:   "openai chat messages",
			format: types.RelayFormatOpenAI,
			body: `{
				"model":"gpt-test",
				"messages":[
					{"role":"system","content":"Be terse"},
					{"role":"user","content":[
						{"type":"text","text":"Explain cache"},
						{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
					]}
				]
			}`,
			want: "Be terse\nExplain cache",
		},
		{
			name:   "openai responses input and instructions",
			format: types.RelayFormatOpenAIResponses,
			body: `{
				"model":"gpt-test",
				"instructions":"Follow policy",
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"Summarize this"}]}
				]
			}`,
			want: "Follow policy\nSummarize this",
		},
		{
			name:   "claude system and messages",
			format: types.RelayFormatClaude,
			body: `{
				"model":"claude-test",
				"system":[{"type":"text","text":"Be safe"}],
				"messages":[{"role":"user","content":[{"type":"text","text":"Draft reply"}]}]
			}`,
			want: "Be safe\nDraft reply",
		},
		{
			name:   "gemini system instruction and contents",
			format: types.RelayFormatGemini,
			body: `{
				"contents":[{"role":"user","parts":[{"text":"Describe image"}]}],
				"systemInstruction":{"parts":[{"text":"Use bullet points"}]}
			}`,
			want: "Use bullet points\nDescribe image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSimulatedModelCachePromptText(tt.format, []byte(tt.body))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractSimulatedModelCachePromptSeparatesDifferentMedia(t *testing.T) {
	settings := simulatedModelCacheMultimodalTestSettings()
	body := func(url string) []byte {
		return []byte(fmt.Sprintf(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":%q}}]}]}`, url))
	}

	first := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("https://example.com/a.png"), settings)
	second := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("https://example.com/b.png"), settings)

	assert.True(t, first.IsMultimodal())
	assert.True(t, second.IsMultimodal())
	assert.NotEqual(t, first.identityDigest, second.identityDigest)
}

func TestExtractSimulatedModelCachePromptNormalizesEquivalentBase64(t *testing.T) {
	settings := simulatedModelCacheMultimodalTestSettings()
	dataURL := simulatedModelCachePNGDataURL(t, 2, 3)
	encoded := strings.TrimPrefix(dataURL, "data:image/png;base64,")
	equivalent := strings.TrimRight(encoded, "=")
	equivalent = equivalent[:12] + "\n" + equivalent[12:]
	body := func(data string) []byte {
		return []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, "data:image/png;base64,"+data))
	}

	first := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body(encoded), settings)
	second := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body(equivalent), settings)

	assert.True(t, first.IsMultimodal())
	assert.Equal(t, first.identityDigest, second.identityDigest)
}

func TestDecodeSimulatedModelCacheInlineMediaRejectsExcessPadding(t *testing.T) {
	_, _, err := decodeSimulatedModelCacheInlineMedia("YQ===")

	assert.ErrorContains(t, err, "invalid base64 padding")
}

func TestSimulatedModelCacheProbeMediaRejectsOversizedPCMDuration(t *testing.T) {
	decodedBytes := int64(relaycommon.MaxTaskDurationSeconds*24_000*2 + 1)

	metadata, invalid := simulatedModelCacheProbeMedia("audio", "", "pcm16", []byte{1}, decodedBytes)

	assert.True(t, invalid)
	assert.False(t, metadata.hasSeconds)
}

func TestExtractSimulatedModelCachePromptNormalizesRecognizedSignedURLs(t *testing.T) {
	settings := simulatedModelCacheMultimodalTestSettings()
	body := func(rawURL string) []byte {
		return []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, rawURL))
	}

	first := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("https://bucket.example/a.png?version=7&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=one&X-Amz-Date=20260101T000000Z&X-Amz-Signature=aaa"), settings)
	refreshed := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("https://bucket.example/a.png?X-Amz-Date=20260102T000000Z&X-Amz-Signature=bbb&X-Amz-Credential=two&X-Amz-Algorithm=AWS4-HMAC-SHA256&version=7"), settings)
	differentContentSelector := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("https://bucket.example/a.png?version=8&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=two&X-Amz-Date=20260102T000000Z&X-Amz-Signature=bbb"), settings)

	assert.Equal(t, first.identityDigest, refreshed.identityDigest)
	assert.NotEqual(t, first.identityDigest, differentContentSelector.identityDigest)
}

func TestExtractSimulatedModelCachePromptNormalizesSignedURLFamilies(t *testing.T) {
	settings := simulatedModelCacheMultimodalTestSettings()
	tests := []struct {
		name      string
		firstURL  string
		secondURL string
	}{
		{
			name:      "Google Cloud Storage",
			firstURL:  "https://storage.example/a.png?generation=7&X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Credential=one&X-Goog-Date=20260101T000000Z&X-Goog-Signature=aaa",
			secondURL: "https://storage.example/a.png?X-Goog-Date=20260102T000000Z&X-Goog-Signature=bbb&X-Goog-Credential=two&X-Goog-Algorithm=GOOG4-RSA-SHA256&generation=7",
		},
		{
			name:      "Azure Blob Storage",
			firstURL:  "https://account.blob.example/a.png?version=7&sv=2024-11-04&se=2026-01-01&sp=r&sig=aaa",
			secondURL: "https://account.blob.example/a.png?sig=bbb&sp=r&se=2026-01-02&sv=2024-11-04&version=7",
		},
		{
			name:      "Alibaba OSS",
			firstURL:  "https://bucket.oss.example/a.png?version=7&OSSAccessKeyId=one&Expires=100&Signature=aaa",
			secondURL: "https://bucket.oss.example/a.png?Signature=bbb&Expires=200&OSSAccessKeyId=two&version=7",
		},
		{
			name:      "Tencent COS",
			firstURL:  "https://bucket.cos.example/a.png?version=7&q-sign-algorithm=sha1&q-ak=one&q-key-time=1%3B2&q-signature=aaa",
			secondURL: "https://bucket.cos.example/a.png?q-signature=bbb&q-key-time=3%3B4&q-ak=two&q-sign-algorithm=sha1&version=7",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := func(rawURL string) []byte {
				return []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, rawURL))
			}
			first := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body(test.firstURL), settings)
			second := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body(test.secondURL), settings)

			assert.Equal(t, first.identityDigest, second.identityDigest)
		})
	}
}

func TestExtractSimulatedModelCachePromptUsesImageFormula(t *testing.T) {
	settings := simulatedModelCacheMultimodalTestSettings()
	dataURL := simulatedModelCachePNGDataURL(t, 1000, 500)
	body := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, dataURL))

	prompt := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body, settings)

	require.True(t, prompt.IsMultimodal())
	assert.Equal(t, uint64(260), simulatedModelCacheMediaWeight(prompt))
}

func TestExtractSimulatedModelCachePromptSupportsProviderMediaBlocks(t *testing.T) {
	settings := simulatedModelCacheMultimodalTestSettings()
	dataURL := simulatedModelCachePNGDataURL(t, 2, 3)
	base64Data := strings.TrimPrefix(dataURL, "data:image/png;base64,")
	tests := []struct {
		name   string
		format types.RelayFormat
		body   string
	}{
		{
			name:   "OpenAI Responses nested message",
			format: types.RelayFormatOpenAIResponses,
			body:   fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":%q}]}]}`, dataURL),
		},
		{
			name:   "Claude base64 source",
			format: types.RelayFormatClaude,
			body:   fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]}]}`, base64Data),
		},
		{
			name:   "Gemini inline data",
			format: types.RelayFormatGemini,
			body:   fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":%q}}]}]}`, base64Data),
		},
		{
			name:   "Gemini Cloud Storage file data",
			format: types.RelayFormatGemini,
			body:   `{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"video/mp4","fileUri":"gs://bucket/video.mp4"}}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt := ExtractSimulatedModelCachePrompt(test.format, "gpt-test", []byte(test.body), settings)

			require.True(t, prompt.IsMultimodal())
			assert.Empty(t, prompt.DiagnosticReason())
			assert.Greater(t, simulatedModelCacheMediaWeight(prompt), uint64(0))
		})
	}
}

func TestExtractSimulatedModelCachePromptUsesMediaFormulasAndFallbacks(t *testing.T) {
	settings := simulatedModelCacheMultimodalTestSettings()
	pcm16 := base64.StdEncoding.EncodeToString(make([]byte, 96_000))
	fileData := base64.StdEncoding.EncodeToString(make([]byte, 256*1024))
	tests := []struct {
		name       string
		format     types.RelayFormat
		body       string
		wantWeight uint64
	}{
		{
			name:       "video metadata formula",
			format:     types.RelayFormatOpenAI,
			body:       `{"messages":[{"role":"user","content":[{"type":"input_video","video_url":"https://example.com/video.mp4","width":1920,"height":1080,"duration":2}]}]}`,
			wantWeight: 3401,
		},
		{
			name:       "PCM audio duration formula",
			format:     types.RelayFormatOpenAI,
			body:       fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":%q,"format":"pcm16"}}]}]}`, pcm16),
			wantWeight: 50,
		},
		{
			name:       "inline file size formula",
			format:     types.RelayFormatOpenAIResponses,
			body:       fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_data":%q,"filename":"report.pdf"}]}]}`, fileData),
			wantWeight: 1024,
		},
		{
			name:       "video fallback",
			format:     types.RelayFormatOpenAI,
			body:       `{"messages":[{"role":"user","content":[{"type":"input_video","video_url":"https://example.com/video.mp4"}]}]}`,
			wantWeight: 8192,
		},
		{
			name:       "audio fallback",
			format:     types.RelayFormatOpenAI,
			body:       `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"url":"https://example.com/audio.wav"}}]}]}`,
			wantWeight: 256,
		},
		{
			name:       "file fallback",
			format:     types.RelayFormatOpenAIResponses,
			body:       `{"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_url":"https://example.com/report.pdf"}]}]}`,
			wantWeight: 4096,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt := ExtractSimulatedModelCachePrompt(test.format, "gpt-test", []byte(test.body), settings)

			require.True(t, prompt.IsMultimodal())
			assert.Empty(t, prompt.DiagnosticReason())
			assert.Equal(t, test.wantWeight, simulatedModelCacheMediaWeight(prompt))
		})
	}
}

func TestSimulatedModelCacheMultimodalBarrierStopsMatchingAtInvalidMedia(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	settings := simulatedModelCacheMultimodalTestSettings()
	valid := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"prefix"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}},{"type":"text","text":"suffix"}]}]}`), settings)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 9, Model: "gpt-test", Prompt: valid, TTLSeconds: 60,
	}))
	invalid := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"prefix"},{"type":"image_url","image_url":{"url":"not-a-url"}},{"type":"text","text":"suffix"}]}]}`), settings)

	match, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 9, Model: "gpt-test", Prompt: invalid, MinMatchRatio: 0.01,
	})

	require.NoError(t, err)
	assert.Equal(t, "invalid_image", invalid.DiagnosticReason())
	assert.True(t, match.Found)
	assert.Greater(t, match.MatchRatio, 0.0)
	assert.Less(t, match.MatchRatio, 1.0)
}

func TestSimulatedModelCacheMultimodalSettingsDigestIsolatesRedisScopes(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	firstSettings := simulatedModelCacheMultimodalTestSettings()
	secondSettings := simulatedModelCacheMultimodalTestSettings()
	secondRate := 521.0
	secondSettings.Multimodal.ImageTokensPerMegapixel = &secondRate
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`)
	first := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body, firstSettings)
	second := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body, secondSettings)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 10, Model: "gpt-test", Prompt: first, TTLSeconds: 60,
	}))

	match, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 10, Model: "gpt-test", Prompt: second, MinMatchRatio: 0.01,
	})

	require.NoError(t, err)
	assert.False(t, match.Found)
	assert.Zero(t, match.CandidateCount)
}

func TestSimulatedModelCacheRedisDoesNotStoreRawMediaReferences(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	settings := simulatedModelCacheMultimodalTestSettings()
	dataURL := simulatedModelCachePNGDataURL(t, 2, 3)
	references := []string{
		"https://private.example/sensitive.png?tenant=secret",
		"file-secret-123",
		dataURL,
	}
	bodies := [][]byte{
		[]byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, references[0])),
		[]byte(fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":%q}]}]}`, references[1])),
		[]byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, references[2])),
	}
	formats := []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatOpenAI}
	for index, body := range bodies {
		prompt := ExtractSimulatedModelCachePrompt(formats[index], "gpt-test", body, settings)
		require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
			UserID: 11, Model: "gpt-test", Prompt: prompt, TTLSeconds: 60,
		}))
	}

	keys, err := common.RDB.Keys(ctx, simulatedModelCacheKeyPrefix+":*").Result()
	require.NoError(t, err)
	var stored strings.Builder
	for _, key := range keys {
		if common.RDB.Type(ctx, key).Val() != "string" {
			continue
		}
		value, getErr := common.RDB.Get(ctx, key).Result()
		require.NoError(t, getErr)
		stored.WriteString(value)
	}
	for _, reference := range references {
		assert.NotContains(t, stored.String(), reference)
	}
}

func TestSimulatedModelCacheMultimodalStrictPrefixAndMediaOnlyRedisMatch(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	settings := simulatedModelCacheMultimodalTestSettings()
	body := func(text string, url string) []byte {
		return []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"text","text":%q},{"type":"image_url","image_url":{"url":%q}}]}]}`, text, url))
	}
	stored := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("shared prefix", "https://example.com/a.png"), settings)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 7, Model: "gpt-test", Prompt: stored, TTLSeconds: 60,
	}))

	exact, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 7, Model: "gpt-test", Prompt: stored, MinMatchRatio: 0.01,
	})
	require.NoError(t, err)
	assert.True(t, exact.Found)
	assert.Equal(t, 1.0, exact.MatchRatio)

	differentMedia := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("shared prefix", "https://example.com/b.png"), settings)
	partial, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 7, Model: "gpt-test", Prompt: differentMedia, MinMatchRatio: 0.01,
	})
	require.NoError(t, err)
	assert.True(t, partial.Found)
	assert.Greater(t, partial.MatchRatio, 0.0)
	assert.Less(t, partial.MatchRatio, 1.0)

	mediaOnly := ExtractSimulatedModelCachePrompt(types.RelayFormatOpenAI, "gpt-test", body("", "https://example.com/media-only.png"), settings)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 8, Model: "gpt-test", Prompt: mediaOnly, TTLSeconds: 60,
	}))
	mediaOnlyMatch, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 8, Model: "gpt-test", Prompt: mediaOnly, MinMatchRatio: 1,
	})
	require.NoError(t, err)
	assert.True(t, mediaOnlyMatch.Found)
	assert.Equal(t, 1.0, mediaOnlyMatch.MatchRatio)
}

func TestSimulatedModelCacheMatchRatioUsesCurrentPromptLength(t *testing.T) {
	assert.Equal(t, 1.0, SimulatedModelCacheMatchRatio("same", "same"))
	assert.Equal(t, 0.0, SimulatedModelCacheMatchRatio("anything", "different"))
	assert.Equal(t, 0.0, SimulatedModelCacheMatchRatio("anything", ""))
}

func TestSimulatedModelCacheMatchRatioMatchesShortPromptTrigrams(t *testing.T) {
	assert.InDelta(t, 6.0/7.0, SimulatedModelCacheMatchRatio("hello AA", "hello B"), 0.000001)
	assert.InDelta(t, 6.0/8.0, SimulatedModelCacheMatchRatio("hello B", "hello AA"), 0.000001)
	assert.InDelta(t, 4.0/5.0, SimulatedModelCacheMatchRatio("你好世界甲", "你好世界乙"), 0.000001)
	assert.InDelta(t, 6.0/9.0, SimulatedModelCacheMatchRatio("QabcabcR", "abcabcXYZ"), 0.000001)
}

func TestSimulatedModelCacheMatchRatioKeepsVeryShortPromptsExactOnly(t *testing.T) {
	assert.Equal(t, 1.0, SimulatedModelCacheMatchRatio("你好", "你好"))
	assert.Equal(t, 0.0, SimulatedModelCacheMatchRatio("你们", "你好"))
}

func TestSimulatedModelCacheFingerprintMatcherUsesCurrentPromptRunes(t *testing.T) {
	current := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 256,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 3, RuneLength: 64},
			{HashHigh: 4, RuneLength: 64},
		},
	}
	candidate := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 192,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 9, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 3, RuneLength: 64},
		},
	}

	ratio := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)

	assert.Equal(t, 0.5, ratio)
}

func TestBuildSimulatedModelCachePromptFingerprintHandlesUnicodeAndBoundaries(t *testing.T) {
	prompt := strings.Repeat("你好🌍", 400)
	fingerprint, err := buildSimulatedModelCachePromptFingerprint(context.Background(), prompt)

	require.NoError(t, err)
	assert.Equal(t, 1200, fingerprint.TotalRunes)
	require.NotEmpty(t, fingerprint.Chunks)
	for index, chunk := range fingerprint.Chunks {
		if index < len(fingerprint.Chunks)-1 {
			assert.GreaterOrEqual(t, int(chunk.RuneLength), simulatedModelCacheFingerprintMinRunes)
		}
		assert.LessOrEqual(t, int(chunk.RuneLength), simulatedModelCacheFingerprintMaxRunes)
	}
	assert.Equal(t, 1.0, SimulatedModelCacheMatchRatio(prompt, prompt))
}

func TestBuildSimulatedModelCachePromptFingerprintStoresFineHashesOnlyThroughLimit(t *testing.T) {
	shortPrompt := strings.Repeat("你", simulatedModelCacheFineFingerprintMaxRunes)
	shortFingerprint, err := buildSimulatedModelCachePromptFingerprint(context.Background(), shortPrompt)
	require.NoError(t, err)
	assert.Len(
		t,
		shortFingerprint.FineHashes,
		(simulatedModelCacheFineFingerprintMaxRunes-simulatedModelCacheFineFingerprintWindowRunes+1)*simulatedModelCacheFineFingerprintHashBytes,
	)
	assert.True(t, shortFingerprint.hasValidFineHashes())
	raw, err := common.Marshal(shortFingerprint)
	require.NoError(t, err)
	assert.Less(t, len(raw), simulatedModelCacheMaxFingerprintEncodedBytes)
	assert.NotContains(t, string(raw), shortPrompt)

	longPrompt := strings.Repeat("你", simulatedModelCacheFineFingerprintMaxRunes+1)
	longFingerprint, err := buildSimulatedModelCachePromptFingerprint(context.Background(), longPrompt)
	require.NoError(t, err)
	assert.Empty(t, longFingerprint.FineHashes)
	assert.True(t, longFingerprint.hasValidFineHashes())
}

func TestSimulatedModelCacheFingerprintFallsBackToCoarseAcrossFineLimit(t *testing.T) {
	current, err := buildSimulatedModelCachePromptFingerprint(
		context.Background(),
		strings.Repeat("abcd", simulatedModelCacheFineFingerprintMaxRunes/4),
	)
	require.NoError(t, err)
	candidate, err := buildSimulatedModelCachePromptFingerprint(
		context.Background(),
		"Z"+strings.Repeat("abcd", simulatedModelCacheFineFingerprintMaxRunes/4),
	)
	require.NoError(t, err)
	require.NotEmpty(t, current.FineHashes)
	require.Empty(t, candidate.FineHashes)

	got := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)
	current.FineHashes = nil
	want := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)

	assert.Equal(t, want, got)
}

func TestSimulatedModelCacheFingerprintKeepsMatchesAroundLocalizedChanges(t *testing.T) {
	base := strings.Repeat("abcdefghij", 600)
	changed := base[:2500] + strings.Repeat("Z", 80) + base[2580:]

	ratio := SimulatedModelCacheMatchRatio(base, changed)

	assert.Greater(t, ratio, 0.5)
	assert.Less(t, ratio, 1.0)
}

func TestSimulatedModelCacheFingerprintResynchronizesAfterUnicodeInsertions(t *testing.T) {
	current := strings.Repeat("你好世界🌍", 400)
	cached := strings.Repeat("前", 100) + current + strings.Repeat("后", 100)

	ratio := SimulatedModelCacheMatchRatio(cached, current)

	assert.Greater(t, ratio, 0.7)
}

func TestSimulatedModelCacheFingerprintFindsLongestRepeatedBlockSequence(t *testing.T) {
	current := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 384,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 3, RuneLength: 64},
			{HashHigh: 4, RuneLength: 64},
		},
	}
	candidate := simulatedModelCachePromptFingerprint{
		Version:    SimulatedModelCacheFingerprintVersion,
		TotalRunes: 384,
		Chunks: []simulatedModelCacheFingerprintChunk{
			{HashHigh: 9, RuneLength: 64},
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 1, RuneLength: 64},
			{HashHigh: 2, RuneLength: 64},
			{HashHigh: 8, RuneLength: 64},
		},
	}

	ratio := newSimulatedModelCacheFingerprintMatcher(current).match(context.Background(), candidate)

	assert.InDelta(t, 2.0/3.0, ratio, 0.000001)
}

func TestBuildSimulatedModelCachePromptFingerprintRejectsOversizedPrompt(t *testing.T) {
	_, err := buildSimulatedModelCachePromptFingerprint(context.Background(), strings.Repeat("a", simulatedModelCacheMaxFingerprintRunes+1))

	assert.ErrorIs(t, err, errSimulatedModelCachePromptTooLarge)
}

func TestBuildSimulatedModelCachePromptFingerprintStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := buildSimulatedModelCachePromptFingerprint(ctx, strings.Repeat("a", 1024))

	assert.ErrorIs(t, err, context.Canceled)
}

func TestApplySimulatedModelCacheUsageRewritePreservesPromptTotal(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 7,
		TotalTokens:      107,
		InputTokens:      100,
		OutputTokens:     7,
	}

	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_fingerprint",
		MatchRatio: 0.25,
	})

	require.NotNil(t, marker)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Equal(t, 25, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.TotalTokens-usage.CompletionTokens)
	assert.Equal(t, 100, marker.OriginalPromptTokens)
	assert.Equal(t, 75, marker.SimulatedPromptTokens)
	assert.Equal(t, 25, marker.SimulatedCachedTokens)
}

func TestApplySimulatedModelCacheUsageRewriteUsesAnthropicUsageSemantics(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 7,
		TotalTokens:      107,
		UsageSemantic:    "anthropic",
	}

	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_fingerprint",
		MatchRatio: 0.25,
	})

	require.NotNil(t, marker)
	assert.Equal(t, 75, usage.PromptTokens)
	assert.Equal(t, 25, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 107, usage.TotalTokens)
	assert.Equal(t, 100, marker.OriginalPromptTokens)
	assert.Equal(t, 75, marker.SimulatedPromptTokens)
	assert.Equal(t, 25, marker.SimulatedCachedTokens)
}

func TestApplySimulatedModelCacheUsageRewriteUpdatesBillingUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 7,
		TotalTokens:      107,
		BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
			InputTokens:  100,
			OutputTokens: 7,
		}),
	}

	ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{MatchRatio: 0.25})
	billingUsage := effectiveBillingUsage(usage)

	assert.Equal(t, 75, billingUsage.PromptTokens)
	assert.Equal(t, 25, billingUsage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 107, billingUsage.TotalTokens)
}

func TestApplySimulatedModelCacheUsageRewriteReplacesCacheCreationInBillingUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 7,
		TotalTokens:      127,
		UsageSemantic:    UsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 20,
		},
		ClaudeCacheCreation5mTokens: 8,
		ClaudeCacheCreation1hTokens: 12,
		BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             7,
			CacheCreationInputTokens: 20,
			CacheCreation: &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 8,
				Ephemeral1hInputTokens: 12,
			},
		}),
	}

	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{MatchRatio: 0.25})
	billingUsage := effectiveBillingUsage(usage)

	require.NotNil(t, marker)
	assert.Equal(t, 120, marker.OriginalPromptTokens)
	assert.Equal(t, 30, marker.SimulatedCachedTokens)
	assert.Equal(t, 90, billingUsage.PromptTokens)
	assert.Equal(t, 30, billingUsage.PromptTokensDetails.CachedTokens)
	assert.Zero(t, billingUsage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 127, billingUsage.TotalTokens)
	require.NotNil(t, usage.BillingUsage.ClaudeUsage)
	assert.Zero(t, usage.BillingUsage.ClaudeUsage.CacheCreationInputTokens)
	assert.Nil(t, usage.BillingUsage.ClaudeUsage.CacheCreation)
}

func TestApplySimulatedModelCacheUsageRewriteKeepsGeminiToolTokensInTotalOnce(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 12,
		TotalTokens:      112,
		BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
			PromptTokenCount:        80,
			ToolUsePromptTokenCount: 20,
			CandidatesTokenCount:    10,
			ThoughtsTokenCount:      2,
			TotalTokenCount:         112,
		}),
	}

	ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{MatchRatio: 0.25})
	billingUsage := effectiveBillingUsage(usage)

	assert.Equal(t, 100, billingUsage.PromptTokens)
	assert.Equal(t, 25, billingUsage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 112, billingUsage.TotalTokens)
	require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
	assert.Zero(t, usage.BillingUsage.GeminiUsageMetadata.ToolUsePromptTokenCount)
}

func TestSimulatedModelCacheFingerprintIndexKeepsRecentUniquePromptsPerUserAndModel(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	originalMaxEntries := common.GetSimulatedModelCacheEntriesPerScope()
	common.SetSimulatedModelCacheEntriesPerScope(3)
	t.Cleanup(func() {
		common.SetSimulatedModelCacheEntriesPerScope(originalMaxEntries)
	})
	const userID = 4242
	const otherUserID = 4243
	const model = "gpt-test"
	const otherModel = "other-test"

	for i := 0; i < 4; i++ {
		prompt := fmt.Sprintf("prompt %03d %s", i, strings.Repeat("content ", 20))
		err := StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
			UserID:     userID,
			Model:      model,
			PromptText: prompt,
			TTLSeconds: 86400,
		})
		require.NoError(t, err)
	}

	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     otherUserID,
		Model:      model,
		PromptText: strings.Repeat("other user ", 20),
		TTLSeconds: 86400,
	}))
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      otherModel,
		PromptText: strings.Repeat("other model ", 20),
		TTLSeconds: 86400,
	}))

	indexKey := simulatedModelCacheScopeIndexKey(0, userID, model)
	promptIDs, err := common.RDB.ZRange(ctx, indexKey, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, promptIDs, 3)
	assert.NotContains(t, promptIDs, sha256Hex([]byte(fmt.Sprintf("prompt %03d %s", 0, strings.Repeat("content ", 20)))))
	assert.Contains(t, promptIDs, sha256Hex([]byte(fmt.Sprintf("prompt %03d %s", 3, strings.Repeat("content ", 20)))))

	otherUserCount, err := common.RDB.ZCard(ctx, simulatedModelCacheScopeIndexKey(0, otherUserID, model)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), otherUserCount)
	otherModelCount, err := common.RDB.ZCard(ctx, simulatedModelCacheScopeIndexKey(0, userID, otherModel)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), otherModelCount)
}

func TestSimulatedModelCacheV5StoresOneFingerprintPerPromptAndKeepsOlderVersionsUntouched(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const prompt = "shared prompt "
	promptText := strings.Repeat(prompt, 20)
	v1Key := "simulated_model_cache:v1:legacy"
	v2Key := "simulated_model_cache:v2:legacy"
	v4Key := "simulated_model_cache:v4:legacy"
	require.NoError(t, common.RDB.Set(ctx, v1Key, "legacy", 0).Err())
	require.NoError(t, common.RDB.Set(ctx, v2Key, "legacy", 0).Err())
	require.NoError(t, common.RDB.Set(ctx, v4Key, "legacy", 0).Err())

	for i := 0; i < 2; i++ {
		require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
			UserID:     1,
			Model:      "gpt-test",
			PromptText: promptText,
			TTLSeconds: 60,
		}))
	}

	indexKey := simulatedModelCacheScopeIndexKey(0, 1, "gpt-test")
	indexCount, err := common.RDB.ZCard(ctx, indexKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), indexCount)
	fingerprintKey := simulatedModelCacheFingerprintKey(0, 1, "gpt-test", sha256Hex([]byte(promptText)))
	raw, err := common.RDB.Get(ctx, fingerprintKey).Result()
	require.NoError(t, err)
	assert.NotContains(t, raw, promptText)
	legacy, err := common.RDB.Get(ctx, v1Key).Result()
	require.NoError(t, err)
	assert.Equal(t, "legacy", legacy)
	legacy, err = common.RDB.Get(ctx, v2Key).Result()
	require.NoError(t, err)
	assert.Equal(t, "legacy", legacy)
	legacy, err = common.RDB.Get(ctx, v4Key).Result()
	require.NoError(t, err)
	assert.Equal(t, "legacy", legacy)
}

func TestSimulatedModelCacheV5IgnoresV4FingerprintIndexes(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	promptText := strings.Repeat("legacy prompt ", 20)
	promptID := sha256Hex([]byte(promptText))
	legacyFingerprint, err := buildSimulatedModelCachePromptFingerprint(ctx, promptText)
	require.NoError(t, err)
	legacyFingerprint.Version = "v4"
	raw, err := common.Marshal(legacyFingerprint)
	require.NoError(t, err)
	legacyIndexKey := strings.Replace(simulatedModelCacheScopeIndexKey(0, 12, "gpt-test"), simulatedModelCacheKeyPrefix, "simulated_model_cache:v4", 1)
	legacyFingerprintKey := strings.Replace(simulatedModelCacheFingerprintKey(0, 12, "gpt-test", promptID), simulatedModelCacheKeyPrefix, "simulated_model_cache:v4", 1)
	require.NoError(t, common.RDB.Set(ctx, legacyFingerprintKey, raw, time.Minute).Err())
	require.NoError(t, common.RDB.ZAdd(ctx, legacyIndexKey, &redis.Z{Score: 1, Member: promptID}).Err())

	match, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID: 12, Model: "gpt-test", PromptText: promptText, MinMatchRatio: 0.01,
	})

	require.NoError(t, err)
	assert.False(t, match.Found)
	assert.Zero(t, match.CandidateCount)
	assert.Equal(t, "string", common.RDB.Type(ctx, legacyFingerprintKey).Val())
}

func TestSimulatedModelCachePartialFingerprintMatchesShortPrompts(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "short-forward",
		PromptText: "hello AA",
		TTLSeconds: 60,
	}))

	forward, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:        10,
		Model:         "short-forward",
		PromptText:    "hello B",
		MinMatchRatio: 0.8,
	})
	require.NoError(t, err)
	assert.True(t, forward.Found)
	assert.InDelta(t, 6.0/7.0, forward.MatchRatio, 0.000001)
	assert.Equal(t, SimulatedModelCacheFingerprintVersion, forward.FingerprintVersion)

	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "short-reverse",
		PromptText: "hello B",
		TTLSeconds: 60,
	}))
	reverse, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:        10,
		Model:         "short-reverse",
		PromptText:    "hello AA",
		MinMatchRatio: 0.7,
	})
	require.NoError(t, err)
	assert.True(t, reverse.Found)
	assert.InDelta(t, 6.0/8.0, reverse.MatchRatio, 0.000001)
}

func TestSimulatedModelCachePartialFingerprintMatchIsScopedByUserAndModel(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	prompt := strings.Repeat("scope content ", 100)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: prompt,
		TTLSeconds: 60,
	}))

	matched, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:        10,
		Model:         "gpt-test",
		PromptText:    prompt,
		MinMatchRatio: 1,
	})
	require.NoError(t, err)
	assert.True(t, matched.Found)
	assert.Equal(t, 1.0, matched.MatchRatio)
	assert.Equal(t, 1, matched.CandidateCount)

	for _, req := range []SimulatedModelCachePartialMatchRequest{
		{UserID: 11, Model: "gpt-test", PromptText: prompt},
		{UserID: 10, Model: "other-model", PromptText: prompt},
	} {
		result, findErr := FindSimulatedModelCachePartialMatch(ctx, req)
		require.NoError(t, findErr)
		assert.False(t, result.Found)
		assert.Zero(t, result.CandidateCount)
	}
}

func TestSimulatedModelCacheSelectsMaximumSimilarityAcrossKeyBindings(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const channelID = 77
	const userID = 10
	const modelName = "gpt-test"
	keyA := "digest-a"
	keyB := "digest-b"
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		ChannelID: channelID, UserID: userID, Model: modelName, PromptText: "abcdefghXX", TTLSeconds: 60, KeyDigest: keyA,
	}))
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		ChannelID: channelID, UserID: userID, Model: modelName, PromptText: "abcdefghijYY", TTLSeconds: 60, KeyDigest: keyB,
	}))

	result, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		ChannelID:         channelID,
		UserID:            userID,
		Model:             modelName,
		PromptText:        "abcdefghij",
		MinMatchRatio:     0.1,
		AllowedKeyDigests: map[string]struct{}{keyA: {}, keyB: {}},
	})

	require.NoError(t, err)
	assert.True(t, result.Found)
	assert.Equal(t, []string{keyB}, result.PreferredKeyDigests)
	assert.InDelta(t, 1, result.MatchRatio, 0.000001)
}

func TestSimulatedModelCacheScopeIncludesChannel(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	prompt := strings.Repeat("channel scoped prompt ", 10)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		ChannelID: 1, UserID: 10, Model: "gpt-test", PromptText: prompt, TTLSeconds: 60, KeyDigest: "digest-a",
	}))

	result, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		ChannelID: 2, UserID: 10, Model: "gpt-test", PromptText: prompt,
	})

	require.NoError(t, err)
	assert.False(t, result.Found)
	assert.Zero(t, result.CandidateCount)
}

func TestSimulatedModelCacheSharedScopeTTLIsNotShortenedByAnotherChannelSetting(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const userID = 10
	const model = "gpt-test"

	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      model,
		PromptText: strings.Repeat("long ttl prompt ", 20),
		TTLSeconds: 3600,
	}))
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     userID,
		Model:      model,
		PromptText: strings.Repeat("short ttl prompt ", 20),
		TTLSeconds: 60,
	}))

	ttl, err := common.RDB.TTL(ctx, simulatedModelCacheScopeIndexKey(0, userID, model)).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, 30*time.Minute)
}

func TestSimulatedModelCachePartialMatchPrunesMissingFingerprintMembers(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	prompt := strings.Repeat("expired fingerprint ", 20)
	require.NoError(t, StoreSimulatedModelCachePromptFingerprint(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: prompt,
		TTLSeconds: 60,
	}))

	promptID := sha256Hex([]byte(prompt))
	require.NoError(t, common.RDB.Del(ctx, simulatedModelCacheFingerprintKey(0, 10, "gpt-test", promptID)).Err())
	result, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: strings.Repeat("current prompt ", 20),
	})
	require.NoError(t, err)
	assert.False(t, result.Found)

	count, err := common.RDB.ZCard(ctx, simulatedModelCacheScopeIndexKey(0, 10, "gpt-test")).Result()
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSimulatedModelCachePartialMatchPrunesInvalidFineFingerprint(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	const prompt = "hello AA"
	fingerprint, err := buildSimulatedModelCachePromptFingerprint(ctx, prompt)
	require.NoError(t, err)
	require.NotEmpty(t, fingerprint.FineHashes)
	fingerprint.FineHashes = fingerprint.FineHashes[:len(fingerprint.FineHashes)-1]
	raw, err := common.Marshal(fingerprint)
	require.NoError(t, err)
	promptID := sha256Hex([]byte(prompt))
	require.NoError(t, common.RDB.Set(ctx, simulatedModelCacheFingerprintKey(0, 10, "gpt-test", promptID), raw, time.Minute).Err())
	indexKey := simulatedModelCacheScopeIndexKey(0, 10, "gpt-test")
	require.NoError(t, common.RDB.ZAdd(ctx, indexKey, &redis.Z{Score: 1, Member: promptID}).Err())

	result, err := FindSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: "hello B",
	})
	require.NoError(t, err)
	assert.False(t, result.Found)

	count, err := common.RDB.ZCard(ctx, indexKey).Result()
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSimulatedModelCacheWorkerReturnsPreparedFingerprintWithoutStoring(t *testing.T) {
	ctx := withSimulatedModelCacheTestRedis(t)
	prompt := strings.Repeat("worker query only prompt ", 20)
	handle, bypassReason := SubmitSimulatedModelCachePartialMatch(ctx, SimulatedModelCachePartialMatchRequest{
		UserID:     10,
		Model:      "gpt-test",
		PromptText: prompt,
	})
	require.Empty(t, bypassReason)
	require.NotNil(t, handle)

	result := <-handle.result
	require.NoError(t, result.Err)
	require.NotNil(t, result.Prepared)
	exists, err := common.RDB.Exists(ctx, simulatedModelCacheFingerprintKey(0, 10, "gpt-test", sha256Hex([]byte(prompt)))).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)

	reservation := ReserveSimulatedModelCacheMemory(common.GetSimulatedModelCacheMemoryBudgetBytes())
	require.NotNil(t, reservation)
	defer reservation.Release()
	require.NoError(t, result.Prepared.Store(ctx), "prepared storage must not reserve or rebuild the fingerprint")
	exists, err = common.RDB.Exists(ctx, simulatedModelCacheFingerprintKey(0, 10, "gpt-test", sha256Hex([]byte(prompt)))).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
}

func withSimulatedModelCacheTestRedis(t *testing.T) context.Context {
	t.Helper()

	redisURL := os.Getenv("NEW_API_TEST_REDIS_CONN_STRING")
	var opt *redis.Options
	if redisURL != "" {
		parsed, err := redis.ParseURL(redisURL)
		require.NoError(t, err)
		opt = parsed
	} else {
		server := miniredis.RunT(t)
		opt = &redis.Options{Addr: server.Addr()}
	}

	oldRedisEnabled := common.RedisEnabled
	oldRDB := common.RDB
	client := redis.NewClient(opt)
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	require.NoError(t, client.FlushDB(ctx).Err())

	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		_ = client.FlushDB(ctx).Err()
		_ = client.Close()
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRDB
	})
	return ctx
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesOpenAIUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		TotalTokens:      108,
	}
	ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_fingerprint",
		MatchRatio: 0.25,
	})

	body := []byte(`{"id":"chatcmpl_test","usage":{"prompt_tokens":100,"completion_tokens":8,"total_tokens":108}}`)
	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatOpenAI, "application/json", body, usage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(patched, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usageMap["prompt_tokens"])
	assert.Equal(t, float64(108), usageMap["total_tokens"])
	details, ok := usageMap["prompt_tokens_details"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(25), details["cached_tokens"])
}

func TestPatchSimulatedModelCacheResponseBodyProductionUsageContracts(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens: 1026, CompletionTokens: 220, UsageSemantic: UsageSemanticAnthropic,
	}
	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{MatchRatio: 1})
	require.NotNil(t, marker)

	claudeBody := PatchSimulatedModelCacheResponseBody(
		types.RelayFormatClaude,
		"application/json",
		[]byte(`{"usage":{}}`),
		usage,
	)
	var claudePayload struct {
		Usage dto.ClaudeUsage `json:"usage"`
	}
	require.NoError(t, common.Unmarshal(claudeBody, &claudePayload))
	assert.Equal(t, 0, claudePayload.Usage.InputTokens)
	assert.Equal(t, 1026, claudePayload.Usage.CacheReadInputTokens)
	assert.Equal(t, 0, claudePayload.Usage.CacheCreationInputTokens)
	assert.Equal(t, 220, claudePayload.Usage.OutputTokens)

	openAIBody := PatchSimulatedModelCacheResponseBody(
		types.RelayFormatOpenAI,
		"application/json",
		[]byte(`{"usage":{}}`),
		usage,
	)
	var openAIPayload struct {
		Usage dto.Usage `json:"usage"`
	}
	require.NoError(t, common.Unmarshal(openAIBody, &openAIPayload))
	assert.Equal(t, 1026, openAIPayload.Usage.PromptTokens)
	assert.Equal(t, 220, openAIPayload.Usage.CompletionTokens)
	assert.Equal(t, 1246, openAIPayload.Usage.TotalTokens)
	assert.Equal(t, 1026, openAIPayload.Usage.PromptTokensDetails.CachedTokens)
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesOpenAIModelInSSE(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2,
		CompletionTokens: 3,
		TotalTokens:      5,
	}
	body := []byte(strings.Join([]string{
		`data: {"id":"chatcmpl_test","model":"xopglm52","choices":[{"delta":{"content":"ok"},"index":0}]}`,
		`data: {"id":"chatcmpl_test","model":"xopglm52","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n"))

	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatOpenAI, "text/event-stream", body, usage, "glm-5.2")

	got := string(patched)
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
	require.Contains(t, got, `data: [DONE]`)
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesResponsesModelInSSE(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2,
		CompletionTokens: 3,
		TotalTokens:      5,
	}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_test","model":"xopglm52"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_test","model":"xopglm52","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n"))

	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatOpenAIResponses, "text/event-stream", body, usage, "glm-5.2")

	got := string(patched)
	require.Contains(t, got, `"model":"glm-5.2"`)
	require.NotContains(t, got, `"model":"xopglm52"`)
	require.Contains(t, got, `data: [DONE]`)
}

func TestPatchSimulatedModelCacheResponseBodyUpdatesClaudeUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		TotalTokens:      108,
		UsageSemantic:    "anthropic",
	}
	marker := ApplySimulatedModelCacheUsageRewrite(usage, SimulatedModelCacheUsageRewrite{
		Mode:       "partial_fingerprint",
		MatchRatio: 0.25,
	})
	require.NotNil(t, marker)
	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":8}}`)
	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatClaude, "application/json", body, usage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(patched, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(75), usageMap["input_tokens"])
	assert.Equal(t, float64(25), usageMap["cache_read_input_tokens"])
	assert.Equal(t, float64(8), usageMap["output_tokens"])
	assert.Equal(t, float64(0), usageMap["cache_creation_input_tokens"])
	assert.Equal(t, float64(0), usageMap["claude_cache_creation_5_m_tokens"])
	assert.Equal(t, float64(0), usageMap["claude_cache_creation_1_h_tokens"])
	assert.Equal(t, 75, usage.PromptTokens, "response formatting must not restore billing prompt tokens")
	assert.Equal(t, 25, usage.PromptTokensDetails.CachedTokens)
}

func TestPatchSimulatedModelCacheResponseBodyUsesNormalizedClaudeCacheCreationFields(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 8,
		UsageSemantic:    "anthropic",
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         25,
			CachedCreationTokens: 11,
		},
		ClaudeCacheCreation5mTokens: 4,
		ClaudeCacheCreation1hTokens: 7,
	}
	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":8,"cache_creation_input_tokens":99,"claude_cache_creation_5_m_tokens":98,"claude_cache_creation_1_h_tokens":97}}`)

	patched := PatchSimulatedModelCacheResponseBody(types.RelayFormatClaude, "application/json", body, usage)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(patched, &payload))
	usageMap, ok := payload["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(11), usageMap["cache_creation_input_tokens"])
	assert.Equal(t, float64(4), usageMap["claude_cache_creation_5_m_tokens"])
	assert.Equal(t, float64(7), usageMap["claude_cache_creation_1_h_tokens"])
}
