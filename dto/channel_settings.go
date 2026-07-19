package dto

import (
	"fmt"
	"net/url"
	"strings"
)

type ChannelSettings struct {
	ForceFormat            bool   `json:"force_format,omitempty"`
	ThinkingToContent      bool   `json:"thinking_to_content,omitempty"`
	Proxy                  string `json:"proxy"`
	ProxyFallbackDirect    bool   `json:"proxy_fallback_direct,omitempty"`
	PassThroughBodyEnabled bool   `json:"pass_through_body_enabled,omitempty"`
	SystemPrompt           string `json:"system_prompt,omitempty"`
	SystemPromptOverride   bool   `json:"system_prompt_override,omitempty"`
}

type VertexKeyType string

const (
	VertexKeyTypeJSON   VertexKeyType = "json"
	VertexKeyTypeAPIKey VertexKeyType = "api_key"
)

type AwsKeyType string

const (
	AwsKeyTypeAKSK   AwsKeyType = "ak_sk" // 默认
	AwsKeyTypeApiKey AwsKeyType = "api_key"
)

type ChannelOtherSettings struct {
	AzureResponsesVersion                 string                             `json:"azure_responses_version,omitempty"`
	VertexKeyType                         VertexKeyType                      `json:"vertex_key_type,omitempty"` // "json" or "api_key"
	OpenRouterEnterprise                  *bool                              `json:"openrouter_enterprise,omitempty"`
	ClaudeBetaQuery                       bool                               `json:"claude_beta_query,omitempty"`          // Claude 渠道是否强制追加 ?beta=true
	AllowServiceTier                      bool                               `json:"allow_service_tier,omitempty"`         // 是否允许 service_tier 透传（默认过滤以避免额外计费）
	AllowInferenceGeo                     bool                               `json:"allow_inference_geo,omitempty"`        // 是否允许 inference_geo 透传（仅 Claude，默认过滤以满足数据驻留合规
	AllowSpeed                            bool                               `json:"allow_speed,omitempty"`                // 是否允许 speed 透传（仅 Claude，默认过滤以避免意外切换推理速度模式）
	AllowSafetyIdentifier                 bool                               `json:"allow_safety_identifier,omitempty"`    // 是否允许 safety_identifier 透传（默认过滤以保护用户隐私）
	DisableStore                          bool                               `json:"disable_store,omitempty"`              // 是否禁用 store 透传（默认允许透传，禁用后可能导致 Codex 无法使用）
	AllowIncludeObfuscation               bool                               `json:"allow_include_obfuscation,omitempty"`  // 是否允许 stream_options.include_obfuscation 透传（默认过滤以避免关闭流混淆保护）
	DisableTaskPollingSleep               bool                               `json:"disable_task_polling_sleep,omitempty"` // 是否跳过异步任务轮询间隔
	AwsKeyType                            AwsKeyType                         `json:"aws_key_type,omitempty"`
	UpstreamModelUpdateCheckEnabled       bool                               `json:"upstream_model_update_check_enabled,omitempty"`        // 是否检测上游模型更新
	UpstreamModelUpdateAutoSyncEnabled    bool                               `json:"upstream_model_update_auto_sync_enabled,omitempty"`    // 是否自动同步上游模型更新
	UpstreamModelUpdateLastCheckTime      int64                              `json:"upstream_model_update_last_check_time,omitempty"`      // 上次检测时间
	UpstreamModelUpdateLastDetectedModels []string                           `json:"upstream_model_update_last_detected_models,omitempty"` // 上次检测到的可加入模型
	UpstreamModelUpdateLastRemovedModels  []string                           `json:"upstream_model_update_last_removed_models,omitempty"`  // 上次检测到的可删除模型
	UpstreamModelUpdateIgnoredModels      []string                           `json:"upstream_model_update_ignored_models,omitempty"`       // 手动忽略的模型
	AdvancedCustom                        *AdvancedCustomConfig              `json:"advanced_custom,omitempty"`
	SimulatedModelCache                   *SimulatedModelCacheSettings       `json:"simulated_model_cache,omitempty"`
	StatusCodeRetry                       *StatusCodeRetrySettings           `json:"status_code_retry,omitempty"`
	InputTokenRouting                     *InputTokenRoutingSettings         `json:"input_token_routing,omitempty"`
	StreamInterruptionBilling             *StreamInterruptionBillingSettings `json:"stream_interruption_billing,omitempty"`
}

type StreamInterruptionBillingMode string

