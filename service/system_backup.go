package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SystemBackupSchema        = "new-api.system-backup"
	SystemBackupVersion       = 1
	SystemBackupMaxImportSize = 128 << 20
	SystemBackupRestoreMode   = "replace"
)

type SystemBackupFile struct {
	Schema               string                            `json:"schema"`
	Version              int                               `json:"version"`
	ExportedAt           string                            `json:"exported_at"`
	ApplicationVersion   string                            `json:"application_version"`
	Sensitive            bool                              `json:"sensitive"`
	RestoreMode          string                            `json:"restore_mode"`
	Options              []model.Option                    `json:"options"`
	Channels             []model.Channel                   `json:"channels"`
	Vendors              []SystemBackupVendor              `json:"vendors"`
	Models               []SystemBackupModel               `json:"models"`
	PrefillGroups        []SystemBackupPrefillGroup        `json:"prefill_groups"`
	Setups               []model.Setup                     `json:"setups"`
	CustomOAuthProviders []SystemBackupCustomOAuthProvider `json:"custom_oauth_providers"`
	SubscriptionPlans    []model.SubscriptionPlan          `json:"subscription_plans"`
	AuthorizationRoles   []model.AuthzRole                 `json:"authorization_roles"`
	AuthorizationRules   []SystemBackupCasbinRule          `json:"authorization_rules"`
	Users                []SystemBackupUser                `json:"users"`
	Tokens               []SystemBackupToken               `json:"tokens"`
	Redemptions          []SystemBackupRedemption          `json:"redemptions"`
	TwoFA                []SystemBackupTwoFA               `json:"two_fa"`
	TwoFABackupCodes     []SystemBackupTwoFABackupCode     `json:"two_fa_backup_codes"`
	Passkeys             []SystemBackupPasskey             `json:"passkeys"`
	ExternalIdentities   []model.ExternalIdentityClaim     `json:"external_identities"`
	OAuthBindings        []model.UserOAuthBinding          `json:"oauth_bindings"`
	UserSubscriptions    []model.UserSubscription          `json:"user_subscriptions"`
}

