package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelStatusRoutesUseOperatePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodPost, "/:id/status", authz.ChannelOperate, controller.UpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/status/batch", authz.ChannelOperate, controller.BatchUpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
}

func TestChannelTestRouteUsesOperatePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/test/:id", authz.ChannelOperate, controller.TestChannel)
}

func TestChannelDeleteRoutesUseSensitiveWritePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodDelete, "/:id", authz.ChannelSensitiveWrite, controller.DeleteChannel)
	assertChannelRoutePermission(t, http.MethodPost, "/batch", authz.ChannelSensitiveWrite, controller.DeleteChannelBatch)
	assertChannelRoutePermission(t, http.MethodDelete, "/disabled", authz.ChannelSensitiveWrite, controller.DeleteDisabledChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/tag", authz.ChannelWrite, controller.EditTagChannels)
	assertChannelRoutePermission(t, http.MethodPost, "/batch/tag", authz.ChannelWrite, controller.BatchSetChannelTag)
}

func TestChannelStatusRoutesRegisterWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")

	require.NotPanics(t, func() {
		registerChannelRoutes(api)
	})
}

func TestChannelKeyRequiresSecurityProofFromRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousSessionSecret := common.SessionSecret
	common.RedisEnabled = false
	common.SessionSecret = "channel-key-route-test-secret"

	db, err := gorm.Open(sqlite.Open("file:channel_key_route?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SessionSecret = previousSessionSecret
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Channel{}, &model.Log{}))
	root := &model.User{
		Id:          1,
		Username:    "root",
		Password:    "test-password",
		Role:        common.RoleRootUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AffCode:     "root-channel-key",
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(root).Error)
	rootAuth := createChannelRouterAuth(t, root)
	channel := &model.Channel{Id: 1, Name: "test-channel", Key: "sk-upstream-secret"}
	require.NoError(t, db.Create(channel).Error)

	engine := gin.New()
	engine.Use(skipAutomaticAdminAudit())
	registerChannelRoutes(engine.Group("/api"))

	request := httptest.NewRequest(http.MethodPost, "/api/channel/1/key", nil)
	request.Header.Set("Authorization", "Bearer "+rootAuth.accessToken)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"SECURITY_PROOF_REQUIRED"`)
	assert.NotContains(t, recorder.Body.String(), channel.Key)

	proofToken, _, err := service.IssueSecurityProof(rootAuth.identity, "2fa", []string{"channel.key.read"})
	require.NoError(t, err)
	request = httptest.NewRequest(http.MethodPost, "/api/channel/1/key", nil)
	request.Header.Set("Authorization", "Bearer "+rootAuth.accessToken)
	request.Header.Set("X-Security-Proof", proofToken)
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, channel.Key, response.Data.Key)

	var auditLog model.Log
	require.NoError(t, db.Where("type = ? AND content = ?", model.LogTypeManage, "Viewed channel key test-channel (ID: 1)").First(&auditLog).Error)
	assert.Equal(t, "Viewed channel key test-channel (ID: 1)", auditLog.Content)

	admin := &model.User{
		Id: 2, Username: "admin", Password: "test-password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AffCode: "admin-channel-key", AuthVersion: 1,
	}
	require.NoError(t, db.Create(admin).Error)
	adminAuth := createChannelRouterAuth(t, admin)

	adminRequest := httptest.NewRequest(http.MethodPost, "/api/channel/1/key", nil)
	adminRequest.Header.Set("Authorization", "Bearer "+adminAuth.accessToken)
	adminRequest.Header.Set("X-Security-Proof", proofToken)
	adminRecorder := httptest.NewRecorder()
	engine.ServeHTTP(adminRecorder, adminRequest)

	require.Equal(t, http.StatusForbidden, adminRecorder.Code)
	assert.NotContains(t, adminRecorder.Body.String(), channel.Key)
	assert.Contains(t, adminRecorder.Body.String(), `"success":false`)
}

func TestMultiKeySecretsAreMaskedAndRootCanRevealOneWithoutTwoFactor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousCriticalRateLimitEnabled := common.CriticalRateLimitEnable
	previousSessionSecret := common.SessionSecret
	common.RedisEnabled = false
	common.CriticalRateLimitEnable = false
	common.SessionSecret = "multi-key-secret-route-test-secret"

	db, err := gorm.Open(sqlite.Open("file:multi_key_secret_route?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.CriticalRateLimitEnable = previousCriticalRateLimitEnabled
		common.SessionSecret = previousSessionSecret
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Channel{}, &model.Log{}))
	root := &model.User{
		Id:          1,
		Username:    "root",
		Password:    "test-password",
		Role:        common.RoleRootUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AffCode:     "root-multi-key",
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(root).Error)
	rootAuth := createChannelRouterAuth(t, root)
	require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "single-channel", Key: "single-secret"}).Error)

	longKey := "sk-abcdefghijklmnopqrstuvwxyz"
	shortKey := "abc"
	multiKey := &model.Channel{
		Id:   2,
		Name: "multi-channel",
		Key:  strings.Join([]string{longKey, shortKey, "", "xy"}, "\n"),
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 4,
		},
	}
	require.NoError(t, db.Create(multiKey).Error)

	engine := gin.New()
	engine.Use(skipAutomaticAdminAudit())
	registerChannelRoutes(engine.Group("/api"))

	sendRootPost := func(target string, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+rootAuth.accessToken)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	statusRecorder := sendRootPost(
		"/api/channel/multi_key/manage",
		`{"channel_id":2,"action":"get_key_status","page":1,"page_size":10}`,
	)
	require.Equal(t, http.StatusOK, statusRecorder.Code)
	var statusResponse struct {
		Success bool `json:"success"`
		Data    struct {
			Keys []controller.KeyStatus `json:"keys"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(statusRecorder.Body.Bytes(), &statusResponse))
	require.True(t, statusResponse.Success)
	require.Len(t, statusResponse.Data.Keys, 4)
	assert.Equal(t, model.MaskTokenKey(longKey), statusResponse.Data.Keys[0].MaskedKey)
	assert.Equal(t, "***", statusResponse.Data.Keys[1].MaskedKey)
	assert.Equal(t, "", statusResponse.Data.Keys[2].MaskedKey)
	assert.Equal(t, "**", statusResponse.Data.Keys[3].MaskedKey)
	assert.Equal(t, "***", statusResponse.Data.Keys[1].KeyPreview)
	assert.NotContains(t, statusRecorder.Body.String(), longKey)
	assert.NotContains(t, statusRecorder.Body.String(), `"abc"`)

	revealRecorder := sendRootPost("/api/channel/2/multi_key/1/key", "")
	require.Equal(t, http.StatusOK, revealRecorder.Code)
	var revealResponse struct {
		Success bool `json:"success"`
		Data    struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(revealRecorder.Body.Bytes(), &revealResponse))
	assert.True(t, revealResponse.Success)
	assert.Equal(t, shortKey, revealResponse.Data.Key)
	assert.Equal(t, "no-store, no-cache, must-revalidate, private, max-age=0", revealRecorder.Header().Get("Cache-Control"))

	var auditLog model.Log
	require.NoError(t, db.Where("content = ?", "Viewed multi-key #2 for channel multi-channel (ID: 2)").First(&auditLog).Error)
	var auditOther map[string]interface{}
	require.NoError(t, common.UnmarshalJsonStr(auditLog.Other, &auditOther))
	op, ok := auditOther["op"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "channel.multi_key_view", op["action"])
	params, ok := op["params"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(2), params["id"])
	assert.Equal(t, "multi-channel", params["name"])
	assert.Equal(t, float64(1), params["key_index"])
	assert.Equal(t, float64(2), params["key_number"])

	invalidTargets := []string{
		"/api/channel/999/multi_key/0/key",
		"/api/channel/1/multi_key/0/key",
		"/api/channel/2/multi_key/-1/key",
		"/api/channel/2/multi_key/4/key",
	}
	for _, target := range invalidTargets {
		recorder := sendRootPost(target, "")
		assert.Contains(t, recorder.Body.String(), `"success":false`, target)
		assert.NotContains(t, recorder.Body.String(), longKey, target)
	}

	admin := &model.User{
		Id: 2, Username: "admin", Password: "test-password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AffCode: "admin-multi-key", AuthVersion: 1,
	}
	require.NoError(t, db.Create(admin).Error)
	adminAuth := createChannelRouterAuth(t, admin)
	adminRequest := httptest.NewRequest(http.MethodPost, "/api/channel/2/multi_key/1/key", nil)
	adminRequest.Header.Set("Authorization", "Bearer "+adminAuth.accessToken)
	adminRecorder := httptest.NewRecorder()
	engine.ServeHTTP(adminRecorder, adminRequest)

	require.Equal(t, http.StatusForbidden, adminRecorder.Code)
	assert.Contains(t, adminRecorder.Body.String(), `"success":false`)
	assert.NotContains(t, adminRecorder.Body.String(), shortKey)
}

