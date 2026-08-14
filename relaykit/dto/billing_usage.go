package dto

const (
	BillingUsageSourceClaudeMessages = "claude_messages"
	BillingUsageSourceGeminiChat     = "gemini_chat"
	BillingUsageSourceOAIChat        = "oai_chat"
	BillingUsageSourceOAIResponses   = "oai_responses"

	BillingUsageSemanticAnthropic = "anthropic"
	BillingUsageSemanticGemini    = "gemini"
	BillingUsageSemanticOpenAI    = "openai"
)

type BillingUsage struct {
	Source              string               `json:"source,omitempty"`
	Semantic            string               `json:"semantic,omitempty"`
	Estimated           bool                 `json:"estimated,omitempty"`
	OpenAIUsage         *Usage               `json:"openai_usage,omitempty"`
	ClaudeUsage         *ClaudeUsage         `json:"claude_usage,omitempty"`
	GeminiUsageMetadata *GeminiUsageMetadata `json:"gemini_usage_metadata,omitempty"`
}

type KimiK3BillingAudit struct {
	OriginalSource        string   `json:"original_source,omitempty"`
	OriginalSemantic      string   `json:"original_semantic,omitempty"`
	Equation              string   `json:"equation"`
	OriginalInputTokens   int      `json:"original_input_tokens"`
	OriginalOutputTokens  int      `json:"original_output_tokens"`
	OriginalCacheRead     int      `json:"original_cache_read_tokens"`
	OriginalCacheCreation int      `json:"original_cache_creation_tokens"`
	SignedTotalInput      int      `json:"signed_total_input_tokens"`
	NormalizedTotalInput  int      `json:"normalized_total_input_tokens"`
	NormalizedOutput      int      `json:"normalized_output_tokens"`
	NormalizedCacheRead   int      `json:"normalized_cache_read_tokens"`
	NormalizedCacheWrite  int      `json:"normalized_cache_write_tokens"`
	NegativeFields        []string `json:"negative_fields,omitempty"`
	Adjustments           []string `json:"adjustments,omitempty"`
}

func NewClaudeMessagesBillingUsage(usage *ClaudeUsage) *BillingUsage {
	if !HasClaudeUsageTokens(usage) {
		return nil
	}
	return &BillingUsage{
		Source:      BillingUsageSourceClaudeMessages,
		Semantic:    BillingUsageSemanticAnthropic,
		ClaudeUsage: cloneClaudeUsage(usage),
	}
}

// HasClaudeUsageTokens mirrors HasOpenAIUsageTokens/HasGeminiUsageMetadataTokens:
// an all-zero ClaudeUsage must not become a BillingUsage, otherwise it would take
// precedence during settlement and zero out a non-zero top-level usage.
func HasClaudeUsageTokens(usage *ClaudeUsage) bool {
	if usage == nil {
		return false
	}
	if usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.CacheCreationInputTokens != 0 ||
		usage.CacheReadInputTokens != 0 ||
		usage.ClaudeCacheCreation5mTokens != 0 ||
		usage.ClaudeCacheCreation1hTokens != 0 {
		return true
	}
	if usage.CacheCreation != nil &&
		(usage.CacheCreation.Ephemeral5mInputTokens != 0 || usage.CacheCreation.Ephemeral1hInputTokens != 0) {
		return true
	}
	return false
}

func NewOpenAIChatBillingUsage(usage *Usage) *BillingUsage {
	return newOpenAIBillingUsage(BillingUsageSourceOAIChat, usage)
}

func NewOpenAIResponsesBillingUsage(usage *Usage) *BillingUsage {
	return newOpenAIBillingUsage(BillingUsageSourceOAIResponses, usage)
}

func newOpenAIBillingUsage(source string, usage *Usage) *BillingUsage {
	if !HasOpenAIUsageTokens(usage) {
		return nil
	}
	return &BillingUsage{
		Source:      source,
		Semantic:    BillingUsageSemanticOpenAI,
		OpenAIUsage: cloneOpenAIUsage(usage),
	}
}

