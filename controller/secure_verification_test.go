package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUniversalVerifyIssuesPasswordProofOnlyForCurrentRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousSecret := common.SessionSecret
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	model.LOG_DB = db
	common.SessionSecret = "system-backup-password-proof-test-secret"
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SessionSecret = previousSecret
		common.RedisEnabled = previousRedisEnabled
	})

	passwordHash, err := common.Password2Hash("RootPassword123")
	require.NoError(t, err)
	root := model.User{
		Username: "root-user", Password: passwordHash, Role: common.RoleRootUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&root).Error)
	identity := service.AuthIdentity{UserID: root.Id, SessionID: "root-session", UserAuthVersion: 1, SessionVersion: 1}

	tests := []struct {
		name     string
		username string
		password string
		role     int
		status   int
		success  bool
	}{
		{
			name: "matching current root", username: "root-user", password: "RootPassword123",
			role: common.RoleRootUser, status: common.UserStatusEnabled, success: true,
		},
		{
			name: "different username", username: "other-root", password: "RootPassword123",
			role: common.RoleRootUser, status: common.UserStatusEnabled,
		},
		{
			name: "wrong password", username: "root-user", password: "WrongPassword123",
			role: common.RoleRootUser, status: common.UserStatusEnabled,
		},
		{
			name: "non-root administrator", username: "root-user", password: "RootPassword123",
			role: common.RoleAdminUser, status: common.UserStatusEnabled,
		},
		{
			name: "disabled root", username: "root-user", password: "RootPassword123",
			role: common.RoleRootUser, status: common.UserStatusDisabled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", root.Id).Updates(map[string]interface{}{
				"role": test.role, "status": test.status,
			}).Error)
			body := fmt.Sprintf(`{"method":"password","scope":%q,"username":%q,"password":%q}`,
				constant.SecurityProofScopeSystemBackupExport, test.username, test.password)
			request := httptest.NewRequest(http.MethodPost, "/api/verify", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = request
			context.Set("id", identity.UserID)
			context.Set("session_id", identity.SessionID)
			context.Set("auth_version", identity.UserAuthVersion)
			context.Set("session_version", identity.SessionVersion)

			UniversalVerify(context)

			var result struct {
				Success bool `json:"success"`
				Data    struct {
					ProofToken string `json:"proof_token"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, test.success, result.Success)
			if !test.success {
				assert.Empty(t, result.Data.ProofToken)
				return
			}
			method, err := service.VerifySecurityProof(
				result.Data.ProofToken,
				identity,
				constant.SecurityProofScopeSystemBackupExport,
				[]string{constant.SecurityProofMethodPassword},
			)
			require.NoError(t, err)
			assert.Equal(t, constant.SecurityProofMethodPassword, method)
		})
	}
}
