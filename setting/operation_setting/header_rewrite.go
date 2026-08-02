package operation_setting

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"golang.org/x/net/http/httpguts"
)

const (
	HeaderRewritePresetsOptionKey = "HeaderRewritePresets"
	maxHeaderRewritePresets       = 64
	maxHeaderRewriteRules         = 64
	maxHeaderRewriteValueBytes    = 8 * 1024
)

type HeaderRewritePreset struct {
	Name string `json:"name"`
	types.HeaderRewriteRule
}

type HeaderRewritePresets map[string]HeaderRewritePreset

var headerRewritePresets atomic.Value

var protectedHeaderNames = map[string]struct{}{
	"host":                     {},
	"origin":                   {},
	"content-length":           {},
	"accept-encoding":          {},
	"connection":               {},
	"keep-alive":               {},
	"proxy-authenticate":       {},
	"proxy-authorization":      {},
	"te":                       {},
	"trailer":                  {},
	"transfer-encoding":        {},
	"upgrade":                  {},
	"cookie":                   {},
	"authorization":            {},
	"x-api-key":                {},
	"x-goog-api-key":           {},
	"api-key":                  {},
	"sec-websocket-key":        {},
	"sec-websocket-version":    {},
	"sec-websocket-extensions": {},
	"sec-websocket-protocol":   {},
}

var protectedHeaderPrefixes = []string{"x-amz-", "sec-websocket-"}

func init() {
	headerRewritePresets.Store(defaultHeaderRewritePresets())
}

func defaultHeaderRewritePresets() HeaderRewritePresets {
	return HeaderRewritePresets{
		"claude-code": {
			Name: "Claude Code",
			HeaderRewriteRule: types.HeaderRewriteRule{
				Remove: []string{"X-App", "X-Stainless-*"},
				Set: map[string]string{
					"User-Agent": "claude-cli/2.1.201 (external, cli)",
				},
			},
		},
		"opencode": {
			Name: "OpenCode",
			HeaderRewriteRule: types.HeaderRewriteRule{
				Remove: []string{"X-App", "X-Stainless-*"},
				Set: map[string]string{
					"User-Agent": "opencode/1.17.13",
				},
			},
		},
		"codex": {
			Name: "Codex CLI",
			HeaderRewriteRule: types.HeaderRewriteRule{
				Remove: []string{"X-App", "X-Stainless-*"},
				Set: map[string]string{
					"User-Agent":          "codex-cli/0.146.0",
					"Originator":          "codex_cli_rs",
					"Session_id":          "{client_header:Session_id|request_id}",
					"Thread_id":           "{client_header:Thread_id|request_id}",
					"X-Client-Request-Id": "{client_header:X-Client-Request-Id|request_id}",
				},
			},
		},
	}
}

func cloneHeaderRewriteRule(rule types.HeaderRewriteRule) types.HeaderRewriteRule {
	cloned := types.HeaderRewriteRule{}
	if len(rule.Remove) > 0 {
		cloned.Remove = append([]string(nil), rule.Remove...)
	}
	if len(rule.Set) > 0 {
		cloned.Set = make(map[string]string, len(rule.Set))
		for key, value := range rule.Set {
			cloned.Set[key] = value
		}
	}
	return cloned
}

func cloneHeaderRewritePresets(source HeaderRewritePresets) HeaderRewritePresets {
	cloned := make(HeaderRewritePresets, len(source))
	for id, preset := range source {
		cloned[id] = HeaderRewritePreset{
			Name:              preset.Name,
			HeaderRewriteRule: cloneHeaderRewriteRule(preset.HeaderRewriteRule),
		}
	}
	return cloned
}

func HeaderRewritePresets2JSONString() string {
	data, err := common.Marshal(headerRewritePresets.Load().(HeaderRewritePresets))
	if err != nil {
		common.SysError("failed to marshal header rewrite presets: " + err.Error())
		return "{}"
	}
	return string(data)
}

func ParseHeaderRewritePresetsJSONString(value string) (HeaderRewritePresets, error) {
	presets := HeaderRewritePresets{}
	if err := common.UnmarshalJsonStr(value, &presets); err != nil {
		return nil, fmt.Errorf("header rewrite presets must be a JSON object: %w", err)
	}
	if presets == nil {
		return nil, fmt.Errorf("header rewrite presets must be a JSON object")
	}
	if err := ValidateHeaderRewritePresets(presets); err != nil {
		return nil, err
	}
	return presets, nil
}

func UpdateHeaderRewritePresetsByJSONString(value string) error {
	presets, err := ParseHeaderRewritePresetsJSONString(value)
	if err != nil {
		return err
	}
	headerRewritePresets.Store(cloneHeaderRewritePresets(presets))
	return nil
}

func GetHeaderRewritePresets() HeaderRewritePresets {
	return cloneHeaderRewritePresets(headerRewritePresets.Load().(HeaderRewritePresets))
}

func GetHeaderRewritePreset(id string) (HeaderRewritePreset, bool) {
	preset, ok := headerRewritePresets.Load().(HeaderRewritePresets)[id]
	if !ok {
		return HeaderRewritePreset{}, false
	}
	return HeaderRewritePreset{Name: preset.Name, HeaderRewriteRule: cloneHeaderRewriteRule(preset.HeaderRewriteRule)}, true
}

