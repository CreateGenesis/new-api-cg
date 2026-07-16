package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayValidationErrorsReturnBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalErrorLogEnabled := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = false
	t.Cleanup(func() { constant.ErrorLogEnabled = originalErrorLogEnabled })

	tests := []struct {
		name            string
		path            string
		body            string
		format          types.RelayFormat
		expectedMessage string
	}{
		{
			name:            "claude messages are required",
			path:            "/v1/messages",
			body:            `{"model":"claude-test"}`,
			format:          types.RelayFormatClaude,
			expectedMessage: "field messages is required",
		},
		{
			name:            "openai messages are required",
			path:            "/v1/chat/completions",
			body:            `{"model":"gpt-test"}`,
			format:          types.RelayFormatOpenAI,
			expectedMessage: "field messages is required",
		},
		{
			name:            "responses input is required",
			path:            "/v1/responses",
			body:            `{"model":"gpt-test"}`,
			format:          types.RelayFormatOpenAIResponses,
			expectedMessage: "input is required",
		},
		{
			name:            "gemini contents are required",
			path:            "/v1beta/models/gemini-test:generateContent",
			body:            `{}`,
			format:          types.RelayFormatGemini,
			expectedMessage: "contents is required",
		},
		{
			name:            "nested rerank validation error",
			path:            "/v1/rerank",
			body:            `{}`,
			format:          types.RelayFormatRerank,
			expectedMessage: "query is empty",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Set(common.RequestIdKey, "validation-test")
			t.Cleanup(func() { common.CleanupBodyStorage(ctx) })

			Relay(ctx, test.format)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Contains(t, recorder.Body.String(), test.expectedMessage)
		})
	}
}

func TestRelaySensitiveWordRejectionReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalErrorLogEnabled := constant.ErrorLogEnabled
	originalCheckSensitiveEnabled := setting.CheckSensitiveEnabled
	originalCheckSensitiveOnPromptEnabled := setting.CheckSensitiveOnPromptEnabled
	originalSensitiveWords := append([]string(nil), setting.SensitiveWords...)
	constant.ErrorLogEnabled = false
	setting.CheckSensitiveEnabled = true
	setting.CheckSensitiveOnPromptEnabled = true
	setting.SensitiveWords = []string{"blocked-word"}
	t.Cleanup(func() {
		constant.ErrorLogEnabled = originalErrorLogEnabled
		setting.CheckSensitiveEnabled = originalCheckSensitiveEnabled
		setting.CheckSensitiveOnPromptEnabled = originalCheckSensitiveOnPromptEnabled
		setting.SensitiveWords = originalSensitiveWords
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"blocked-word"}]}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set(common.RequestIdKey, "sensitive-test")
	t.Cleanup(func() { common.CleanupBodyStorage(ctx) })

	Relay(ctx, types.RelayFormatOpenAI)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "sensitive words detected")
}

func TestPlaygroundAccessTokenRejectionReturnsForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/pg/chat/completions", nil)
	ctx.Set("use_access_token", true)

	Playground(ctx)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "access token")
}
