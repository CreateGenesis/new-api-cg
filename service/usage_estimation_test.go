package service

import (
	"bytes"
	"encoding/base64"
	"github.com/gin-gonic/gin"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateModelFamilyInputTokensMatchesRoutingStandards(t *testing.T) {
	meta := &types.TokenCountMeta{
		CombineText:   "你a好bc世界d",
		ToolsCount:    1,
		MessagesCount: 2,
		NameCount:     1,
	}

	assert.Equal(t, 25, EstimateModelFamilyInputTokens(dto.UsageEstimationModelFamilyGLM, types.RelayFormatOpenAI, meta))
	assert.Equal(t, 25, EstimateModelFamilyInputTokens(dto.UsageEstimationModelFamilyKimi, types.RelayFormatOpenAI, meta))
}

func TestEstimateDeepSeekTextTokensUsesDocumentedCharacterRatios(t *testing.T) {
	assert.Equal(t, 3, EstimateModelFamilyTextTokens(dto.UsageEstimationModelFamilyDeepSeek, "abcdefghij"))
	assert.Equal(t, 3, EstimateModelFamilyTextTokens(dto.UsageEstimationModelFamilyDeepSeek, "一二三四五"))
	assert.Equal(t, 3, EstimateModelFamilyTextTokens(dto.UsageEstimationModelFamilyDeepSeek, "ab中1"))
}

func TestApplyMissingUsageEstimatesPreservesValidInputAndRepairsBillingOutput(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:  84,
		InputTokens:   84,
		UsageSemantic: UsageSemanticAnthropic,
		BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
			PromptTokens: 17748,
			InputTokens:  17748,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 17664,
			},
		}),
	}
	usage.PromptTokensDetails.CachedTokens = 17664

	inputApplied, outputApplied := ApplyMissingUsageEstimates(usage, 999, 23)

	assert.False(t, inputApplied)
	assert.True(t, outputApplied)
	assert.Equal(t, 84, usage.PromptTokens)
	assert.Equal(t, 23, usage.CompletionTokens)
	assert.True(t, usage.EstimatedOutput)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 17748, usage.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, 23, usage.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Equal(t, 17771, usage.BillingUsage.OpenAIUsage.TotalTokens)
	normalized := NormalizeUsageForBilling(usage)
	assert.Equal(t, 17748, normalized.InputTokens.TotalInputTokens)
	assert.Equal(t, 23, normalized.OutputTokens)
}

func TestKimiFrameEstimatesAndBase64CacheIsolation(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	var encoded []string
	var files []*types.FileMeta
	for _, size := range []image.Point{{1024, 576}, {512, 288}} {
		var buffer bytes.Buffer
		require.NoError(t, jpeg.Encode(&buffer, image.NewGray(image.Rect(0, 0, size.X, size.Y)), nil))
		data := base64.StdEncoding.EncodeToString(buffer.Bytes())
		source := types.NewBase64FileSource(data, "image/jpeg")
		config, _, err := GetImageConfig(c, source)
		require.NoError(t, err)
		assert.Equal(t, size.X, config.Width)
		assert.Equal(t, size.Y, config.Height)
		encoded = append(encoded, data)
		files = append(files, &types.FileMeta{FileType: types.FileTypeImage, Source: source})
	}
	assert.Equal(t, encoded[0][:128], encoded[1][:128], "fixture shares JPEG/base64 prefix")
	assert.NotEqual(t, getBase64ContextCacheKey(strings.Repeat("a", 128)+"a", "image/jpeg"), getBase64ContextCacheKey(strings.Repeat("a", 128)+"b", "image/jpeg"))
	tokens := EstimateModelFamilyInputTokens(dto.UsageEstimationModelFamilyKimi, types.RelayFormatClaude, &types.TokenCountMeta{Files: files})
	assert.Equal(t, (37*21+16)+(19*11+16), tokens)
}
