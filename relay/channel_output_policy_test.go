package relay

import (
	"math"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelOutputRecorderKeepsEmptyStreamUncommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		IsStream:        true,
		OriginModelName: "gpt-test",
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, operation_setting.ResponseContentRetryPolicy{}, 1, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(": ping\n\ndata: {\"choices\":[],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	assert.Empty(t, response.Body.String())

	policyErr := recorder.finish(ctx, info, &dto.Usage{})
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelZeroOutput, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderKeepsEmptyNonStreamUncommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, operation_setting.ResponseContentRetryPolicy{}, 1, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`)
	require.NoError(t, err)
	assert.Empty(t, response.Body.String())

	policyErr := recorder.finish(ctx, info, &dto.Usage{PromptTokens: 5, TotalTokens: 5})
	require.NotNil(t, policyErr)
	assert.Equal(t, types.ErrorCodeChannelZeroOutput, policyErr.GetErrorCode())
	assert.Empty(t, response.Body.String())
}

func TestChannelOutputRecorderEstimatesAndPatchesValidNonStreamOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, operation_setting.ResponseContentRetryPolicy{}, 1.5, 64*1024)
	ctx.Writer = recorder
	ctx.Writer.Header().Set("Content-Type", "application/json")

	_, err := recorder.WriteString(`{"choices":[{"message":{"role":"assistant","content":"hello world"}}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`)
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 5, TotalTokens: 5}
	require.Nil(t, recorder.finish(ctx, info, usage))

	assert.True(t, usage.Estimated)
	assert.Positive(t, usage.CompletionTokens)
	assert.Equal(t, 5+usage.CompletionTokens, usage.TotalTokens)
	var payload struct {
		Usage dto.Usage `json:"usage"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.Equal(t, usage.CompletionTokens, payload.Usage.CompletionTokens)
	assert.Equal(t, usage.TotalTokens, payload.Usage.TotalTokens)
	assert.Equal(t, strconv.Itoa(response.Body.Len()), response.Header().Get("Content-Length"))
}

func TestChannelOutputRecorderPreservesFirstStatusCodeBeforeCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-test",
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, operation_setting.ResponseContentRetryPolicy{}, 1, 64*1024)
	ctx.Writer = recorder

	recorder.WriteHeader(299)
	recorder.WriteHeader(201)
	_, err := recorder.WriteString(`{"choices":[{"message":{"content":"hello"}}]}`)
	require.NoError(t, err)
	recorder.WriteHeader(202)

	require.Nil(t, recorder.finish(ctx, info, &dto.Usage{CompletionTokens: 1, TotalTokens: 1}))
	assert.Equal(t, 299, response.Code)
}

func TestChannelOutputRecorderCommitsFirstEffectiveStreamOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		IsStream:           true,
		ShouldIncludeUsage: false,
		OriginModelName:    "gpt-test",
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, operation_setting.ResponseContentRetryPolicy{}, 1.5, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "hello")

	_, err = recorder.WriteString("data: [DONE]\n\n")
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 10, TotalTokens: 10}
	require.Nil(t, recorder.finish(ctx, info, usage))

	assert.Greater(t, usage.CompletionTokens, 0)
	assert.NotContains(t, response.Body.String(), "\"usage\"")
	assert.True(t, strings.HasSuffix(response.Body.String(), "data: [DONE]\n\n"))
}

func TestChannelOutputRecorderPatchesHeldStreamUsageAfterEstimation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAI,
		RelayMode:          relayconstant.RelayModeChatCompletions,
		IsStream:           true,
		ShouldIncludeUsage: true,
		OriginModelName:    "gpt-test",
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	recorder := newChannelOutputRecorder(ctx.Writer, info, true, operation_setting.ResponseContentRetryPolicy{}, 1.25, 64*1024)
	ctx.Writer = recorder

	_, err := recorder.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	require.NoError(t, err)
	_, err = recorder.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":0,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	usage := &dto.Usage{PromptTokens: 10, TotalTokens: 10}
	require.Nil(t, recorder.finish(ctx, info, usage))

	assert.True(t, usage.Estimated)
	assert.Positive(t, usage.CompletionTokens)
	usageEventFound := false
	for _, event := range splitSimulatedModelCacheSSEChunks(response.Body.Bytes()) {
		data, ok := simulatedModelCacheSSEData(event)
		if !ok {
			continue
		}
		var payload struct {
			Usage *dto.Usage `json:"usage"`
		}
		if common.Unmarshal(data, &payload) != nil || payload.Usage == nil {
			continue
		}
		usageEventFound = true
		assert.Equal(t, usage.CompletionTokens, payload.Usage.CompletionTokens)
		assert.Equal(t, usage.TotalTokens, payload.Usage.TotalTokens)
	}
	assert.True(t, usageEventFound)
	assert.True(t, strings.HasSuffix(response.Body.String(), "data: [DONE]\n\n"))
}

