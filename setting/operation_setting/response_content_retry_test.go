package operation_setting

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseResponseContentRetryPolicyNormalizesRules(t *testing.T) {
	policy, err := ParseResponseContentRetryPolicyJSONString(`{
		"enabled": true,
		"rules": [
			{"mode":"prefix","content":"  blocked  "},
			{"mode":"exact","content":"filtered"}
		]
	}`)
	require.NoError(t, err)
	assert.True(t, policy.Enabled)
	assert.Equal(t, []ResponseContentRetryRule{
		{Mode: ResponseContentMatchPrefix, Content: "blocked"},
		{Mode: ResponseContentMatchExact, Content: "filtered"},
	}, policy.Rules)
}

func TestParseResponseContentRetryPolicyRejectsInvalidRules(t *testing.T) {
	tests := []string{
		`null`,
		`{"enabled":true,"rules":[{"mode":"contains","content":"blocked"}]}`,
		`{"enabled":true,"rules":[{"mode":"prefix","content":"  "}]}`,
		`{"enabled":true,"rules":[{"mode":"prefix","content":"blocked"},{"mode":"prefix","content":" blocked "}]}`,
	}
	for _, value := range tests {
		_, err := ParseResponseContentRetryPolicyJSONString(value)
		assert.Error(t, err, value)
	}
}

func TestParseResponseContentRetryPolicyEnforcesRuleAndTotalSizeLimits(t *testing.T) {
	totalRules := make([]ResponseContentRetryRule, 17)
	for index := range totalRules {
		totalRules[index] = ResponseContentRetryRule{
			Mode:    ResponseContentMatchPrefix,
			Content: strings.Repeat("x", maxResponseContentRetryRuleBytes-8) + string(rune('a'+index)),
		}
	}
	tests := []ResponseContentRetryPolicy{
		{
			Enabled: true,
			Rules: []ResponseContentRetryRule{{
				Mode:    ResponseContentMatchPrefix,
				Content: strings.Repeat("x", maxResponseContentRetryRuleBytes+1),
			}},
		},
		{
			Enabled: true,
			Rules:   totalRules,
		},
	}

	for _, policy := range tests {
		data, err := common.Marshal(policy)
		require.NoError(t, err)
		_, err = ParseResponseContentRetryPolicyJSONString(string(data))
		assert.Error(t, err)
	}
}

func TestUpdateResponseContentRetryPolicyUsesImmutableSnapshot(t *testing.T) {
	original := ResponseContentRetryPolicy2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateResponseContentRetryPolicyByJSONString(original))
	})

	require.NoError(t, UpdateResponseContentRetryPolicyByJSONString(`{"enabled":true,"rules":[{"mode":"prefix","content":"blocked"}]}`))
	loaded := GetResponseContentRetryPolicy()
	loaded.Rules[0].Content = "mutated"

	assert.Equal(t, "blocked", GetResponseContentRetryPolicy().Rules[0].Content)
}