func ValidateHeaderRewritePresets(presets HeaderRewritePresets) error {
	if len(presets) > maxHeaderRewritePresets {
		return fmt.Errorf("header rewrite presets cannot contain more than %d entries", maxHeaderRewritePresets)
	}
	for id, preset := range presets {
		if err := validateHeaderRewritePresetID(id); err != nil {
			return err
		}
		if strings.TrimSpace(preset.Name) == "" || len(preset.Name) > 128 {
			return fmt.Errorf("header rewrite preset %q name must contain 1 to 128 characters", id)
		}
		if err := ValidateHeaderRewriteRule(preset.HeaderRewriteRule); err != nil {
			return fmt.Errorf("header rewrite preset %q: %w", id, err)
		}
	}
	return nil
}

func ValidateChannelHeaderRewrite(settings *types.ChannelHeaderRewriteSettings) error {
	if settings == nil {
		return nil
	}
	if settings.PresetID != "" {
		if err := validateHeaderRewritePresetID(settings.PresetID); err != nil {
			return err
		}
		if _, ok := GetHeaderRewritePreset(settings.PresetID); !ok {
			return fmt.Errorf("header rewrite preset %q does not exist", settings.PresetID)
		}
	}
	if err := ValidateHeaderRewriteRule(settings.HeaderRewriteRule); err != nil {
		return fmt.Errorf("channel header rewrite: %w", err)
	}
	return nil
}

func validateHeaderRewritePresetID(id string) error {
	if len(id) < 1 || len(id) > 64 {
		return fmt.Errorf("header rewrite preset id %q must contain 1 to 64 characters", id)
	}
	for i, char := range id {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if i > 0 {
			valid = valid || char == '.' || char == '_' || char == '-'
		}
		if !valid {
			return fmt.Errorf("invalid header rewrite preset id %q", id)
		}
	}
	return nil
}

func ValidateHeaderRewriteRule(rule types.HeaderRewriteRule) error {
	if len(rule.Remove) > maxHeaderRewriteRules || len(rule.Set) > maxHeaderRewriteRules {
		return fmt.Errorf("remove and set cannot contain more than %d rules each", maxHeaderRewriteRules)
	}
	for _, pattern := range rule.Remove {
		if err := validateHeaderRemovePattern(pattern); err != nil {
			return err
		}
	}
	setNames := make(map[string]string, len(rule.Set))
	for name, value := range rule.Set {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		if existing, ok := setNames[lowerName]; ok {
			return fmt.Errorf("header names %q and %q are duplicates", existing, name)
		}
		setNames[lowerName] = name
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("invalid header name %q", name)
		}
		if isProtectedHeaderName(name) {
			return fmt.Errorf("header %q cannot be rewritten", name)
		}
		if len(value) > maxHeaderRewriteValueBytes {
			return fmt.Errorf("header %q value cannot exceed %d bytes", name, maxHeaderRewriteValueBytes)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q value cannot contain CR or LF", name)
		}
		if err := validateHeaderRewriteValue(value); err != nil {
			return fmt.Errorf("header %q: %w", name, err)
		}
	}
	return nil
}

func validateHeaderRemovePattern(pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return fmt.Errorf("header remove pattern %q is not allowed", pattern)
	}
	if strings.Count(pattern, "*") > 1 || strings.ContainsAny(pattern, "?[]") {
		return fmt.Errorf("header remove pattern %q supports at most one * wildcard", pattern)
	}
	name := strings.ReplaceAll(pattern, "*", "A")
	if !httpguts.ValidHeaderFieldName(name) {
		return fmt.Errorf("invalid header remove pattern %q", pattern)
	}
	if isProtectedHeaderPattern(pattern) {
		return fmt.Errorf("header remove pattern %q can match a protected header", pattern)
	}
	return nil
}

func isProtectedHeaderName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range protectedHeaderPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	_, ok := protectedHeaderNames[lower]
	return ok
}

func isProtectedHeaderPattern(pattern string) bool {
	lower := strings.ToLower(strings.TrimSpace(pattern))
	if !strings.Contains(lower, "*") {
		return isProtectedHeaderName(lower)
	}
	parts := strings.SplitN(lower, "*", 2)
	for name := range protectedHeaderNames {
		if strings.HasPrefix(name, parts[0]) && strings.HasSuffix(name, parts[1]) {
			return true
		}
	}
	for _, prefix := range protectedHeaderPrefixes {
		if strings.HasPrefix(parts[0], prefix) || strings.HasPrefix(prefix, parts[0]) {
			return true
		}
	}
	return false
}

func validateHeaderRewriteValue(value string) error {
	if !strings.Contains(value, "{") && !strings.Contains(value, "}") {
		return nil
	}
	if value == "{request_id}" {
		return nil
	}
	if strings.HasPrefix(value, "{client_header:") && strings.HasSuffix(value, "}") {
		body := strings.TrimSuffix(strings.TrimPrefix(value, "{client_header:"), "}")
		name := body
		if strings.HasSuffix(body, "|request_id") {
			name = strings.TrimSuffix(body, "|request_id")
		}
		if httpguts.ValidHeaderFieldName(strings.TrimSpace(name)) {
			return nil
		}
	}
	return fmt.Errorf("unsupported placeholder %q", value)
}

func CanonicalizeHeaderRewriteRule(rule types.HeaderRewriteRule) types.HeaderRewriteRule {
	canonical := cloneHeaderRewriteRule(rule)
	for name, value := range canonical.Set {
		canonicalName := http.CanonicalHeaderKey(name)
		if canonicalName != name {
			delete(canonical.Set, name)
			canonical.Set[canonicalName] = value
		}
	}
	return canonical
}
