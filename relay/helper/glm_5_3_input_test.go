package helper

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalBodyWithGLM53FallbackDefersCompatibilityDecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		format types.RelayFormat
		body   string
		newDTO func() any
		check  func(*testing.T, any)
	}{
		{
			name:   "chat scalar coercions",
			format: types.RelayFormatOpenAI,
			body:   `{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}],"max_tokens":"8","top_k":"1","top_p":"0.5","temperature":"","max_completion_tokens":{},"stop":[123,true]}`,
			newDTO: func() any { return &dto.GeneralOpenAIRequest{} },
			check: func(t *testing.T, value any) {
				request := value.(*dto.GeneralOpenAIRequest)
				require.NotNil(t, request.MaxTokens)
				assert.Equal(t, uint(8), *request.MaxTokens)
				require.NotNil(t, request.TopK)
				assert.Equal(t, 1, *request.TopK)
				assert.Nil(t, request.Temperature)
				assert.Nil(t, request.MaxCompletionTokens)
				assert.Equal(t, []any{"123", "true"}, request.Stop)
			},
		},
		{
			name:   "claude scalar coercions",
			format: types.RelayFormatClaude,
			body:   `{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}],"max_tokens":true,"top_k":"0","top_p":false,"temperature":true,"max_tokens_to_sample":{},"response_format":"text"}`,
			newDTO: func() any { return &dto.ClaudeRequest{} },
			check: func(t *testing.T, value any) {
				request := value.(*dto.ClaudeRequest)
				require.NotNil(t, request.MaxTokens)
				assert.Equal(t, uint(1), *request.MaxTokens)
				require.NotNil(t, request.TopK)
				assert.Equal(t, 0, *request.TopK)
				assert.Nil(t, request.MaxTokensToSample)
				assert.JSONEq(t, `"text"`, string(request.ResponseFormat))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			c.Request.Header.Set("Content-Type", "application/json")
			target := test.newDTO()

			require.NoError(t, unmarshalBodyWithGLM53Fallback(c, target, test.format))
			require.Error(t, GLM53CompatibilityDecodeError(c))
			test.check(t, target)
		})
	}
}

func TestNormalizeGLM53RequestBodyUsesOriginalJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := `{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}],"stop":[1.0,true,"END"]}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	request := &dto.GeneralOpenAIRequest{}
	require.NoError(t, NormalizeGLM53RequestBody(c, types.RelayFormatOpenAI, request))
	assert.Equal(t, []any{"1.0", "true", "END"}, request.Stop)
	assert.Equal(t, "max", request.ReasoningEffort)
}
