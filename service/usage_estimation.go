package service

import (
	"math"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func EstimateModelFamilyInputTokens(family dto.UsageEstimationModelFamily, format types.RelayFormat, meta *types.TokenCountMeta) int {
	if meta == nil {
		return 0
	}
	textTokens := EstimateModelFamilyTextTokens(family, meta.CombineText)
	tokens := saturatingTokenAdd(textTokens, usageEstimationFramingOverhead(format, meta))

	switch family {
	case dto.UsageEstimationModelFamilyKimi:
		for _, file := range meta.Files {
			if file != nil {
				return saturatingTokenAdd(tokens, 5001)
			}
		}
	default:
		for _, file := range meta.Files {
			if file == nil {
				continue
			}
			switch file.FileType {
			case types.FileTypeImage:
				tokens = saturatingTokenAdd(tokens, 520)
			case types.FileTypeAudio:
				tokens = saturatingTokenAdd(tokens, 256)
			case types.FileTypeVideo:
				tokens = saturatingTokenAdd(tokens, 8192)
			case types.FileTypeFile:
				tokens = saturatingTokenAdd(tokens, 4096)
			}
		}
	}
	return tokens
}

func EstimateModelFamilyTextTokens(family dto.UsageEstimationModelFamily, text string) int {
	if text == "" {
		return 0
	}
	switch family {
	case dto.UsageEstimationModelFamilyGLM:
		runes := utf8.RuneCountInString(text)
		return ceilTokenRatio(runes, 5, 8)
	case dto.UsageEstimationModelFamilyKimi:
		dense, other := 0, 0
		for _, character := range text {
			if isDenseScriptCharacter(character) {
				dense++
			} else {
				other++
			}
		}
		return saturatingTokenAdd(ceilTokenRatio(dense, 2, 3), ceilTokenRatio(other, 10, 31))
	case dto.UsageEstimationModelFamilyDeepSeek:
		// DeepSeek documents approximately 0.3 token per English character and
		// 0.6 token per Chinese character. Digits, symbols and other scripts use
		// one token each so an unusual response cannot be underestimated badly.
		tenths := int64(0)
		for _, character := range text {
			switch {
			case isDenseScriptCharacter(character):
				tenths += 6
			case unicode.IsLetter(character) || unicode.IsSpace(character):
				tenths += 3
			default:
				tenths += 10
			}
			if tenths >= int64(math.MaxInt32)*10 {
				return math.MaxInt32
			}
		}
		return int((tenths + 9) / 10)
	default:
		return 0
	}
}

func ScaleUsageEstimate(baseTokens int, multiplier float64) (int, *common.QuotaClamp) {
	if baseTokens <= 0 || multiplier <= 0 || math.IsNaN(multiplier) {
		return 0, nil
	}
	estimated, clamp := common.QuotaFromFloatChecked(math.Ceil(float64(baseTokens) * multiplier))
	if estimated < 1 {
		estimated = 1
	}
	return estimated, clamp
}

func ApplyMissingUsageEstimates(usage *dto.Usage, inputEstimate, outputEstimate int) (bool, bool) {
	if usage == nil {
		return false, false
	}
	normalized := NormalizeUsageForBilling(usage)
	inputApplied := normalized.InputTokens.TotalInputTokens <= 0 && inputEstimate > 0
	outputApplied := normalized.OutputTokens <= 0 && outputEstimate > 0
	if !inputApplied && !outputApplied {
		return false, false
	}

	finalInput := normalized.InputTokens.TotalInputTokens
	if inputApplied {
		finalInput = inputEstimate
		usage.PromptTokens = inputEstimate
		usage.InputTokens = inputEstimate
		usage.PromptTokensDetails.CachedTokens = 0
		usage.PromptTokensDetails.CachedCreationTokens = 0
		usage.PromptTokensDetails.CacheWriteTokens = 0
		if usage.InputTokensDetails != nil {
			usage.InputTokensDetails.CachedTokens = 0
			usage.InputTokensDetails.CachedCreationTokens = 0
			usage.InputTokensDetails.CacheWriteTokens = 0
		}
		usage.EstimatedInput = true
	}
	finalOutput := normalized.OutputTokens
	if outputApplied {
		finalOutput = outputEstimate
		usage.CompletionTokens = outputEstimate
		usage.OutputTokens = outputEstimate
		usage.EstimatedOutput = true
	}
	usage.TotalTokens = saturatingTokenAdd(finalInput, finalOutput)
	usage.Estimated = true

	if usage.BillingUsage == nil || !usage.BillingUsage.IsRecognized() {
		return inputApplied, outputApplied
	}
	usage.BillingUsage.Estimated = true
	switch {
	case usage.BillingUsage.OpenAIUsage != nil:
		billing := usage.BillingUsage.OpenAIUsage
		if inputApplied {
			billing.PromptTokens = inputEstimate
			billing.InputTokens = inputEstimate
			billing.EstimatedInput = true
		}
		if outputApplied {
			billing.CompletionTokens = outputEstimate
			billing.OutputTokens = outputEstimate
			billing.EstimatedOutput = true
		}
		billing.TotalTokens = usage.TotalTokens
		billing.Estimated = true
	case usage.BillingUsage.ClaudeUsage != nil:
		billing := usage.BillingUsage.ClaudeUsage
		if inputApplied {
			billing.InputTokens = inputEstimate
		}
		if outputApplied {
			billing.OutputTokens = outputEstimate
		}
	case usage.BillingUsage.GeminiUsageMetadata != nil:
		billing := usage.BillingUsage.GeminiUsageMetadata
		if inputApplied {
			billing.PromptTokenCount = inputEstimate
		}
		if outputApplied {
			billing.CandidatesTokenCount = outputEstimate
		}
		billing.TotalTokenCount = usage.TotalTokens
	}
	return inputApplied, outputApplied
}

func usageEstimationFramingOverhead(format types.RelayFormat, meta *types.TokenCountMeta) int {
	if meta == nil || format != types.RelayFormatOpenAI {
		return 0
	}
	return saturatingTokenAdd(meta.ToolsCount*8, meta.MessagesCount*3, meta.NameCount*3, 3)
}

func ceilTokenRatio(value, numerator, denominator int) int {
	if value <= 0 || numerator <= 0 || denominator <= 0 {
		return 0
	}
	result := (int64(value)*int64(numerator) + int64(denominator) - 1) / int64(denominator)
	if result > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(result)
}

func isDenseScriptCharacter(character rune) bool {
	return unicode.In(character, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		(character >= 0xFF00 && character <= 0xFFEF)
}