type SystemBackupVendor struct {
	model.Vendor
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type SystemBackupModel struct {
	model.Model
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type SystemBackupPrefillGroup struct {
	model.PrefillGroup
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type SystemBackupCustomOAuthProvider struct {
	model.CustomOAuthProvider
	ClientSecret string `json:"client_secret"`
}

type SystemBackupUser struct {
	ID              int        `json:"id"`
	Username        string     `json:"username"`
	Password        string     `json:"password"`
	DisplayName     string     `json:"display_name"`
	Role            int        `json:"role"`
	Status          int        `json:"status"`
	Email           string     `json:"email"`
	GitHubID        string     `json:"github_id"`
	DiscordID       string     `json:"discord_id"`
	OIDCID          string     `json:"oidc_id"`
	WeChatID        string     `json:"wechat_id"`
	TelegramID      string     `json:"telegram_id"`
	AccessToken     *string    `json:"access_token,omitempty"`
	Quota           int        `json:"quota"`
	UsedQuota       int        `json:"used_quota"`
	RequestCount    int        `json:"request_count"`
	Group           string     `json:"group"`
	AffCode         string     `json:"aff_code"`
	AffCount        int        `json:"aff_count"`
	AffQuota        int        `json:"aff_quota"`
	AffHistoryQuota int        `json:"aff_history_quota"`
	InviterID       int        `json:"inviter_id"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	LinuxDOID       string     `json:"linux_do_id"`
	Setting         string     `json:"setting"`
	Remark          string     `json:"remark,omitempty"`
	StripeCustomer  string     `json:"stripe_customer"`
	CreatedAt       int64      `json:"created_at"`
	LastLoginAt     int64      `json:"last_login_at"`
	AuthVersion     int64      `json:"auth_version"`
}

type SystemBackupToken struct {
	ID                 int        `json:"id"`
	UserID             int        `json:"user_id"`
	Key                string     `json:"key"`
	Status             int        `json:"status"`
	Name               string     `json:"name"`
	CreatedTime        int64      `json:"created_time"`
	AccessedTime       int64      `json:"accessed_time"`
	ExpiredTime        int64      `json:"expired_time"`
	RemainQuota        int        `json:"remain_quota"`
	UnlimitedQuota     bool       `json:"unlimited_quota"`
	ModelLimitsEnabled bool       `json:"model_limits_enabled"`
	ModelLimits        string     `json:"model_limits"`
	AllowIPs           *string    `json:"allow_ips"`
	UsedQuota          int        `json:"used_quota"`
	Group              string     `json:"group"`
	CrossGroupRetry    bool       `json:"cross_group_retry"`
	DeletedAt          *time.Time `json:"deleted_at,omitempty"`
}

type SystemBackupRedemption struct {
	ID           int        `json:"id"`
	UserID       int        `json:"user_id"`
	Key          string     `json:"key"`
	Status       int        `json:"status"`
	Name         string     `json:"name"`
	Quota        int        `json:"quota"`
	CreatedTime  int64      `json:"created_time"`
	RedeemedTime int64      `json:"redeemed_time"`
	UsedUserID   int        `json:"used_user_id"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	ExpiredTime  int64      `json:"expired_time"`
}

type SystemBackupTwoFA struct {
	model.TwoFA
	Secret    string     `json:"secret"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type SystemBackupTwoFABackupCode struct {
	model.TwoFABackupCode
	CodeHash  string     `json:"code_hash"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type SystemBackupPasskey struct {
	model.PasskeyCredential
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type SystemBackupCasbinRule struct {
	ID    uint   `json:"id"`
	Ptype string `json:"ptype"`
	V0    string `json:"v0"`
	V1    string `json:"v1"`
	V2    string `json:"v2"`
	V3    string `json:"v3"`
	V4    string `json:"v4"`
	V5    string `json:"v5"`
}

type SystemBackupChangeCounts struct {
	Add       int `json:"add"`
	Update    int `json:"update"`
	Delete    int `json:"delete"`
	Unchanged int `json:"unchanged"`
}

type SystemBackupImportPreview struct {
	Hash           string                              `json:"hash"`
	RecordCount    int                                 `json:"record_count"`
	Sections       map[string]SystemBackupChangeCounts `json:"sections"`
	Warnings       []SystemConfigIssue                 `json:"warnings"`
	Conflicts      []SystemConfigIssue                 `json:"conflicts"`
	RequiresLogout bool                                `json:"requires_logout"`
}

func (preview SystemBackupImportPreview) HasConflicts() bool {
	return len(preview.Conflicts) > 0
}

var systemBackupImportMu sync.Mutex

func ExportSystemBackup() (*SystemBackupFile, error) {
	backup := &SystemBackupFile{
		Schema:             SystemBackupSchema,
		Version:            SystemBackupVersion,
		ExportedAt:         time.Now().UTC().Format(time.RFC3339),
		ApplicationVersion: common.Version,
		Sensitive:          true,
		RestoreMode:        SystemBackupRestoreMode,
	}
	if err := model.DB.Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Find(&backup.Options).Error; err != nil {
		return nil, err
	}
	if err := model.DB.Order("id asc").Find(&backup.Channels).Error; err != nil {
		return nil, err
	}

	var vendors []model.Vendor
	if err := model.DB.Unscoped().Order("id asc").Find(&vendors).Error; err != nil {
		return nil, err
	}
	backup.Vendors = make([]SystemBackupVendor, 0, len(vendors))
	for _, item := range vendors {
		backup.Vendors = append(backup.Vendors, SystemBackupVendor{Vendor: item, DeletedAt: systemBackupDeletedAt(item.DeletedAt)})
	}

	var models []model.Model
	if err := model.DB.Unscoped().Order("id asc").Find(&models).Error; err != nil {
		return nil, err
	}
	backup.Models = make([]SystemBackupModel, 0, len(models))
	for _, item := range models {
		backup.Models = append(backup.Models, SystemBackupModel{Model: item, DeletedAt: systemBackupDeletedAt(item.DeletedAt)})
	}

	var prefillGroups []model.PrefillGroup
	if err := model.DB.Unscoped().Order("id asc").Find(&prefillGroups).Error; err != nil {
		return nil, err
	}
	backup.PrefillGroups = make([]SystemBackupPrefillGroup, 0, len(prefillGroups))
	for _, item := range prefillGroups {
		backup.PrefillGroups = append(backup.PrefillGroups, SystemBackupPrefillGroup{PrefillGroup: item, DeletedAt: systemBackupDeletedAt(item.DeletedAt)})
	}

	if err := model.DB.Order("id asc").Find(&backup.Setups).Error; err != nil {
		return nil, err
	}
	var oauthProviders []model.CustomOAuthProvider
	if err := model.DB.Order("id asc").Find(&oauthProviders).Error; err != nil {
		return nil, err
	}
	backup.CustomOAuthProviders = make([]SystemBackupCustomOAuthProvider, 0, len(oauthProviders))
	for _, item := range oauthProviders {
		backup.CustomOAuthProviders = append(backup.CustomOAuthProviders, SystemBackupCustomOAuthProvider{
			CustomOAuthProvider: item,
			ClientSecret:        item.ClientSecret,
		})
	}
	if err := model.DB.Order("id asc").Find(&backup.SubscriptionPlans).Error; err != nil {
		return nil, err
	}
	if err := model.DB.Order("id asc").Find(&backup.AuthorizationRoles).Error; err != nil {
		return nil, err
	}
	var rules []model.CasbinRule
	if err := model.DB.Order("id asc").Find(&rules).Error; err != nil {
		return nil, err
	}
	backup.AuthorizationRules = make([]SystemBackupCasbinRule, 0, len(rules))
	for _, item := range rules {
		backup.AuthorizationRules = append(backup.AuthorizationRules, systemBackupCasbinRuleFromModel(item))
	}

	var users []model.User
	if err := model.DB.Unscoped().Order("id asc").Find(&users).Error; err != nil {
		return nil, err
	}
	backup.Users = make([]SystemBackupUser, 0, len(users))
	for _, item := range users {
		backup.Users = append(backup.Users, systemBackupUserFromModel(item))
	}

	var tokens []model.Token
	if err := model.DB.Unscoped().Order("id asc").Find(&tokens).Error; err != nil {
		return nil, err
	}
	backup.Tokens = make([]SystemBackupToken, 0, len(tokens))
	for _, item := range tokens {
		backup.Tokens = append(backup.Tokens, systemBackupTokenFromModel(item))
	}

	var redemptions []model.Redemption
	if err := model.DB.Unscoped().Order("id asc").Find(&redemptions).Error; err != nil {
		return nil, err
	}
	backup.Redemptions = make([]SystemBackupRedemption, 0, len(redemptions))
	for _, item := range redemptions {
		backup.Redemptions = append(backup.Redemptions, systemBackupRedemptionFromModel(item))
	}

	var twoFA []model.TwoFA
	if err := model.DB.Unscoped().Order("id asc").Find(&twoFA).Error; err != nil {
		return nil, err
	}
	backup.TwoFA = make([]SystemBackupTwoFA, 0, len(twoFA))
	for _, item := range twoFA {
		backup.TwoFA = append(backup.TwoFA, SystemBackupTwoFA{TwoFA: item, Secret: item.Secret, DeletedAt: systemBackupDeletedAt(item.DeletedAt)})
	}

	var backupCodes []model.TwoFABackupCode
	if err := model.DB.Unscoped().Order("id asc").Find(&backupCodes).Error; err != nil {
		return nil, err
	}
	backup.TwoFABackupCodes = make([]SystemBackupTwoFABackupCode, 0, len(backupCodes))
	for _, item := range backupCodes {
		backup.TwoFABackupCodes = append(backup.TwoFABackupCodes, SystemBackupTwoFABackupCode{
			TwoFABackupCode: item,
			CodeHash:        item.CodeHash,
			DeletedAt:       systemBackupDeletedAt(item.DeletedAt),
		})
	}

	var passkeys []model.PasskeyCredential
	if err := model.DB.Unscoped().Order("id asc").Find(&passkeys).Error; err != nil {
		return nil, err
	}
	backup.Passkeys = make([]SystemBackupPasskey, 0, len(passkeys))
	for _, item := range passkeys {
		backup.Passkeys = append(backup.Passkeys, SystemBackupPasskey{PasskeyCredential: item, DeletedAt: systemBackupDeletedAt(item.DeletedAt)})
	}

	if err := model.DB.Order("id asc").Find(&backup.ExternalIdentities).Error; err != nil {
		return nil, err
	}
	if err := model.DB.Order("id asc").Find(&backup.OAuthBindings).Error; err != nil {
		return nil, err
	}
	if err := model.DB.Order("id asc").Find(&backup.UserSubscriptions).Error; err != nil {
		return nil, err
	}
	return backup, nil
}

func PreviewSystemBackupImport(data []byte) (*SystemBackupImportPreview, error) {
	backup, hash, err := decodeSystemBackup(data)
	if err != nil {
		return nil, err
	}
	return buildSystemBackupImportPreview(backup, hash)
}

func ApplySystemBackupImport(data []byte, expectedHash string) (*SystemBackupImportPreview, error) {
	systemBackupImportMu.Lock()
	defer systemBackupImportMu.Unlock()

	backup, hash, err := decodeSystemBackup(data)
	if err != nil {
		return nil, err
	}
	if expectedHash == "" || !strings.EqualFold(expectedHash, hash) {
		return nil, errors.New("import file changed after preview")
	}
	preview, err := buildSystemBackupImportPreview(backup, hash)
	if err != nil {
		return nil, err
	}
	if preview.HasConflicts() {
		return preview, errors.New("import has unresolved conflicts")
	}

	var oldUsers []model.User
	var oldTokens []model.Token
	var oldSessions []model.UserSession
	var oldPlans []model.SubscriptionPlan
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Select("id").Find(&oldUsers).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Select("key").Find(&oldTokens).Error; err != nil {
			return err
		}
		if err := tx.Select("sid").Find(&oldSessions).Error; err != nil {
			return err
		}
		if err := tx.Select("id").Find(&oldPlans).Error; err != nil {
			return err
		}
		if err := deleteSystemBackupRestoreData(tx); err != nil {
			return err
		}
		if err := insertSystemBackupRestoreData(tx, backup); err != nil {
			return err
		}
		return syncSystemBackupPostgresSequences(tx)
	})
	if err != nil {
		return nil, err
	}

	refreshSystemBackupRuntimeState(backup, oldUsers, oldTokens, oldSessions, oldPlans)
	return preview, nil
}

func decodeSystemBackup(data []byte) (*SystemBackupFile, string, error) {
	if len(data) == 0 {
		return nil, "", errors.New("import file is empty")
	}
	if len(data) > SystemBackupMaxImportSize {
		return nil, "", fmt.Errorf("import file exceeds %d MiB", SystemBackupMaxImportSize>>20)
	}
	var backup SystemBackupFile
	if err := common.Unmarshal(data, &backup); err != nil {
		return nil, "", fmt.Errorf("invalid backup JSON: %w", err)
	}
	if backup.Schema != SystemBackupSchema {
		return nil, "", fmt.Errorf("unsupported backup schema %q", backup.Schema)
	}
	if backup.Version != SystemBackupVersion {
		return nil, "", fmt.Errorf("unsupported backup version %d", backup.Version)
	}
	if !backup.Sensitive || backup.RestoreMode != SystemBackupRestoreMode {
		return nil, "", errors.New("backup metadata is invalid")
	}
	if err := validateSystemBackup(&backup); err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(data)
	return &backup, hex.EncodeToString(digest[:]), nil
}

func validateSystemBackup(backup *SystemBackupFile) error {
	if err := validateSystemBackupUniqueKeys(backup); err != nil {
		return err
	}
	userIDs := make(map[int]struct{}, len(backup.Users))
	hasRoot := false
	for index := range backup.Users {
		item := backup.Users[index].toModel()
		if item.Id <= 0 || strings.TrimSpace(item.Username) == "" || !common.IsValidateRole(item.Role) {
			return fmt.Errorf("users[%d] is invalid", index)
		}
		if item.AuthVersion < 1 {
			return fmt.Errorf("users[%d].auth_version is invalid", index)
		}
		userIDs[item.Id] = struct{}{}
		if item.Role == common.RoleRootUser && item.Status == common.UserStatusEnabled && !item.DeletedAt.Valid {
			if _, err := bcrypt.Cost([]byte(item.Password)); err == nil {
				hasRoot = true
			}
		}
	}
	if !hasRoot {
		return errors.New("backup must contain an enabled root user with a valid password hash")
	}
	for index, item := range backup.Users {
		if item.InviterID != 0 {
			if _, ok := userIDs[item.InviterID]; !ok {
				return fmt.Errorf("users[%d] references a missing inviter", index)
			}
		}
	}

	vendorIDs := make(map[int]struct{}, len(backup.Vendors))
	for _, item := range backup.Vendors {
		vendorIDs[item.Id] = struct{}{}
	}
	for index, item := range backup.Models {
		if item.VendorID != 0 {
			if _, ok := vendorIDs[item.VendorID]; !ok {
				return fmt.Errorf("models[%d] references a missing vendor", index)
			}
		}
	}
	for index := range backup.Channels {
		channel := &backup.Channels[index]
		if channel.Id <= 0 || strings.TrimSpace(channel.Name) == "" {
			return fmt.Errorf("channels[%d] is invalid", index)
		}
		if err := model.ValidateAndNormalizeChannelInfo(&channel.ChannelInfo); err != nil {
			return fmt.Errorf("channels[%d] metadata is invalid: %w", index, err)
		}
		if err := channel.ValidateSettings(); err != nil {
			return fmt.Errorf("channels[%d] settings are invalid: %w", index, err)
		}
	}
	for _, option := range backup.Options {
		if strings.TrimSpace(option.Key) == "" {
			return errors.New("backup contains an option without a key")
		}
		if err := validateSystemConfigOption(option.Key, option.Value); err != nil {
			return err
		}
	}

	providerIDs := make(map[int]struct{}, len(backup.CustomOAuthProviders))
	for _, item := range backup.CustomOAuthProviders {
		providerIDs[item.Id] = struct{}{}
	}
	planIDs := make(map[int]struct{}, len(backup.SubscriptionPlans))
	for _, item := range backup.SubscriptionPlans {
		planIDs[item.Id] = struct{}{}
	}
	for index, item := range backup.Tokens {
		if _, ok := userIDs[item.UserID]; !ok {
			return fmt.Errorf("tokens[%d] references a missing user", index)
		}
	}
	for index, item := range backup.Redemptions {
		for _, userID := range []int{item.UserID, item.UsedUserID} {
			if userID == 0 {
				continue
			}
			if _, ok := userIDs[userID]; !ok {
				return fmt.Errorf("redemptions[%d] references a missing user", index)
			}
		}
	}
	for index, item := range backup.TwoFA {
		if _, ok := userIDs[item.UserId]; !ok {
			return fmt.Errorf("two_fa[%d] references a missing user", index)
		}
	}
	for index, item := range backup.TwoFABackupCodes {
		if _, ok := userIDs[item.UserId]; !ok {
			return fmt.Errorf("two_fa_backup_codes[%d] references a missing user", index)
		}
	}
	for index, item := range backup.Passkeys {
		if _, ok := userIDs[item.UserID]; !ok {
			return fmt.Errorf("passkeys[%d] references a missing user", index)
		}
	}
	for index, item := range backup.ExternalIdentities {
		if _, ok := userIDs[item.UserId]; !ok {
			return fmt.Errorf("external_identities[%d] references a missing user", index)
		}
	}
	for index, item := range backup.OAuthBindings {
		if _, ok := userIDs[item.UserId]; !ok {
			return fmt.Errorf("oauth_bindings[%d] references a missing user", index)
		}
		if _, ok := providerIDs[item.ProviderId]; !ok {
			return fmt.Errorf("oauth_bindings[%d] references a missing provider", index)
		}
	}
	for index, item := range backup.UserSubscriptions {
		if _, ok := userIDs[item.UserId]; !ok {
			return fmt.Errorf("user_subscriptions[%d] references a missing user", index)
		}
		if _, ok := planIDs[item.PlanId]; !ok {
			return fmt.Errorf("user_subscriptions[%d] references a missing plan", index)
		}
	}
	return nil
}

func validateSystemBackupUniqueKeys(backup *SystemBackupFile) error {
	seen := make(map[string]struct{})
	check := func(section string, identity any) error {
		value := reflect.ValueOf(identity)
		switch value.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if value.Int() <= 0 {
				return fmt.Errorf("backup contains invalid %s identity %v", section, identity)
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if value.Uint() == 0 {
				return fmt.Errorf("backup contains invalid %s identity %v", section, identity)
			}
		case reflect.String:
			if strings.TrimSpace(value.String()) == "" {
				return fmt.Errorf("backup contains an empty %s identity", section)
			}
		}
		key := fmt.Sprintf("%s\x00%v", section, identity)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("backup contains duplicate %s identity %v", section, identity)
		}
		seen[key] = struct{}{}
		return nil
	}
	for _, item := range backup.Options {
		if err := check("option", item.Key); err != nil {
			return err
		}
	}
	for _, item := range backup.Channels {
		if err := check("channel", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.Vendors {
		if err := check("vendor", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.Models {
		if err := check("model", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.PrefillGroups {
		if err := check("prefill_group", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.Setups {
		if err := check("setup", item.ID); err != nil {
			return err
		}
	}
	for _, item := range backup.CustomOAuthProviders {
		if err := check("oauth_provider", item.Id); err != nil {
			return err
		}
		if err := check("oauth_provider_slug", item.Slug); err != nil {
			return err
		}
	}
	for _, item := range backup.SubscriptionPlans {
		if err := check("subscription_plan", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.AuthorizationRoles {
		if err := check("authorization_role", item.Id); err != nil {
			return err
		}
		if err := check("authorization_role_key", item.Key); err != nil {
			return err
		}
	}
	for _, item := range backup.AuthorizationRules {
		if err := check("authorization_rule", item.ID); err != nil {
			return err
		}
	}
	for _, item := range backup.Users {
		if err := check("user", item.ID); err != nil {
			return err
		}
		if err := check("username", item.Username); err != nil {
			return err
		}
		if item.AccessToken != nil && *item.AccessToken != "" {
			if err := check("user_access_token", *item.AccessToken); err != nil {
				return err
			}
		}
	}
	for _, item := range backup.Tokens {
		if err := check("token", item.ID); err != nil {
			return err
		}
		if err := check("token_key", item.Key); err != nil {
			return err
		}
	}
	for _, item := range backup.Redemptions {
		if err := check("redemption", item.ID); err != nil {
			return err
		}
		if err := check("redemption_key", item.Key); err != nil {
			return err
		}
	}
	for _, item := range backup.TwoFA {
		if err := check("two_fa", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.TwoFABackupCodes {
		if err := check("two_fa_backup_code", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.Passkeys {
		if err := check("passkey", item.ID); err != nil {
			return err
		}
		if err := check("passkey_credential", item.CredentialID); err != nil {
			return err
		}
	}
	for _, item := range backup.ExternalIdentities {
		if err := check("external_identity", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.OAuthBindings {
		if err := check("oauth_binding", item.Id); err != nil {
			return err
		}
	}
	for _, item := range backup.UserSubscriptions {
		if err := check("user_subscription", item.Id); err != nil {
			return err
		}
	}
	return nil
}

func buildSystemBackupImportPreview(backup *SystemBackupFile, hash string) (*SystemBackupImportPreview, error) {
	current, err := ExportSystemBackup()
	if err != nil {
		return nil, err
	}
	preview := &SystemBackupImportPreview{
		Hash:           hash,
		RecordCount:    backup.recordCount(),
		Sections:       make(map[string]SystemBackupChangeCounts),
		Warnings:       []SystemConfigIssue{{Code: "runtime_history_preserved"}},
		Conflicts:      make([]SystemConfigIssue, 0),
		RequiresLogout: true,
	}
	preview.Sections["options"] = systemBackupRecordCounts(backup.Options, current.Options, func(item model.Option) string { return item.Key })
	preview.Sections["channels"] = systemBackupRecordCounts(backup.Channels, current.Channels, func(item model.Channel) int { return item.Id })
	preview.Sections["catalog"] = addSystemBackupCounts(
		systemBackupRecordCounts(backup.Vendors, current.Vendors, func(item SystemBackupVendor) int { return item.Id }),
		systemBackupRecordCounts(backup.Models, current.Models, func(item SystemBackupModel) int { return item.Id }),
		systemBackupRecordCounts(backup.PrefillGroups, current.PrefillGroups, func(item SystemBackupPrefillGroup) int { return item.Id }),
		systemBackupRecordCounts(backup.Setups, current.Setups, func(item model.Setup) uint { return item.ID }),
	)
	preview.Sections["oauth"] = systemBackupRecordCounts(backup.CustomOAuthProviders, current.CustomOAuthProviders, func(item SystemBackupCustomOAuthProvider) int { return item.Id })
	preview.Sections["authorization"] = addSystemBackupCounts(
		systemBackupRecordCounts(backup.AuthorizationRoles, current.AuthorizationRoles, func(item model.AuthzRole) uint { return item.Id }),
		systemBackupRecordCounts(backup.AuthorizationRules, current.AuthorizationRules, func(item SystemBackupCasbinRule) uint { return item.ID }),
	)
	preview.Sections["users"] = systemBackupRecordCounts(backup.Users, current.Users, func(item SystemBackupUser) int { return item.ID })
	preview.Sections["tokens"] = systemBackupRecordCounts(backup.Tokens, current.Tokens, func(item SystemBackupToken) int { return item.ID })
	preview.Sections["redemptions"] = systemBackupRecordCounts(backup.Redemptions, current.Redemptions, func(item SystemBackupRedemption) int { return item.ID })
	preview.Sections["authentication"] = addSystemBackupCounts(
		systemBackupRecordCounts(backup.TwoFA, current.TwoFA, func(item SystemBackupTwoFA) int { return item.Id }),
		systemBackupRecordCounts(backup.TwoFABackupCodes, current.TwoFABackupCodes, func(item SystemBackupTwoFABackupCode) int { return item.Id }),
		systemBackupRecordCounts(backup.Passkeys, current.Passkeys, func(item SystemBackupPasskey) int { return item.ID }),
		systemBackupRecordCounts(backup.ExternalIdentities, current.ExternalIdentities, func(item model.ExternalIdentityClaim) int64 { return item.Id }),
		systemBackupRecordCounts(backup.OAuthBindings, current.OAuthBindings, func(item model.UserOAuthBinding) int { return item.Id }),
	)
	preview.Sections["subscriptions"] = addSystemBackupCounts(
		systemBackupRecordCounts(backup.SubscriptionPlans, current.SubscriptionPlans, func(item model.SubscriptionPlan) int { return item.Id }),
		systemBackupRecordCounts(backup.UserSubscriptions, current.UserSubscriptions, func(item model.UserSubscription) int { return item.Id }),
	)
	return preview, nil
}

func deleteSystemBackupRestoreData(tx *gorm.DB) error {
	restoreTx := tx.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true})
	targets := []any{
		&model.SubscriptionPreConsumeRecord{}, &model.UserSession{}, &model.AuthFlow{},
		&model.UserOAuthBinding{}, &model.ExternalIdentityClaim{}, &model.PasskeyCredential{},
		&model.TwoFABackupCode{}, &model.TwoFA{}, &model.Token{}, &model.Redemption{},
		&model.UserSubscription{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.User{},
		&model.SubscriptionPlan{}, &model.CustomOAuthProvider{}, &model.Ability{},
		&model.Channel{}, &model.Model{}, &model.Vendor{}, &model.PrefillGroup{},
		&model.Setup{}, &model.Option{},
	}
	for _, target := range targets {
		if err := restoreTx.Unscoped().Delete(target).Error; err != nil {
			return err
		}
	}
	return nil
}

func insertSystemBackupRestoreData(tx *gorm.DB, backup *SystemBackupFile) error {
	restoreTx := tx.Session(&gorm.Session{SkipHooks: true})
	if err := createSystemBackupRecords(restoreTx, backup.Options); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, backup.Setups); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupVendorsToModels(backup.Vendors)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupModelsToModels(backup.Models)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupPrefillGroupsToModels(backup.PrefillGroups)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, backup.Channels); err != nil {
		return err
	}
	for index := range backup.Channels {
		if err := backup.Channels[index].UpdateAbilities(tx); err != nil {
			return err
		}
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupOAuthProvidersToModels(backup.CustomOAuthProviders)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, backup.SubscriptionPlans); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupUsersToModels(backup.Users)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupTokensToModels(backup.Tokens)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupRedemptionsToModels(backup.Redemptions)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupTwoFAToModels(backup.TwoFA)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupTwoFABackupCodesToModels(backup.TwoFABackupCodes)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, systemBackupPasskeysToModels(backup.Passkeys)); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, backup.ExternalIdentities); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, backup.OAuthBindings); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, backup.UserSubscriptions); err != nil {
		return err
	}
	if err := createSystemBackupRecords(restoreTx, backup.AuthorizationRoles); err != nil {
		return err
	}
	return createSystemBackupRecords(restoreTx, systemBackupCasbinRulesToModels(backup.AuthorizationRules))
}

func syncSystemBackupPostgresSequences(tx *gorm.DB) error {
	if !common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		return nil
	}
	tables := []string{
		"channels", "tokens", "users", "redemptions", "abilities", "models", "vendors",
		"prefill_groups", "setups", "two_fas", "two_fa_backup_codes", "subscription_plans",
		"user_subscriptions", "custom_oauth_providers", "user_oauth_bindings", "passkey_credentials",
		"external_identity_claims", "authz_roles", "casbin_rule",
	}
	for _, table := range tables {
		query := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE(MAX("id"), 1), COUNT(*) > 0) FROM "%s"`,
			table,
			table,
		)
		if err := tx.Exec(query).Error; err != nil {
			return fmt.Errorf("sync %s sequence: %w", table, err)
		}
	}
	return nil
}

func refreshSystemBackupRuntimeState(backup *SystemBackupFile, oldUsers []model.User, oldTokens []model.Token, oldSessions []model.UserSession, oldPlans []model.SubscriptionPlan) {
	userIDs := make([]int, 0, len(oldUsers)+len(backup.Users))
	for _, item := range oldUsers {
		userIDs = append(userIDs, item.Id)
	}
	for _, item := range backup.Users {
		userIDs = append(userIDs, item.ID)
	}
	sessionIDs := make([]string, 0, len(oldSessions))
	for _, item := range oldSessions {
		sessionIDs = append(sessionIDs, item.SID)
	}
	if err := model.InvalidateAuthenticationCachesAfterRestore(userIDs, sessionIDs); err != nil {
		common.SysError("failed to invalidate authentication caches after restore: " + err.Error())
	}
	tokenKeys := make([]string, 0, len(oldTokens)+len(backup.Tokens))
	for _, item := range oldTokens {
		tokenKeys = append(tokenKeys, item.Key)
	}
	for _, item := range backup.Tokens {
		tokenKeys = append(tokenKeys, item.Key)
	}
	if err := model.InvalidateTokenCaches(tokenKeys); err != nil {
		common.SysError("failed to invalidate token caches after restore: " + err.Error())
	}
	for _, item := range oldPlans {
		model.InvalidateSubscriptionPlanCache(item.Id)
	}
	for _, item := range backup.SubscriptionPlans {
		model.InvalidateSubscriptionPlanCache(item.Id)
	}
	model.InitOptionMap()
	model.InitChannelCache()
	model.RefreshPricing()
	if err := authz.ReloadPolicy(); err != nil {
		common.SysError("failed to reload authorization policy after restore: " + err.Error())
	}
}

func createSystemBackupRecords[T any](tx *gorm.DB, records []T) error {
	if len(records) == 0 {
		return nil
	}
	return tx.Select("*").CreateInBatches(&records, 100).Error
}

func systemBackupRecordCounts[T any, K comparable](imported []T, current []T, key func(T) K) SystemBackupChangeCounts {
	counts := SystemBackupChangeCounts{}
	currentByKey := make(map[K]T, len(current))
	for _, item := range current {
		currentByKey[key(item)] = item
	}
	for _, item := range imported {
		identity := key(item)
		currentItem, ok := currentByKey[identity]
		if !ok {
			counts.Add++
			continue
		}
		delete(currentByKey, identity)
		if reflect.DeepEqual(item, currentItem) {
			counts.Unchanged++
		} else {
			counts.Update++
		}
	}
	counts.Delete = len(currentByKey)
	return counts
}

func addSystemBackupCounts(values ...SystemBackupChangeCounts) SystemBackupChangeCounts {
	var total SystemBackupChangeCounts
	for _, item := range values {
		total.Add += item.Add
		total.Update += item.Update
		total.Delete += item.Delete
		total.Unchanged += item.Unchanged
	}
	return total
}

func (backup SystemBackupFile) recordCount() int {
	return len(backup.Options) + len(backup.Channels) + len(backup.Vendors) + len(backup.Models) +
		len(backup.PrefillGroups) + len(backup.Setups) + len(backup.CustomOAuthProviders) +
		len(backup.SubscriptionPlans) + len(backup.AuthorizationRoles) + len(backup.AuthorizationRules) +
		len(backup.Users) + len(backup.Tokens) + len(backup.Redemptions) + len(backup.TwoFA) +
		len(backup.TwoFABackupCodes) + len(backup.Passkeys) + len(backup.ExternalIdentities) +
		len(backup.OAuthBindings) + len(backup.UserSubscriptions)
}

func systemBackupDeletedAt(value gorm.DeletedAt) *time.Time {
	if !value.Valid {
		return nil
	}
	deletedAt := value.Time
	return &deletedAt
}

func systemBackupRestoreDeletedAt(value *time.Time) gorm.DeletedAt {
	if value == nil {
		return gorm.DeletedAt{}
	}
	return gorm.DeletedAt{Time: *value, Valid: true}
}

func (item SystemBackupUser) toModel() model.User {
	return model.User{
		Id: item.ID, Username: item.Username, Password: item.Password, DisplayName: item.DisplayName,
		Role: item.Role, Status: item.Status, Email: item.Email, GitHubId: item.GitHubID,
		DiscordId: item.DiscordID, OidcId: item.OIDCID, WeChatId: item.WeChatID,
		TelegramId: item.TelegramID, AccessToken: item.AccessToken, Quota: item.Quota,
		UsedQuota: item.UsedQuota, RequestCount: item.RequestCount, Group: item.Group,
		AffCode: item.AffCode, AffCount: item.AffCount, AffQuota: item.AffQuota,
		AffHistoryQuota: item.AffHistoryQuota, InviterId: item.InviterID,
		DeletedAt: systemBackupRestoreDeletedAt(item.DeletedAt), LinuxDOId: item.LinuxDOID,
		Setting: item.Setting, Remark: item.Remark, StripeCustomer: item.StripeCustomer,
		CreatedAt: item.CreatedAt, LastLoginAt: item.LastLoginAt, AuthVersion: item.AuthVersion,
	}
}

func systemBackupUserFromModel(item model.User) SystemBackupUser {
	return SystemBackupUser{
		ID: item.Id, Username: item.Username, Password: item.Password, DisplayName: item.DisplayName,
		Role: item.Role, Status: item.Status, Email: item.Email, GitHubID: item.GitHubId,
		DiscordID: item.DiscordId, OIDCID: item.OidcId, WeChatID: item.WeChatId,
		TelegramID: item.TelegramId, AccessToken: item.AccessToken, Quota: item.Quota,
		UsedQuota: item.UsedQuota, RequestCount: item.RequestCount, Group: item.Group,
		AffCode: item.AffCode, AffCount: item.AffCount, AffQuota: item.AffQuota,
		AffHistoryQuota: item.AffHistoryQuota, InviterID: item.InviterId,
		DeletedAt: systemBackupDeletedAt(item.DeletedAt), LinuxDOID: item.LinuxDOId,
		Setting: item.Setting, Remark: item.Remark, StripeCustomer: item.StripeCustomer,
		CreatedAt: item.CreatedAt, LastLoginAt: item.LastLoginAt, AuthVersion: item.AuthVersion,
	}
}

func systemBackupVendorsToModels(items []SystemBackupVendor) []model.Vendor {
	result := make([]model.Vendor, 0, len(items))
	for _, item := range items {
		value := item.Vendor
		value.DeletedAt = systemBackupRestoreDeletedAt(item.DeletedAt)
		result = append(result, value)
	}
	return result
}

func systemBackupModelsToModels(items []SystemBackupModel) []model.Model {
	result := make([]model.Model, 0, len(items))
	for _, item := range items {
		value := item.Model
		value.DeletedAt = systemBackupRestoreDeletedAt(item.DeletedAt)
		value.BoundChannels = nil
		value.EnableGroups = nil
		value.QuotaTypes = nil
		value.MatchedModels = nil
		value.MatchedCount = 0
		result = append(result, value)
	}
	return result
}

func systemBackupPrefillGroupsToModels(items []SystemBackupPrefillGroup) []model.PrefillGroup {
	result := make([]model.PrefillGroup, 0, len(items))
	for _, item := range items {
		value := item.PrefillGroup
		value.DeletedAt = systemBackupRestoreDeletedAt(item.DeletedAt)
		result = append(result, value)
	}
	return result
}

func systemBackupOAuthProvidersToModels(items []SystemBackupCustomOAuthProvider) []model.CustomOAuthProvider {
	result := make([]model.CustomOAuthProvider, 0, len(items))
	for _, item := range items {
		value := item.CustomOAuthProvider
		value.ClientSecret = item.ClientSecret
		result = append(result, value)
	}
	return result
}

func systemBackupUsersToModels(items []SystemBackupUser) []model.User {
	result := make([]model.User, 0, len(items))
	for _, item := range items {
		result = append(result, item.toModel())
	}
	return result
}

func systemBackupTokensToModels(items []SystemBackupToken) []model.Token {
	result := make([]model.Token, 0, len(items))
	for _, item := range items {
		result = append(result, model.Token{
			Id: item.ID, UserId: item.UserID, Key: item.Key, Status: item.Status, Name: item.Name,
			CreatedTime: item.CreatedTime, AccessedTime: item.AccessedTime, ExpiredTime: item.ExpiredTime,
			RemainQuota: item.RemainQuota, UnlimitedQuota: item.UnlimitedQuota,
			ModelLimitsEnabled: item.ModelLimitsEnabled, ModelLimits: item.ModelLimits, AllowIps: item.AllowIPs,
			UsedQuota: item.UsedQuota, Group: item.Group, CrossGroupRetry: item.CrossGroupRetry,
			DeletedAt: systemBackupRestoreDeletedAt(item.DeletedAt),
		})
	}
	return result
}

func systemBackupTokenFromModel(item model.Token) SystemBackupToken {
	return SystemBackupToken{
		ID: item.Id, UserID: item.UserId, Key: item.Key, Status: item.Status, Name: item.Name,
		CreatedTime: item.CreatedTime, AccessedTime: item.AccessedTime, ExpiredTime: item.ExpiredTime,
		RemainQuota: item.RemainQuota, UnlimitedQuota: item.UnlimitedQuota,
		ModelLimitsEnabled: item.ModelLimitsEnabled, ModelLimits: item.ModelLimits, AllowIPs: item.AllowIps,
		UsedQuota: item.UsedQuota, Group: item.Group, CrossGroupRetry: item.CrossGroupRetry,
		DeletedAt: systemBackupDeletedAt(item.DeletedAt),
	}
}

func systemBackupRedemptionsToModels(items []SystemBackupRedemption) []model.Redemption {
	result := make([]model.Redemption, 0, len(items))
	for _, item := range items {
		result = append(result, model.Redemption{
			Id: item.ID, UserId: item.UserID, Key: item.Key, Status: item.Status, Name: item.Name,
			Quota: item.Quota, CreatedTime: item.CreatedTime, RedeemedTime: item.RedeemedTime,
			UsedUserId: item.UsedUserID, DeletedAt: systemBackupRestoreDeletedAt(item.DeletedAt),
			ExpiredTime: item.ExpiredTime,
		})
	}
	return result
}

func systemBackupRedemptionFromModel(item model.Redemption) SystemBackupRedemption {
	return SystemBackupRedemption{
		ID: item.Id, UserID: item.UserId, Key: item.Key, Status: item.Status, Name: item.Name,
		Quota: item.Quota, CreatedTime: item.CreatedTime, RedeemedTime: item.RedeemedTime,
		UsedUserID: item.UsedUserId, DeletedAt: systemBackupDeletedAt(item.DeletedAt),
		ExpiredTime: item.ExpiredTime,
	}
}

func systemBackupTwoFAToModels(items []SystemBackupTwoFA) []model.TwoFA {
	result := make([]model.TwoFA, 0, len(items))
	for _, item := range items {
		value := item.TwoFA
		value.Secret = item.Secret
		value.DeletedAt = systemBackupRestoreDeletedAt(item.DeletedAt)
		result = append(result, value)
	}
	return result
}

func systemBackupTwoFABackupCodesToModels(items []SystemBackupTwoFABackupCode) []model.TwoFABackupCode {
	result := make([]model.TwoFABackupCode, 0, len(items))
	for _, item := range items {
		value := item.TwoFABackupCode
		value.CodeHash = item.CodeHash
		value.DeletedAt = systemBackupRestoreDeletedAt(item.DeletedAt)
		result = append(result, value)
	}
	return result
}

func systemBackupPasskeysToModels(items []SystemBackupPasskey) []model.PasskeyCredential {
	result := make([]model.PasskeyCredential, 0, len(items))
	for _, item := range items {
		value := item.PasskeyCredential
		value.DeletedAt = systemBackupRestoreDeletedAt(item.DeletedAt)
		result = append(result, value)
	}
	return result
}

func systemBackupCasbinRuleFromModel(item model.CasbinRule) SystemBackupCasbinRule {
	return SystemBackupCasbinRule{ID: item.Id, Ptype: item.Ptype, V0: item.V0, V1: item.V1, V2: item.V2, V3: item.V3, V4: item.V4, V5: item.V5}
}

func systemBackupCasbinRulesToModels(items []SystemBackupCasbinRule) []model.CasbinRule {
	result := make([]model.CasbinRule, 0, len(items))
	for _, item := range items {
		result = append(result, model.CasbinRule{Id: item.ID, Ptype: item.Ptype, V0: item.V0, V1: item.V1, V2: item.V2, V3: item.V3, V4: item.V4, V5: item.V5})
	}
	return result
}