const (
	StreamInterruptionBillingModeInputOnlyFree      StreamInterruptionBillingMode = "input_only_free"
	StreamInterruptionBillingModeAllInterruptedFree StreamInterruptionBillingMode = "all_interrupted_free"
)

type StreamInterruptionBillingSettings struct {
	Mode StreamInterruptionBillingMode `json:"mode,omitempty"`
}

func (s StreamInterruptionBillingSettings) Validate() error {
	switch s.Mode {
	case "", StreamInterruptionBillingModeInputOnlyFree, StreamInterruptionBillingModeAllInterruptedFree:
		return nil
	default:
		return fmt.Errorf("unknown mode %q", s.Mode)
	}
}

type SimulatedModelCacheSettings struct {
	Enabled       bool    `json:"enabled,omitempty"`
	TTLSeconds    int     `json:"ttl_seconds,omitempty"`
	MinMatchRatio float64 `json:"min_match_ratio,omitempty"`
}

func (s SimulatedModelCacheSettings) Normalize() SimulatedModelCacheSettings {
	if s.TTLSeconds <= 0 {
		s.TTLSeconds = 86400
	}
	if s.MinMatchRatio <= 0 {
		s.MinMatchRatio = 0.01
	}
	if s.MinMatchRatio > 1 {
		s.MinMatchRatio = 1
	}
	return s
}

func (s SimulatedModelCacheSettings) IsActive() bool {
	return s.Enabled
}

type InputTokenRoutingSettings struct {
	Enabled   bool                     `json:"enabled,omitempty"`
	GLM52Mode bool                     `json:"glm_5_2_mode,omitempty"`
	MinTokens int                      `json:"min_tokens,omitempty"`
	MaxTokens int                      `json:"max_tokens,omitempty"`
	Ranges    []InputTokenRoutingRange `json:"ranges,omitempty"`
}

type InputTokenEstimates struct {
	Default int
	GLM52   int
}

type InputTokenRoutingRange struct {
	MinTokens int `json:"min_tokens,omitempty"`
	MaxTokens int `json:"max_tokens,omitempty"`
}

func (s InputTokenRoutingSettings) Normalize() InputTokenRoutingSettings {
	if s.MinTokens < 0 {
		s.MinTokens = 0
	}
	if s.MaxTokens < 0 {
		s.MaxTokens = 0
	}
	if s.MinTokens > 0 && s.MaxTokens > 0 && s.MinTokens > s.MaxTokens {
		s.MinTokens, s.MaxTokens = s.MaxTokens, s.MinTokens
	}
	ranges := make([]InputTokenRoutingRange, 0, len(s.Ranges))
	for _, item := range s.Ranges {
		if item.MinTokens < 0 {
			item.MinTokens = 0
		}
		if item.MaxTokens < 0 {
			item.MaxTokens = 0
		}
		if item.MinTokens == 0 && item.MaxTokens == 0 {
			continue
		}
		if item.MinTokens > 0 && item.MaxTokens > 0 && item.MinTokens > item.MaxTokens {
			item.MinTokens, item.MaxTokens = item.MaxTokens, item.MinTokens
		}
		ranges = append(ranges, item)
	}
	s.Ranges = ranges
	return s
}

const (
	DefaultStatusCodeRetryTimes       = 10
	DefaultStatusCodeRetryIntervalMS  = 50
	DefaultStatusCodeRetryStatusCodes = "100-199,300-399,401-407,409-499,500-503,505-523,525-599"
)

type StatusCodeRetrySettings struct {
	Enabled         bool   `json:"enabled,omitempty"`
	RetryTimes      *int   `json:"retry_times,omitempty"`
	RetryIntervalMS *int   `json:"retry_interval_ms,omitempty"`
	StatusCodes     string `json:"status_codes,omitempty"`
}

type NormalizedStatusCodeRetrySettings struct {
	Enabled         bool
	RetryTimes      int
	RetryIntervalMS int
	StatusCodes     string
}

func (s StatusCodeRetrySettings) Normalize() NormalizedStatusCodeRetrySettings {
	retryTimes := DefaultStatusCodeRetryTimes
	if s.RetryTimes != nil {
		retryTimes = *s.RetryTimes
		if retryTimes < 0 {
			retryTimes = 0
		}
	}

	retryIntervalMS := DefaultStatusCodeRetryIntervalMS
	if s.RetryIntervalMS != nil {
		retryIntervalMS = *s.RetryIntervalMS
		if retryIntervalMS < 0 {
			retryIntervalMS = 0
		}
	}

	statusCodes := strings.TrimSpace(s.StatusCodes)
	if statusCodes == "" {
		statusCodes = DefaultStatusCodeRetryStatusCodes
	}

	return NormalizedStatusCodeRetrySettings{
		Enabled:         s.Enabled,
		RetryTimes:      retryTimes,
		RetryIntervalMS: retryIntervalMS,
		StatusCodes:     statusCodes,
	}
}

