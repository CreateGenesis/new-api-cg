package operation_setting

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

const (
	ResponseContentRetryPolicyOptionKey = "ResponseContentRetryPolicy"
	maxResponseContentRetryRules        = 64
	maxResponseContentRetryRuleBytes    = 4 * 1024
	maxResponseContentRetryTotalBytes   = 64 * 1024
)

type ResponseContentMatchMode string

const (
	ResponseContentMatchPrefix ResponseContentMatchMode = "prefix"
	ResponseContentMatchExact  ResponseContentMatchMode = "exact"
)

type ResponseContentRetryRule struct {
	Mode    ResponseContentMatchMode `json:"mode"`
	Content string                   `json:"content"`
}

type ResponseContentRetryPolicy struct {
	Enabled bool                       `json:"enabled"`
	Rules   []ResponseContentRetryRule `json:"rules"`
}

var responseContentRetryPolicy atomic.Value

func init() {
	responseContentRetryPolicy.Store(defaultResponseContentRetryPolicy())
}

func defaultResponseContentRetryPolicy() ResponseContentRetryPolicy {
	return ResponseContentRetryPolicy{
		Enabled: true,
		Rules: []ResponseContentRetryRule{
			{
				Mode:    ResponseContentMatchPrefix,
				Content: "抱歉，系统检测到您当前输入的信息存在敏感内容，我无法响应您的请求，请检查后重输入",
			},
			{
				Mode:    ResponseContentMatchPrefix,
				Content: "[内容已过滤]",
			},
		},
	}
}

func cloneResponseContentRetryPolicy(policy ResponseContentRetryPolicy) ResponseContentRetryPolicy {
	cloned := policy
	if len(policy.Rules) > 0 {
		cloned.Rules = append([]ResponseContentRetryRule(nil), policy.Rules...)
	}
	return cloned
}

func ResponseContentRetryPolicy2JSONString() string {
	data, err := common.Marshal(responseContentRetryPolicy.Load().(ResponseContentRetryPolicy))
	if err != nil {
		common.SysError("failed to marshal response content retry policy: " + err.Error())
		return "{}"
	}
	return string(data)
}

func ParseResponseContentRetryPolicyJSONString(value string) (ResponseContentRetryPolicy, error) {
	if strings.TrimSpace(value) == "null" {
		return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry policy must be a JSON object")
	}
	policy := ResponseContentRetryPolicy{}
	if err := common.UnmarshalJsonStr(value, &policy); err != nil {
		return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry policy must be a JSON object: %w", err)
	}
	if len(policy.Rules) > maxResponseContentRetryRules {
		return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry policy cannot contain more than %d rules", maxResponseContentRetryRules)
	}

	seen := make(map[string]struct{}, len(policy.Rules))
	totalBytes := 0
	for index := range policy.Rules {
		rule := &policy.Rules[index]
		switch rule.Mode {
		case ResponseContentMatchPrefix, ResponseContentMatchExact:
		default:
			return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry rule %d has invalid mode %q", index+1, rule.Mode)
		}
		rule.Content = strings.TrimSpace(rule.Content)
		if rule.Content == "" {
			return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry rule %d content cannot be empty", index+1)
		}
		if len(rule.Content) > maxResponseContentRetryRuleBytes {
			return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry rule %d cannot exceed %d bytes", index+1, maxResponseContentRetryRuleBytes)
		}
		totalBytes += len(rule.Content)
		if totalBytes > maxResponseContentRetryTotalBytes {
			return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry rule content cannot exceed %d bytes in total", maxResponseContentRetryTotalBytes)
		}
		key := string(rule.Mode) + "\x00" + rule.Content
		if _, ok := seen[key]; ok {
			return ResponseContentRetryPolicy{}, fmt.Errorf("response content retry rule %d is duplicated", index+1)
		}
		seen[key] = struct{}{}
	}
	return policy, nil
}

func UpdateResponseContentRetryPolicyByJSONString(value string) error {
	policy, err := ParseResponseContentRetryPolicyJSONString(value)
	if err != nil {
		return err
	}
	responseContentRetryPolicy.Store(cloneResponseContentRetryPolicy(policy))
	return nil
}

func GetResponseContentRetryPolicy() ResponseContentRetryPolicy {
	return cloneResponseContentRetryPolicy(responseContentRetryPolicy.Load().(ResponseContentRetryPolicy))
}
