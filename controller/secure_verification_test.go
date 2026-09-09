package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}, &model.AuditLog{}, &model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousEncryption := common.PasswordLoginEncryptionEnabled
	common.PasswordLoginEncryptionEnabled = false
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
		common.PasswordLoginEncryptionEnabled = previousEncryption
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
	require.NoError(t, db.Create(&model.UserSession{SID: identity.SessionID, UserID: root.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "test-refresh-hash", ExpiresAt: time.Now().Add(time.Hour).Unix()}).Error)

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
			operation := service.VerificationOperation{Scope: constant.SecurityProofScopeSystemBackupExport}
			_, err := service.ConsumeOperationProof(result.Data.ProofToken, identity, service.VerificationOperation{Scope: constant.SecurityProofScopeSystemBackupImport})
			assert.ErrorIs(t, err, service.ErrProofScope)
			wrongSession := identity
			wrongSession.SessionID = "another-session"
			_, err = service.ConsumeOperationProof(result.Data.ProofToken, wrongSession, operation)
			assert.ErrorIs(t, err, service.ErrAuthTokenInvalid)
			authorization, err := service.ConsumeOperationProof(result.Data.ProofToken, identity, operation)
			require.NoError(t, err)
			assert.Equal(t, constant.SecurityProofMethodPassword, authorization.Method)
			_, err = service.ConsumeOperationProof(result.Data.ProofToken, identity, operation)
			assert.ErrorIs(t, err, service.ErrProofConsumed)
		})
	}
}

func TestSystemBackupProofRejectsExpiryMethodBypassAndPlaintextWhenEncryptionRequired(t *testing.T) {
	for _, scope := range []string{constant.SecurityProofScopeSystemBackupExport, constant.SecurityProofScopeSystemBackupImport} {
		t.Run(scope, func(t *testing.T) {
			user, identity := setupSecurityEnrollmentTest(t)
			require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
			input := service.VerificationInput{Username: user.Username, Password: "enrollment-password", Method: service.VerificationMethodPassword, Scope: scope}
			for _, method := range []string{service.VerificationMethodSession, service.VerificationMethodTwoFA, service.VerificationMethodPasskey, service.VerificationMethodOAuth} {
				bypass := input
				bypass.Method = method
				_, err := service.VerifySecurityInput(identity, bypass)
				assert.ErrorIs(t, err, service.ErrProofMethod)
			}
			common.PasswordLoginEncryptionEnabled = true
			_, err := service.VerifySecurityInput(identity, input)
			assert.ErrorIs(t, err, service.ErrVerificationFailed)
			kid, publicPEM := common.PasswordEncryptionPublicKey()
			if kid == "" {
				privatePEM, err := common.GeneratePasswordEncryptionPrivateKey()
				require.NoError(t, err)
				require.NoError(t, common.LoadPasswordEncryptionPrivateKey(privatePEM))
				kid, publicPEM = common.PasswordEncryptionPublicKey()
			}
			block, _ := pem.Decode([]byte(publicPEM))
			require.NotNil(t, block)
			parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
			require.NoError(t, err)
			pub, ok := parsed.(*rsa.PublicKey)
			require.True(t, ok)
			ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, []byte(input.Password), nil)
			require.NoError(t, err)
			input.Password = ""
			input.EncryptionKeyID = kid
			input.PasswordEncrypted = base64.StdEncoding.EncodeToString(ciphertext)
			proof, err := service.VerifySecurityInput(identity, input)
			require.NoError(t, err)
			require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("user_id = ?", user.Id).Update("expires_at", time.Now().Add(-time.Minute)).Error)
			_, err = service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: scope})
			assert.ErrorIs(t, err, service.ErrAuthTokenExpired)
		})
	}
}