func (s *ChannelOtherSettings) IsOpenRouterEnterprise() bool {
	if s == nil || s.OpenRouterEnterprise == nil {
		return false
	}
	return *s.OpenRouterEnterprise
}

const (
	AdvancedCustomConverterNone                                         = "none"
	AdvancedCustomConverterAnthropicMessagesToOpenAIChatCompletions     = "anthropic_messages_to_openai_chat_completions"
	AdvancedCustomConverterOpenAIChatCompletionsToAnthropicMessages     = "openai_chat_completions_to_anthropic_messages"
	AdvancedCustomConverterOpenAIChatCompletionsToOpenAIResponses       = "openai_chat_completions_to_openai_responses"
	AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions       = "openai_responses_to_openai_chat_completions"
	AdvancedCustomConverterGeminiGenerateContentToOpenAIChatCompletions = "gemini_generate_content_to_openai_chat_completions"
	AdvancedCustomConverterOpenAIChatCompletionsToGeminiGenerateContent = "openai_chat_completions_to_gemini_generate_content"
)

const (
	AdvancedCustomAuthTypeNone   = "none"
	AdvancedCustomAuthTypeHeader = "header"
	AdvancedCustomAuthTypeQuery  = "query"
)

type AdvancedCustomConfig struct {
	Routes []AdvancedCustomRoute `json:"advanced_routes,omitempty"`
}

type AdvancedCustomRoute struct {
	IncomingPath string                   `json:"incoming_path,omitempty"`
	UpstreamPath string                   `json:"upstream_path,omitempty"`
	Converter    string                   `json:"converter,omitempty"`
	Auth         *AdvancedCustomRouteAuth `json:"auth,omitempty"`
}

