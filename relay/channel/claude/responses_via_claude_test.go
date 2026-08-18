package claude

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newKimiK3ClaudeResponsesContext(t *testing.T, body string, stream bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "resp_kimi_claude")
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	maxOutputTokens := uint(64)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAIResponses,
		RelayMode:       relayconstant.RelayModeResponses,
		IsStream:        stream,
		OriginModelName: "client-k3",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeAnthropic,
			UpstreamModelName: "kimi-k3",
			IsModelMapped:     true,
			ChannelOtherSettings: dto.ChannelOtherSettings{
				KimiK3OfficialCompatibility: true,
			},
		},
		Request: &dto.OpenAIResponsesRequest{
			Model:           "client-k3",
			MaxOutputTokens: &maxOutputTokens,
			Metadata:        []byte(`{"trace":"claude"}`),
			Reasoning:       &dto.Reasoning{Effort: "high"},
		},
	}
	info.ActivateKimiK3OfficialCompatibility()
	return c, recorder, resp, info
}

func TestClaudeToResponsesHandlerRestoresKimiK3MetadataModelAndUsage(t *testing.T) {
	body := `{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"kimi-k3",
		"content":[{"type":"text","text":"OK"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":2,"cache_read_input_tokens":3,"output_tokens":1}
	}`
	c, recorder, resp, info := newKimiK3ClaudeResponsesContext(t, body, false)

	usage, apiErr := ClaudeToResponsesHandler(c, resp, info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIResponses, usage.BillingUsage.Source)
	assert.Equal(t, 3, usage.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)
	assert.Nil(t, usage.BillingUsage.ClaudeUsage)

	var got struct {
		Model           string            `json:"model"`
		MaxOutputTokens uint              `json:"max_output_tokens"`
		Metadata        map[string]string `json:"metadata"`
		Reasoning       *dto.Reasoning    `json:"reasoning"`
		Usage           *dto.Usage        `json:"usage"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &got))
	assert.Equal(t, "client-k3", got.Model)
	assert.Equal(t, uint(64), got.MaxOutputTokens)
	assert.Equal(t, map[string]string{"trace": "claude"}, got.Metadata)
	require.NotNil(t, got.Reasoning)
	assert.Equal(t, "high", got.Reasoning.Effort)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 1, got.Usage.OutputTokens)
	assert.Equal(t, "oai_responses", gjson.Get(recorder.Body.String(), "usage.billing_usage.source").String())
	assert.True(t, gjson.Get(recorder.Body.String(), "usage.billing_usage.openai_usage").Exists())
	assert.False(t, gjson.Get(recorder.Body.String(), "usage.billing_usage.claude_usage").Exists())
}

func TestClaudeToResponsesStreamHandlerRestoresKimiK3MetadataAndTerminalUsage(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"kimi-k3","content":[],"usage":{"input_tokens":2,"cache_read_input_tokens":3,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n\n")
	c, recorder, resp, info := newKimiK3ClaudeResponsesContext(t, body, true)

	usage, apiErr := ClaudeToResponsesStreamHandler(c, resp, info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 1, usage.CompletionTokens)
	require.NotNil(t, usage.BillingUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIResponses, usage.BillingUsage.Source)
	assert.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Nil(t, usage.BillingUsage.ClaudeUsage)

	got := recorder.Body.String()
	assert.Contains(t, got, `"type":"response.output_text.delta"`)
	assert.Contains(t, got, `"delta":"OK"`)
	assert.Contains(t, got, `"type":"response.completed"`)
	assert.NotContains(t, got, `"model":"kimi-k3"`)
	assert.Equal(t, 2, strings.Count(got, `"model":"client-k3"`))
	assert.Equal(t, 2, strings.Count(got, `"max_output_tokens":64`))
	assert.NotContains(t, got, `"max_output_tokens":0`)
}

func TestDeepSeekV4ClaudeAdaptorReturnsResponsesProtocol(t *testing.T) {
	body := `{
		"id":"msg_deepseek_1",
		"type":"message",
		"role":"assistant",
		"model":"deepseek-v4-pro",
		"content":[{"type":"text","text":"OK"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":2,"output_tokens":1}
	}`
	c, recorder, resp, info := newKimiK3ClaudeResponsesContext(t, body, false)
	info.OriginModelName = "deepseek-v4-pro-0813"
	info.Request = &dto.OpenAIResponsesRequest{
		Model: "deepseek-v4-pro-0813",
		Input: []byte(`"hello"`),
	}
	info.ChannelMeta.UpstreamModelName = "deepseek-v4-pro"
	info.ChannelMeta.ChannelOtherSettings = dto.ChannelOtherSettings{DeepSeekV4OfficialCompatibility: true}
	info.KimiK3OfficialCompatibilityActive = false
	info.ActivateDeepSeekV4OfficialCompatibility()
	require.True(t, info.IsDeepSeekV4OfficialCompatibility())
	require.False(t, info.IsOfficialCompatibility())

	usageValue, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	usage := usageValue.(*dto.Usage)
	assert.Equal(t, 1, usage.CompletionTokens)

	var response dto.OpenAIResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "response", response.Object)
	assert.Equal(t, "deepseek-v4-pro-0813", response.Model)
	require.Len(t, response.Output, 1)
	assert.Equal(t, "message", response.Output[0].Type)
}
