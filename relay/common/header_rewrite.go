package common

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	common2 "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const (
	legacyHeaderPassthroughAllKey        = "*"
	legacyHeaderPassthroughRegexPrefix   = "re:"
	legacyHeaderPassthroughRegexPrefixV2 = "regex:"
	clientHeaderPlaceholderPrefix        = "{client_header:"
)

var legacyHeaderPassthroughRegexCache sync.Map

var legacyPassthroughSkipHeaders = map[string]struct{}{
	"connection": {}, "keep-alive": {}, "proxy-authenticate": {},
	"proxy-authorization": {}, "te": {}, "trailer": {},
	"transfer-encoding": {}, "upgrade": {}, "cookie": {},
	"host": {}, "content-length": {}, "accept-encoding": {},
	"authorization": {}, "x-api-key": {}, "x-goog-api-key": {},
	"sec-websocket-key": {}, "sec-websocket-version": {},
	"sec-websocket-extensions": {},
}

type HeaderRewriteInput struct {
	ChannelSetting    dto.ChannelSettings
	LegacyOverride    map[string]interface{}
	IncomingHeaders   http.Header
	APIKey            string
	RequestID         string
	AllowClientHeader bool
	IsChannelTest     bool
}

type HeaderMutationPlan struct {
	LegacyPassthrough map[string]string
	PresetRule        types.HeaderRewriteRule
	ChannelRule       types.HeaderRewriteRule
	LegacyExplicit    map[string]string
}

func ResolveHeaderMutationPlan(input HeaderRewriteInput) (HeaderMutationPlan, error) {
	plan := HeaderMutationPlan{
		LegacyPassthrough: map[string]string{},
		LegacyExplicit:    map[string]string{},
	}

	passAll := false
	var passthroughRegex []*regexp.Regexp
	if input.AllowClientHeader && !input.IsChannelTest {
		for key := range input.LegacyOverride {
			trimmed := strings.TrimSpace(strings.ToLower(key))
			if trimmed == legacyHeaderPassthroughAllKey {
				passAll = true
				continue
			}
			var pattern string
			switch {
			case strings.HasPrefix(trimmed, legacyHeaderPassthroughRegexPrefix):
				pattern = strings.TrimSpace(trimmed[len(legacyHeaderPassthroughRegexPrefix):])
			case strings.HasPrefix(trimmed, legacyHeaderPassthroughRegexPrefixV2):
				pattern = strings.TrimSpace(trimmed[len(legacyHeaderPassthroughRegexPrefixV2):])
			default:
				continue
			}
			if pattern == "" {
				return plan, fmt.Errorf("header passthrough regex pattern is empty: %q", key)
			}
			compiled, err := compileLegacyHeaderRegex(pattern)
			if err != nil {
				return plan, err
			}
			passthroughRegex = append(passthroughRegex, compiled)
		}
	}

	if passAll || len(passthroughRegex) > 0 {
		for name := range input.IncomingHeaders {
			if shouldSkipLegacyPassthrough(name) {
				continue
			}
			matched := passAll
			if !matched {
				for _, compiled := range passthroughRegex {
					if compiled.MatchString(name) {
						matched = true
						break
					}
				}
			}
			value := strings.TrimSpace(input.IncomingHeaders.Get(name))
			if matched && value != "" {
				plan.LegacyPassthrough[strings.ToLower(strings.TrimSpace(name))] = value
			}
		}
	}

	for key, rawValue := range input.LegacyOverride {
		if isLegacyHeaderPassthroughRule(key) {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		value, ok := rawValue.(string)
		if !ok {
			return plan, fmt.Errorf("header override %q value must be a string", key)
		}
		if input.IsChannelTest && strings.HasPrefix(strings.TrimSpace(value), clientHeaderPlaceholderPrefix) {
			continue
		}
		resolved, include, err := resolveLegacyHeaderValue(value, input)
		if err != nil {
			return plan, err
		}
		if include {
			plan.LegacyExplicit[key] = resolved
		}
	}

	settings := input.ChannelSetting.HeaderRewrite
	if settings == nil {
		return plan, nil
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		requestID = common2.NewRequestId()
	}
	if settings.PresetID != "" {
		preset, ok := operation_setting.GetHeaderRewritePreset(settings.PresetID)
		if !ok {
			return plan, fmt.Errorf("header rewrite preset %q does not exist", settings.PresetID)
		}
		resolved, err := resolveStructuredHeaderRule(preset.HeaderRewriteRule, input.IncomingHeaders, requestID, input.AllowClientHeader && !input.IsChannelTest)
		if err != nil {
			return plan, fmt.Errorf("header rewrite preset %q: %w", settings.PresetID, err)
		}
		plan.PresetRule = resolved
	}
	resolved, err := resolveStructuredHeaderRule(settings.HeaderRewriteRule, input.IncomingHeaders, requestID, input.AllowClientHeader && !input.IsChannelTest)
	if err != nil {
		return plan, fmt.Errorf("channel header rewrite: %w", err)
	}
	plan.ChannelRule = resolved
	return plan, nil
}

func ResolveAndApplyHeaderRewrite(header http.Header, input HeaderRewriteInput) error {
	plan, err := ResolveHeaderMutationPlan(input)
	if err != nil {
		return err
	}
	ApplyHeaderMutationPlan(header, plan)
	return nil
}

func ResolveAndApplyHeaderRewriteToRequest(req *http.Request, input HeaderRewriteInput) error {
	if req == nil {
		return nil
	}
	plan, err := ResolveHeaderMutationPlan(input)
	if err != nil {
		return err
	}
	ApplyHeaderMutationPlan(req.Header, plan)
	if host, ok := plan.LegacyExplicit["Host"]; ok {
		req.Host = host
	} else {
		for key, value := range plan.LegacyExplicit {
			if strings.EqualFold(key, "Host") {
				req.Host = value
				break
			}
		}
	}
	return nil
}

func ApplyHeaderMutationPlan(header http.Header, plan HeaderMutationPlan) {
	if header == nil {
		return
	}
	applyHeaderSet(header, plan.LegacyPassthrough)
	applyHeaderRemove(header, plan.PresetRule.Remove)
	applyHeaderSet(header, plan.PresetRule.Set)
	applyHeaderRemove(header, plan.ChannelRule.Remove)
	applyHeaderSet(header, plan.ChannelRule.Set)
	applyHeaderSet(header, plan.LegacyExplicit)
}

func compileLegacyHeaderRegex(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, errors.New("empty regex pattern")
	}
	if cached, ok := legacyHeaderPassthroughRegexCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	actual, _ := legacyHeaderPassthroughRegexCache.LoadOrStore(pattern, compiled)
	return actual.(*regexp.Regexp), nil
}

