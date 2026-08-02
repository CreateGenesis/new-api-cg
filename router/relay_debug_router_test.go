package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelayDebugTraceRouteIsRootOnlyAndReportsStorageState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	previousSessionSecret := common.SessionSecret
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.SessionSecret = "relay-debug-route-test-secret"
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	db, err := gorm.Open(sqlite.Open("file:relay_debug_route?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Log{}, &model.RelayDebugPayload{}))
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
		common.SessionSecret = previousSessionSecret
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	root := &model.User{Id: 1, Username: "relay-debug-root", Password: "unused", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "relay-debug-root-aff", AuthVersion: 1}
	admin := &model.User{Id: 2, Username: "relay-debug-admin", Password: "unused", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "relay-debug-admin-aff", AuthVersion: 1}
	require.NoError(t, db.Create(root).Error)
	require.NoError(t, db.Create(admin).Error)
	rootAuth := createChannelRouterAuth(t, root)
	adminAuth := createChannelRouterAuth(t, admin)

	trace := service.RelayDebugTrace{Version: 1, RequestId: "request-success", Outcome: "failed"}
	payload, err := common.Marshal(trace)
	require.NoError(t, err)
	require.NoError(t, model.SaveRelayDebugPayload(context.Background(), trace.RequestId, payload))
	mismatchedPayload, err := common.Marshal(service.RelayDebugTrace{Version: 1, RequestId: "different-request", Outcome: "failed"})
	require.NoError(t, err)
	require.NoError(t, model.SaveRelayDebugPayload(context.Background(), "request-mismatch", mismatchedPayload))
	require.NoError(t, model.SaveRelayDebugPayload(context.Background(), "request-corrupted", payload))
	require.NoError(t, db.Model(&model.RelayDebugPayload{}).
		Where("request_id = ? AND chunk_index = ?", "request-corrupted", 0).
		Update("payload", []byte("corrupted")).Error)

	engine := gin.New()
	engine.Use(skipAutomaticAdminAudit())
	engine.GET("/api/log/:request_id/relay-debug", middleware.RootAuth(), controller.GetRelayDebugTrace)

	rootRequest := httptest.NewRequest(http.MethodGet, "/api/log/request-success/relay-debug", nil)
	rootRequest.Header.Set("Authorization", "Bearer "+rootAuth.accessToken)
	rootResponse := httptest.NewRecorder()
	engine.ServeHTTP(rootResponse, rootRequest)
	require.Equal(t, http.StatusOK, rootResponse.Code)
	assert.Equal(t, "no-store", rootResponse.Header().Get("Cache-Control"))
	assert.Contains(t, rootResponse.Body.String(), `"request_id":"request-success"`)

	adminRequest := httptest.NewRequest(http.MethodGet, "/api/log/request-success/relay-debug", nil)
	adminRequest.Header.Set("Authorization", "Bearer "+adminAuth.accessToken)
	adminResponse := httptest.NewRecorder()
	engine.ServeHTTP(adminResponse, adminRequest)
	assert.Equal(t, http.StatusForbidden, adminResponse.Code)
	assert.NotContains(t, adminResponse.Body.String(), `"outcome"`)

	for _, test := range []struct {
		requestId string
		status    int
		code      string
	}{
		{requestId: "request-missing", status: http.StatusNotFound, code: "RELAY_DEBUG_TRACE_NOT_FOUND"},
		{requestId: "request-corrupted", status: http.StatusInternalServerError, code: "RELAY_DEBUG_TRACE_UNAVAILABLE"},
		{requestId: "request-mismatch", status: http.StatusInternalServerError, code: "RELAY_DEBUG_TRACE_UNAVAILABLE"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/log/"+test.requestId+"/relay-debug", nil)
		request.Header.Set("Authorization", "Bearer "+rootAuth.accessToken)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		assert.Equal(t, test.status, response.Code, test.requestId)
		assert.Contains(t, response.Body.String(), test.code, test.requestId)
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"), test.requestId)
	}
}