func HasOpenAIUsageTokens(usage *Usage) bool {
	if usage == nil {
		return false
	}
	if usage.PromptTokens != 0 ||
		usage.CompletionTokens != 0 ||
		usage.TotalTokens != 0 ||
		usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.PromptCacheHitTokens != 0 ||
		usage.ClaudeCacheCreation5mTokens != 0 ||
		usage.ClaudeCacheCreation1hTokens != 0 {
		return true
	}
	if usage.PromptTokensDetails.CachedTokens != 0 ||
		usage.PromptTokensDetails.CachedCreationTokens != 0 ||
		usage.PromptTokensDetails.CacheWriteTokens != 0 ||
		usage.PromptTokensDetails.TextTokens != 0 ||
		usage.PromptTokensDetails.ImageTokens != 0 ||
		usage.PromptTokensDetails.AudioTokens != 0 {
		return true
	}
	if usage.CompletionTokenDetails.ReasoningTokens != 0 ||
		usage.CompletionTokenDetails.TextTokens != 0 ||
		usage.CompletionTokenDetails.ImageTokens != 0 ||
		usage.CompletionTokenDetails.AudioTokens != 0 {
		return true
	}
	return usage.InputTokensDetails != nil
}

func NewGeminiChatBillingUsage(metadata *GeminiUsageMetadata) *BillingUsage {
	return newGeminiChatBillingUsage(metadata, false)
}

func NewEstimatedGeminiChatBillingUsage(usage *Usage) *BillingUsage {
	if usage == nil {
		return nil
	}
	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return newGeminiChatBillingUsage(&GeminiUsageMetadata{
		PromptTokenCount:     usage.PromptTokens,
		CandidatesTokenCount: usage.CompletionTokens,
		TotalTokenCount:      totalTokens,
	}, true)
}

func newGeminiChatBillingUsage(metadata *GeminiUsageMetadata, estimated bool) *BillingUsage {
	if !HasGeminiUsageMetadataTokens(metadata) {
		return nil
	}
	usageMetadata := cloneGeminiUsageMetadata(*metadata)
	return &BillingUsage{
		Source:              BillingUsageSourceGeminiChat,
		Semantic:            BillingUsageSemanticGemini,
		Estimated:           estimated,
		GeminiUsageMetadata: &usageMetadata,
	}
}

func CloneBillingUsage(usage *BillingUsage) *BillingUsage {
	if usage == nil {
		return nil
	}
	clone := *usage
	clone.OpenAIUsage = cloneOpenAIUsage(usage.OpenAIUsage)
	clone.ClaudeUsage = cloneClaudeUsage(usage.ClaudeUsage)
	if usage.GeminiUsageMetadata != nil {
		metadata := cloneGeminiUsageMetadata(*usage.GeminiUsageMetadata)
		clone.GeminiUsageMetadata = &metadata
	}
	return &clone
}

func cloneOpenAIUsage(usage *Usage) *Usage {
	if usage == nil {
		return nil
	}
	clone := *usage
	clone.BillingUsage = nil
	if usage.InputTokensDetails != nil {
		inputTokensDetails := *usage.InputTokensDetails
		clone.InputTokensDetails = &inputTokensDetails
	}
	return &clone
}

func cloneClaudeUsage(usage *ClaudeUsage) *ClaudeUsage {
	if usage == nil {
		return nil
	}
	clone := *usage
	clone.BillingUsage = nil
	if usage.CacheCreation != nil {
		cacheCreation := *usage.CacheCreation
		clone.CacheCreation = &cacheCreation
	}
	if usage.ServerToolUse != nil {
		serverToolUse := *usage.ServerToolUse
		clone.ServerToolUse = &serverToolUse
	}
	return &clone
}

func cloneGeminiUsageMetadata(metadata GeminiUsageMetadata) GeminiUsageMetadata {
	metadata.PromptTokensDetails = append([]GeminiPromptTokensDetails{}, metadata.PromptTokensDetails...)
	metadata.ToolUsePromptTokensDetails = append([]GeminiPromptTokensDetails{}, metadata.ToolUsePromptTokensDetails...)
	metadata.CandidatesTokensDetails = append([]GeminiPromptTokensDetails{}, metadata.CandidatesTokensDetails...)
	metadata.BillingUsage = nil
	return metadata
}

func HasGeminiUsageMetadataTokens(metadata *GeminiUsageMetadata) bool {
	if metadata == nil {
		return false
	}
	if metadata.PromptTokenCount != 0 ||
		metadata.ToolUsePromptTokenCount != 0 ||
		metadata.CandidatesTokenCount != 0 ||
		metadata.TotalTokenCount != 0 ||
		metadata.ThoughtsTokenCount != 0 ||
		metadata.CachedContentTokenCount != 0 {
		return true
	}
	for _, detail := range metadata.PromptTokensDetails {
		if detail.TokenCount != 0 {
			return true
		}
	}
	for _, detail := range metadata.ToolUsePromptTokensDetails {
		if detail.TokenCount != 0 {
			return true
		}
	}
	for _, detail := range metadata.CandidatesTokensDetails {
		if detail.TokenCount != 0 {
			return true
		}
	}
	return false
}