func isLegacyHeaderPassthroughRule(key string) bool {
	key = strings.TrimSpace(strings.ToLower(key))
	return key == legacyHeaderPassthroughAllKey ||
		strings.HasPrefix(key, legacyHeaderPassthroughRegexPrefix) ||
		strings.HasPrefix(key, legacyHeaderPassthroughRegexPrefixV2)
}

func shouldSkipLegacyPassthrough(name string) bool {
	_, ok := legacyPassthroughSkipHeaders[strings.ToLower(strings.TrimSpace(name))]
	return strings.TrimSpace(name) == "" || ok
}

func resolveLegacyHeaderValue(value string, input HeaderRewriteInput) (string, bool, error) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, clientHeaderPlaceholderPrefix) {
		body := strings.TrimSuffix(strings.TrimPrefix(trimmed, clientHeaderPlaceholderPrefix), "}")
		if body == trimmed || !strings.HasSuffix(trimmed, "}") || strings.Contains(body, "}") {
			return "", false, fmt.Errorf("client_header placeholder must be the full value: %q", value)
		}
		name := strings.TrimSpace(body)
		if name == "" {
			return "", false, fmt.Errorf("client_header placeholder name is empty: %q", value)
		}
		if !input.AllowClientHeader || input.IncomingHeaders == nil {
			return "", false, nil
		}
		resolved := input.IncomingHeaders.Get(name)
		return resolved, strings.TrimSpace(resolved) != "", nil
	}
	resolved := strings.ReplaceAll(value, "{api_key}", input.APIKey)
	return resolved, strings.TrimSpace(resolved) != "", nil
}

func resolveStructuredHeaderRule(rule types.HeaderRewriteRule, incoming http.Header, requestID string, allowClient bool) (types.HeaderRewriteRule, error) {
	resolved := operation_setting.CanonicalizeHeaderRewriteRule(rule)
	if len(resolved.Set) == 0 {
		return resolved, nil
	}
	for name, value := range resolved.Set {
		switch {
		case value == "{request_id}":
			resolved.Set[name] = requestID
		case strings.HasPrefix(value, clientHeaderPlaceholderPrefix) && strings.HasSuffix(value, "}"):
			body := strings.TrimSuffix(strings.TrimPrefix(value, clientHeaderPlaceholderPrefix), "}")
			fallback := strings.HasSuffix(body, "|request_id")
			if fallback {
				body = strings.TrimSuffix(body, "|request_id")
			}
			clientValue := ""
			if allowClient && incoming != nil {
				clientValue = incoming.Get(strings.TrimSpace(body))
			}
			if strings.TrimSpace(clientValue) != "" {
				resolved.Set[name] = clientValue
			} else if fallback {
				resolved.Set[name] = requestID
			} else {
				delete(resolved.Set, name)
			}
		}
	}
	return resolved, nil
}

func applyHeaderSet(header http.Header, values map[string]string) {
	for name, value := range values {
		for existing := range header {
			if strings.EqualFold(existing, name) {
				delete(header, existing)
			}
		}
		header.Set(name, value)
	}
}

func applyHeaderRemove(header http.Header, patterns []string) {
	for _, pattern := range patterns {
		lowerPattern := strings.ToLower(pattern)
		for name := range header {
			lowerName := strings.ToLower(name)
			matched := lowerName == lowerPattern
			if strings.Contains(lowerPattern, "*") {
				parts := strings.Split(lowerPattern, "*")
				matched = strings.HasPrefix(lowerName, parts[0]) && strings.HasSuffix(lowerName, parts[1])
			}
			if !matched {
				continue
			}
			delete(header, name)
			if strings.EqualFold(name, "User-Agent") {
				header["User-Agent"] = nil
			}
		}
	}
}
