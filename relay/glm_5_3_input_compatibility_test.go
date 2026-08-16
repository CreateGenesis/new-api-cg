package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGLM53FallbackDoesNotLoosenChannelsWithoutCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		format types.RelayFormat
		path   string
		body   string
	}{
		{
			name:   "chat",
			format: types.RelayFormatOpenAI,
			path:   "/v1/chat/completions",
			body:   `{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}],"max_tokens":"8"}`,
		},
		{
			name:   "claude",
			format: types.RelayFormatClaude,
			path:   "/v1/messages",
			body:   `{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}],"max_tokens":true}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "glm-5.3")

			request, err := helper.GetAndValidateRequest(c, test.format)
			require.NoError(t, err)
			info := &relaycommon.RelayInfo{
				OriginModelName: "glm-5.3",
				RelayFormat:     test.format,
				Request:         request,
			}

			var apiErr *types.NewAPIError
			if test.format == types.RelayFormatClaude {
				apiErr = ClaudeHelper(c, info)
			} else {
				apiErr = TextHelper(c, info)
			}
			require.NotNil(t, apiErr)
			assert.Contains(t, apiErr.Error(), "cannot unmarshal")
		})
	}
}
