package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/utils/tests"
)

func setupSystemBackupTest(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Option{}, &model.Channel{}, &model.Ability{}, &model.Vendor{}, &model.Model{},
		&model.PrefillGroup{}, &model.Setup{}, &model.CustomOAuthProvider{}, &model.SubscriptionPlan{},
		&model.AuthzRole{}, &model.CasbinRule{}, &model.User{}, &model.Token{}, &model.Redemption{},
		&model.TwoFA{}, &model.TwoFABackupCode{}, &model.PasskeyCredential{},
		&model.ExternalIdentityClaim{}, &model.UserOAuthBinding{}, &model.UserSubscription{},
		&model.UserSession{}, &model.AuthFlow{}, &model.SubscriptionPreConsumeRecord{}, &model.Log{},
	))

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousDatabaseType := common.MainDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	model.DB = db
	model.LOG_DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousDatabaseType)
		common.RedisEnabled = previousRedisEnabled
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})
	return db
}

func seedSystemBackupSource(t *testing.T, db *gorm.DB) {
	t.Helper()
	passwordHash, err := common.Password2Hash("SourcePassword123")
	require.NoError(t, err)
	accessToken := "source-admin-access-token"
	require.NoError(t, db.Create(&model.Option{Key: "custom.integration_secret", Value: "option-secret"}).Error)
	require.NoError(t, db.Create(&model.Vendor{Id: 10, Name: "Source vendor", Status: 1}).Error)
	require.NoError(t, db.Create(&model.Model{Id: 20, ModelName: "source-model", VendorID: 10, Status: 1}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id: 30, Name: "source-channel", Type: 1, Key: "sk-channel-secret", Status: common.ChannelStatusEnabled,
		Models: "source-model", Group: "default", Weight: common.GetPointer(uint(0)), BaseURL: common.GetPointer(""),
		StatusCodeMapping: common.GetPointer(""), Priority: common.GetPointer(int64(0)), AutoBan: common.GetPointer(1),
	}).Error)
	require.NoError(t, db.Create(&model.CustomOAuthProvider{
		Id: 40, Name: "Source OAuth", Slug: "source-oauth", ClientId: "client-id", ClientSecret: "oauth-secret",
	}).Error)
	require.NoError(t, db.Create(&model.User{
		Id: 50, Username: "root-source", Password: passwordHash, Role: common.RoleRootUser,
		Status: common.UserStatusEnabled, Group: "default", AccessToken: &accessToken, AuthVersion: 3,
	}).Error)
	require.NoError(t, db.Create(&model.Token{
		Id: 60, UserId: 50, Key: "source-api-token", Name: "source-token", Status: 1, Group: "default",
	}).Error)
	require.NoError(t, db.Create(&model.TwoFA{
		Id: 70, UserId: 50, Secret: "totp-secret", IsEnabled: true,
	}).Error)
	require.NoError(t, db.Create(&model.TwoFABackupCode{
		Id: 80, UserId: 50, CodeHash: "backup-code-hash",
	}).Error)
}

func TestSystemBackupExportQuotesReservedOptionKey(t *testing.T) {
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true})
	require.NoError(t, err)

	queries := make([]string, 0, 1)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("system_backup:capture_sql", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "options" {
			queries = append(queries, tx.Statement.SQL.String())
		}
	}))

	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	_, err = ExportSystemBackup()
	require.NoError(t, err)
	require.Len(t, queries, 1)
	assert.Contains(t, strings.ToLower(queries[0]), "order by `key`")
}

