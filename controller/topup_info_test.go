package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTopUpInfoIncludesReplacementHTML(t *testing.T) {
	oldTopUpHTML := common.TopUpHTML
	common.TopUpHTML = `<p>Contact billing</p>`
	t.Cleanup(func() {
		common.TopUpHTML = oldTopUpHTML
	})

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/user/topup", nil)

	GetTopUpInfo(c)

	require.Equal(t, http.StatusOK, w.Code)
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			TopUpHTML string `json:"topup_html"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	assert.Equal(t, `<p>Contact billing</p>`, payload.Data.TopUpHTML)
}