type AdvancedCustomRouteAuth struct {
	Type  string `json:"type,omitempty"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

const advancedCustomModelPlaceholder = "{model}"

// MatchPath returns the first route whose IncomingPath matches requestPath.
// Matching mirrors the relay adaptor: exact match, {model} placeholder, and
// :generateContent <-> :streamGenerateContent equivalence.
func (c *AdvancedCustomConfig) MatchPath(requestPath string) (AdvancedCustomRoute, bool) {
	if c == nil {
		return AdvancedCustomRoute{}, false
	}
	for _, route := range c.Routes {
		if matchAdvancedCustomIncomingPath(strings.TrimSpace(route.IncomingPath), requestPath) {
			return route, true
		}
	}
	return AdvancedCustomRoute{}, false
}

// SupportsPath reports whether any route matches requestPath.
func (c *AdvancedCustomConfig) SupportsPath(requestPath string) bool {
	_, ok := c.MatchPath(requestPath)
	return ok
}

func matchAdvancedCustomIncomingPath(configuredPath string, requestPath string) bool {
	if matchAdvancedCustomIncomingPathTemplate(configuredPath, requestPath) {
		return true
	}
	if strings.Contains(configuredPath, ":generateContent") {
		streamPath := strings.Replace(configuredPath, ":generateContent", ":streamGenerateContent", 1)
		return matchAdvancedCustomIncomingPathTemplate(streamPath, requestPath)
	}
	return false
}

func matchAdvancedCustomIncomingPathTemplate(configuredPath string, requestPath string) bool {
	if !strings.Contains(configuredPath, advancedCustomModelPlaceholder) {
		return configuredPath == requestPath
	}

	parts := strings.Split(configuredPath, advancedCustomModelPlaceholder)
	if len(parts) != 2 {
		return false
	}
	if !strings.HasPrefix(requestPath, parts[0]) || !strings.HasSuffix(requestPath, parts[1]) {
		return false
	}

	model := strings.TrimSuffix(strings.TrimPrefix(requestPath, parts[0]), parts[1])
	return model != "" && !strings.Contains(model, "/")
}

func IsAdvancedCustomConverterAllowed(converter string) bool {
	switch converter {
	case AdvancedCustomConverterNone,
		AdvancedCustomConverterAnthropicMessagesToOpenAIChatCompletions,
		AdvancedCustomConverterOpenAIChatCompletionsToAnthropicMessages,
		AdvancedCustomConverterOpenAIChatCompletionsToOpenAIResponses,
		AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions,
		AdvancedCustomConverterGeminiGenerateContentToOpenAIChatCompletions,
		AdvancedCustomConverterOpenAIChatCompletionsToGeminiGenerateContent:
		return true
	default:
		return false
	}
}

func (c *AdvancedCustomConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("advanced_custom is required")
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("advanced_custom requires at least one route")
	}

	seenPaths := make(map[string]struct{}, len(c.Routes))
	for i := range c.Routes {
		route := c.Routes[i]
		route.IncomingPath = strings.TrimSpace(route.IncomingPath)
		upstreamPath := strings.TrimSpace(route.UpstreamPath)
		route.Converter = strings.TrimSpace(route.Converter)
		if route.Converter == "" {
			route.Converter = AdvancedCustomConverterNone
		}

		if route.IncomingPath == "" {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].incoming_path is required", i)
		}
		if !strings.HasPrefix(route.IncomingPath, "/") {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].incoming_path must start with /", i)
		}
		if strings.Contains(route.IncomingPath, "?") {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].incoming_path must not include query", i)
		}
		if _, exists := seenPaths[route.IncomingPath]; exists {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].incoming_path must be unique: %s", i, route.IncomingPath)
		}
		seenPaths[route.IncomingPath] = struct{}{}

		if upstreamPath == "" {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].upstream_path is required", i)
		}
		if err := validateAdvancedCustomUpstreamTarget(i, upstreamPath); err != nil {
			return err
		}

		if !IsAdvancedCustomConverterAllowed(route.Converter) {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].converter is not registered: %s", i, route.Converter)
		}
		if err := validateAdvancedCustomConverterPath(i, route.IncomingPath, route.Converter); err != nil {
			return err
		}
		if err := validateAdvancedCustomRouteAuth(i, route.Auth); err != nil {
			return err
		}
	}

	return nil
}

func validateAdvancedCustomUpstreamTarget(index int, upstreamPath string) error {
	if strings.HasPrefix(upstreamPath, "/") {
		if strings.HasPrefix(upstreamPath, "//") {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].upstream_path must be a full URL or a path starting with /", index)
		}
		return nil
	}

	parsedURL, err := url.Parse(upstreamPath)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return fmt.Errorf("advanced_custom.advanced_routes[%d].upstream_path must be a full URL or a path starting with /", index)
	}
	if !strings.EqualFold(parsedURL.Scheme, "http") && !strings.EqualFold(parsedURL.Scheme, "https") {
		return fmt.Errorf("advanced_custom.advanced_routes[%d].upstream_path must use http or https", index)
	}
	return nil
}

func validateAdvancedCustomConverterPath(index int, incomingPath string, converter string) error {
	switch converter {
	case AdvancedCustomConverterNone:
		return nil
	case AdvancedCustomConverterAnthropicMessagesToOpenAIChatCompletions:
		if incomingPath == "/v1/messages" {
			return nil
		}
	case AdvancedCustomConverterOpenAIChatCompletionsToAnthropicMessages,
		AdvancedCustomConverterOpenAIChatCompletionsToOpenAIResponses,
		AdvancedCustomConverterOpenAIChatCompletionsToGeminiGenerateContent:
		if incomingPath == "/v1/chat/completions" {
			return nil
		}
	case AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions:
		if incomingPath == "/v1/responses" {
			return nil
		}
	case AdvancedCustomConverterGeminiGenerateContentToOpenAIChatCompletions:
		if strings.Contains(incomingPath, ":generateContent") || strings.Contains(incomingPath, ":streamGenerateContent") {
			return nil
		}
	}
	return fmt.Errorf("advanced_custom.advanced_routes[%d].converter does not match incoming_path: %s", index, converter)
}

func validateAdvancedCustomRouteAuth(index int, auth *AdvancedCustomRouteAuth) error {
	if auth == nil {
		return nil
	}
	authType := strings.TrimSpace(auth.Type)
	switch authType {
	case AdvancedCustomAuthTypeNone:
		return nil
	case AdvancedCustomAuthTypeHeader, AdvancedCustomAuthTypeQuery:
		if strings.TrimSpace(auth.Name) == "" {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].auth.name is required", index)
		}
		if strings.TrimSpace(auth.Value) == "" {
			return fmt.Errorf("advanced_custom.advanced_routes[%d].auth.value is required", index)
		}
		return nil
	default:
		return fmt.Errorf("advanced_custom.advanced_routes[%d].auth.type is invalid: %s", index, auth.Type)
	}
}