func TestSystemBackupRoundTripIncludesSecretsAndReplacesTarget(t *testing.T) {
	db := setupSystemBackupTest(t)
	seedSystemBackupSource(t, db)

	backup, err := ExportSystemBackup()
	require.NoError(t, err)
	raw, err := common.Marshal(backup)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "option-secret")
	assert.Contains(t, string(raw), "sk-channel-secret")
	assert.Contains(t, string(raw), "source-admin-access-token")
	assert.Contains(t, string(raw), "source-api-token")
	assert.Contains(t, string(raw), "oauth-secret")
	assert.Contains(t, string(raw), "totp-secret")
	assert.Contains(t, string(raw), "backup-code-hash")
	assert.NotContains(t, string(raw), "original_password")
	assert.NotContains(t, string(raw), "verification_code")

	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", "custom.integration_secret").Update("value", "target-secret").Error)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 30).Update("key", "target-channel-key").Error)
	require.NoError(t, db.Model(&model.CustomOAuthProvider{}).Where("id = ?", 40).Update("client_secret", "target-oauth-secret").Error)
	targetPassword, err := common.Password2Hash("TargetPassword123")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", 50).Update("password", targetPassword).Error)
	require.NoError(t, db.Create(&model.User{
		Id: 99, Username: "target-extra", Password: targetPassword, Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AffCode: "target-extra-aff", AuthVersion: 1,
	}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 100, UserId: 99, Key: "target-extra-token", Name: "extra"}).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "target-session", UserID: 50, Version: 1, UserAuthVersion: 3, Status: model.UserSessionStatusActive,
		RefreshHash: "refresh-hash", LoginMethod: "password", CreatedAt: 1, LastActiveAt: 1, ExpiresAt: 9999999999,
	}).Error)

	preview, err := PreviewSystemBackupImport(raw)
	require.NoError(t, err)
	assert.Equal(t, 2, preview.Sections["users"].Delete+preview.Sections["tokens"].Delete)
	assert.True(t, preview.RequiresLogout)
	assert.Equal(t, backup.recordCount(), preview.RecordCount)

	_, err = ApplySystemBackupImport(raw, preview.Hash)
	require.NoError(t, err)

	var restoredOption model.Option
	require.NoError(t, db.First(&restoredOption, "key = ?", "custom.integration_secret").Error)
	assert.Equal(t, "option-secret", restoredOption.Value)
	var restoredChannel model.Channel
	require.NoError(t, db.First(&restoredChannel, 30).Error)
	assert.Equal(t, "sk-channel-secret", restoredChannel.Key)
	var restoredProvider model.CustomOAuthProvider
	require.NoError(t, db.First(&restoredProvider, 40).Error)
	assert.Equal(t, "oauth-secret", restoredProvider.ClientSecret)
	var restoredUser model.User
	require.NoError(t, db.First(&restoredUser, 50).Error)
	assert.True(t, common.ValidatePasswordAndHash("SourcePassword123", restoredUser.Password))
	require.NotNil(t, restoredUser.AccessToken)
	assert.Equal(t, "source-admin-access-token", *restoredUser.AccessToken)
	var restoredToken model.Token
	require.NoError(t, db.First(&restoredToken, 60).Error)
	assert.Equal(t, "source-api-token", restoredToken.Key)
	var restoredTwoFA model.TwoFA
	require.NoError(t, db.First(&restoredTwoFA, 70).Error)
	assert.Equal(t, "totp-secret", restoredTwoFA.Secret)
	var restoredBackupCode model.TwoFABackupCode
	require.NoError(t, db.First(&restoredBackupCode, 80).Error)
	assert.Equal(t, "backup-code-hash", restoredBackupCode.CodeHash)
	var extraUsers int64
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("id = ?", 99).Count(&extraUsers).Error)
	assert.Zero(t, extraUsers)
	var sessions int64
	require.NoError(t, db.Model(&model.UserSession{}).Count(&sessions).Error)
	assert.Zero(t, sessions)
	var ability model.Ability
	require.NoError(t, db.First(&ability, "channel_id = ? AND model = ?", 30, "source-model").Error)
	assert.True(t, ability.Enabled)
}

func TestSystemBackupRejectsBackupWithoutLoginCapableRoot(t *testing.T) {
	db := setupSystemBackupTest(t)
	seedSystemBackupSource(t, db)
	backup, err := ExportSystemBackup()
	require.NoError(t, err)
	backup.Users[0].Status = common.UserStatusDisabled
	raw, err := common.Marshal(backup)
	require.NoError(t, err)

	_, err = PreviewSystemBackupImport(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enabled root user")
}

func TestSystemBackupRejectsRootWithInvalidPasswordHash(t *testing.T) {
	db := setupSystemBackupTest(t)
	seedSystemBackupSource(t, db)
	backup, err := ExportSystemBackup()
	require.NoError(t, err)
	backup.Users[0].Password = "not-a-password-hash"
	raw, err := common.Marshal(backup)
	require.NoError(t, err)

	_, err = PreviewSystemBackupImport(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "valid password hash")
}

func TestSystemBackupRestoreRollsBackAllReplacedData(t *testing.T) {
	db := setupSystemBackupTest(t)
	seedSystemBackupSource(t, db)
	backup, err := ExportSystemBackup()
	require.NoError(t, err)
	raw, err := common.Marshal(backup)
	require.NoError(t, err)

	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", "custom.integration_secret").Update("value", "target-must-remain").Error)
	preview, err := PreviewSystemBackupImport(raw)
	require.NoError(t, err)
	require.NoError(t, db.Migrator().DropTable(&model.Ability{}))

	_, err = ApplySystemBackupImport(raw, preview.Hash)
	require.Error(t, err)
	var option model.Option
	require.NoError(t, db.First(&option, "key = ?", "custom.integration_secret").Error)
	assert.Equal(t, "target-must-remain", option.Value)
	var users int64
	require.NoError(t, db.Model(&model.User{}).Count(&users).Error)
	assert.EqualValues(t, 1, users)
}
