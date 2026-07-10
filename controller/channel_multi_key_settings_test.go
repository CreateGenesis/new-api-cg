package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddChannelRejectsInvalidLeastRequestsWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload, err := common.Marshal(AddChannelRequest{
		Mode:                               "multi_to_single",
		MultiKeyMode:                       constant.MultiKeyModeLeastRequests,
		MultiKeyLeastRequestsWindowSeconds: 15,
		Channel: &model.Channel{
			Type:   constant.ChannelTypeOpenAI,
			Name:   "least-requests",
			Key:    "key-a\nkey-b",
			Models: "gpt-4o",
			Group:  "default",
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")

	AddChannel(ctx)

	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Contains(t, response.Message, "multiple of 10")
}