type channelRouterAuthFixture struct {
	accessToken string
	identity    service.AuthIdentity
}

func createChannelRouterAuth(t *testing.T, user *model.User) channelRouterAuthFixture {
	t.Helper()
	now := time.Now().Unix()
	identity := service.AuthIdentity{
		UserID:          user.Id,
		SessionID:       user.Username + "-session",
		UserAuthVersion: user.AuthVersion,
		SessionVersion:  1,
	}
	require.NoError(t, model.CreateUserSession(&model.UserSession{
		SID:             identity.SessionID,
		UserID:          identity.UserID,
		Version:         identity.SessionVersion,
		UserAuthVersion: identity.UserAuthVersion,
		Status:          model.UserSessionStatusActive,
		RefreshHash:     strings.Repeat("a", 64),
		LoginMethod:     "password",
		LastActiveAt:    now,
		ExpiresAt:       now + 3600,
	}))
	accessToken, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	return channelRouterAuthFixture{accessToken: accessToken, identity: identity}
}

func skipAutomaticAdminAudit() gin.HandlerFunc {
	return func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyAuditLogged, true)
		c.Next()
	}
}

func assertChannelRoutePermission(t *testing.T, method string, path string, permission authz.Permission, handler any) {
	t.Helper()
	for _, route := range channelPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, permission, route.permission)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}