func TestObserveChannelOutputPayloadRecognizesSupportedProtocols(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		payload   string
	}{
		{
			name:      "OpenAI chat text",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			payload:   `{"choices":[{"message":{"content":"hello"}}]}`,
		},
		{
			name:      "OpenAI completion text",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeCompletions,
			payload:   `{"choices":[{"text":"hello"}]}`,
		},
		{
			name:      "Responses text delta",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			payload:   `{"type":"response.output_text.delta","delta":"hello"}`,
		},
		{
			name:    "Claude tool use",
			format:  types.RelayFormatClaude,
			payload: `{"content":[{"type":"tool_use","name":"search","input":{}}]}`,
		},
		{
			name:    "Gemini function call",
			format:  types.RelayFormatGemini,
			payload: `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search","args":{}}}]}}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, common.UnmarshalJsonStr(test.payload, &payload))
			var output strings.Builder

			assert.True(t, observeChannelOutputPayload(test.format, test.relayMode, payload, &output))
			assert.NotEmpty(t, output.String())
		})
	}

	var empty map[string]any
	require.NoError(t, common.UnmarshalJsonStr(`{"choices":[{"delta":{"role":"assistant","content":""}}]}`, &empty))
	var output strings.Builder
	assert.False(t, observeChannelOutputPayload(types.RelayFormatOpenAI, relayconstant.RelayModeChatCompletions, empty, &output))
}

func TestKimiK3OfficialOutputPolicyRetriesReasoningOnlyResponses(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		payload   string
	}{
		{
			name:      "OpenAI Chat reasoning only",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			payload:   `{"choices":[{"message":{"content":"","reasoning_content":"thinking"}}]}`,
		},
		{
			name:      "Responses reasoning only",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			payload:   `{"output":[{"type":"reasoning","content":[{"type":"summary_text","text":"thinking"}]}]}`,
		},
		{
			name:    "Anthropic thinking only",
			format:  types.RelayFormatClaude,
			payload: `{"content":[{"type":"thinking","thinking":"thinking"}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, common.UnmarshalJsonStr(test.payload, &payload))
			var output strings.Builder
			assert.False(t, observeKimiK3VisibleOutputPayload(test.format, test.relayMode, payload, &output))
			assert.Empty(t, output.String())
		})
	}
}

func TestKimiK3OfficialOutputPolicyAcceptsVisibleTextAndTools(t *testing.T) {
	tests := []struct {
		name      string
		format    types.RelayFormat
		relayMode int
		payload   string
	}{
		{
			name:      "OpenAI Chat text",
			format:    types.RelayFormatOpenAI,
			relayMode: relayconstant.RelayModeChatCompletions,
			payload:   `{"choices":[{"message":{"content":"answer"}}]}`,
		},
		{
			name:      "Responses tool",
			format:    types.RelayFormatOpenAIResponses,
			relayMode: relayconstant.RelayModeResponses,
			payload:   `{"output":[{"type":"function_call","name":"lookup","arguments":"{}"}]}`,
		},
		{
			name:    "Anthropic tool",
			format:  types.RelayFormatClaude,
			payload: `{"content":[{"type":"tool_use","name":"lookup","input":{}}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, common.UnmarshalJsonStr(test.payload, &payload))
			var output strings.Builder
			assert.True(t, observeKimiK3VisibleOutputPayload(test.format, test.relayMode, payload, &output))
			assert.NotEmpty(t, output.String())
		})
	}
}

func TestScaleMissingTokenEstimateRoundsUpAndAuditsSaturation(t *testing.T) {
	assert.Equal(t, 4, scaleMissingTokenEstimate(nil, 3, 1.01))

	info := &relaycommon.RelayInfo{}
	assert.Equal(t, math.MaxInt32, scaleMissingTokenEstimate(info, math.MaxInt32, 100))
	require.NotNil(t, info.QuotaClamp)
}

func TestGeminiOutputEventWithUsageIsNotHeldAsTerminalTail(t *testing.T) {
	event := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n")

	assert.False(t, isChannelOutputStreamTailEvent(types.RelayFormatGemini, event))
}
