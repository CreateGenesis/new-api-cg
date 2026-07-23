package oairesponses

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesRequestToClaudeMessagesNoneDisablesThinking(t *testing.T) {
	temperature := 0.7
	topP := 0.9

	claudeRequest, err := OpenAIResponsesRequestToClaudeMessages(nil, &dto.OpenAIResponsesRequest{
		Model:       "claude-sonnet-4-5",
		Input:       mustRawMessage(t, "hello"),
		Reasoning:   &dto.Reasoning{Effort: "none"},
		Temperature: &temperature,
		TopP:        &topP,
	})
	require.NoError(t, err)
	require.NotNil(t, claudeRequest.Thinking)
	assert.Equal(t, "disabled", claudeRequest.Thinking.Type)
	assert.Nil(t, claudeRequest.Thinking.BudgetTokens)
	assert.Empty(t, claudeRequest.Thinking.Display)
	assert.Same(t, &temperature, claudeRequest.Temperature)
	assert.Same(t, &topP, claudeRequest.TopP)

	requestJSON, err := common.Marshal(claudeRequest)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"disabled"}`, gjson.GetBytes(requestJSON, "thinking").Raw)
}
