package service

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// TieredResultWrapper wraps billingexpr.TieredResult for use at the service layer.
type TieredResultWrapper = billingexpr.TieredResult

// BuildTieredTokenParams constructs billingexpr.TokenParams from a dto.Usage,
// normalizing P and C so they mean "tokens not separately priced by the
// expression". Sub-categories (cache, image, audio) are only subtracted
// when the expression references them via their own variable.
//
// When cacheUsageValidationSplit is enabled, P starts from canonical total
// input for every upstream protocol. Otherwise this preserves the legacy
// protocol-semantic behavior.
func BuildTieredTokenParams(usage *dto.Usage, isClaudeUsageSemantic bool, usedVars map[string]bool, cacheUsageValidationSplit bool) billingexpr.TokenParams {
	semantic := UsageSemanticOpenAI
	if usage != nil && (usage.UsageSemantic == UsageSemanticAnthropic ||
		usage.UsageSemantic == "" && isClaudeUsageSemantic) {
		semantic = UsageSemanticAnthropic
	}

	var normalizedUsage dto.Usage
	var input NormalizedInputTokens
	var p, c, img, ai, imgO, ao float64
	if cacheUsageValidationSplit {
		normalized := NormalizeUsageForBilling(usage)
		input = normalized.InputTokens
		p = float64(input.TotalInputTokens)
		c = float64(normalized.OutputTokens)
		img = float64(normalized.InputImageTokens)
		ai = float64(normalized.InputAudioTokens)
		imgO = float64(normalized.OutputImageTokens)
		ao = float64(normalized.OutputAudioTokens)
	} else {
		normalizedUsage = NormalizeUsageForSemantic(usage, semantic)
		input = NormalizeInputTokens(&normalizedUsage)
		p = float64(normalizedUsage.PromptTokens)
		c = float64(normalizedUsage.CompletionTokens)
		img = float64(normalizedUsage.PromptTokensDetails.ImageTokens)
		ai = float64(normalizedUsage.PromptTokensDetails.AudioTokens)
		imgO = float64(normalizedUsage.CompletionTokenDetails.ImageTokens)
		ao = float64(normalizedUsage.CompletionTokenDetails.AudioTokens)
	}
	cacheCreation5m, cacheCreation1h := NormalizeCacheCreationSplit(
		input.CacheCreationInputTokens,
		input.CacheCreation5mInputTokens,
		input.CacheCreation1hInputTokens,
	)

	cr := float64(input.CacheReadInputTokens)
	cc5m := float64(cacheCreation5m)
	cc1h := float64(cacheCreation1h)

	inputLen := float64(input.TotalInputTokens)

	if cacheUsageValidationSplit || semantic != UsageSemanticAnthropic {
		if usedVars["cr"] {
			p -= cr
		}
		if usedVars["cc"] {
			p -= cc5m
		}
		if usedVars["cc1h"] {
			p -= cc1h
		}
		if usedVars["img"] {
			p -= img
		}
		if usedVars["ai"] {
			p -= ai
		}
		if usedVars["img_o"] {
			c -= imgO
		}
		if usedVars["ao"] {
			c -= ao
		}
	}

	// OpenAI cache-write usage reports unadjusted prefix counts, so cr + cc can
	// exceed the prompt and drive the remainder negative. Clamp at zero.
	if p < 0 {
		p = 0
	}
	if c < 0 {
		c = 0
	}

	return billingexpr.TokenParams{
		P:    p,
		C:    c,
		Len:  inputLen,
		CR:   cr,
		CC:   cc5m,
		CC1h: cc1h,
		Img:  img,
		ImgO: imgO,
		AI:   ai,
		AO:   ao,
	}
}

// TryTieredSettle checks if the request uses tiered_expr billing and, if so,
// computes the actual quota using the frozen BillingSnapshot. Returns:
//   - ok=true, quota, result  when tiered billing applies
//   - ok=false, 0, nil        when it doesn't (caller should fall through to existing logic)
func TryTieredSettle(relayInfo *relaycommon.RelayInfo, params billingexpr.TokenParams) (ok bool, quota int, result *billingexpr.TieredResult) {
	snap := relayInfo.TieredBillingSnapshot
	if snap == nil || snap.BillingMode != "tiered_expr" {
		return false, 0, nil
	}

	requestInput := billingexpr.RequestInput{}
	if relayInfo.BillingRequestInput != nil {
		requestInput = *relayInfo.BillingRequestInput
	}

	tr, err := billingexpr.ComputeTieredQuotaWithRequest(snap, params, requestInput)
	if err != nil {
		quota = relayInfo.FinalPreConsumedQuota
		if quota <= 0 {
			quota = snap.EstimatedQuotaAfterGroup
		}
		return true, quota, nil
	}

	// Surface any int32 saturation from settlement onto RelayInfo so the
	// consume log records it under admin_info, regardless of which caller
	// (text, audio, WSS) consumes the returned quota. First non-nil wins.
	noteQuotaClamp(relayInfo, tr.Clamp)

	return true, tr.ActualQuotaAfterGroup, &tr
}
