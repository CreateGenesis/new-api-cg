package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"gorm.io/gorm"
)

const (
	SystemConfigSchema        = "new-api.system-config"
	SystemConfigVersion       = 1
	SystemConfigMaxImportSize = 32 << 20
)

type SystemConfigFile struct {
	Schema             string                `json:"schema"`
	Version            int                   `json:"version"`
	ExportedAt         string                `json:"exported_at"`
	ApplicationVersion string                `json:"application_version"`
	Options            map[string]string     `json:"options"`
	Vendors            []SystemConfigVendor  `json:"vendors"`
	Models             []SystemConfigModel   `json:"models"`
	Channels           []SystemConfigChannel `json:"channels"`
	Omitted            SystemConfigOmitted   `json:"omitted"`
}

type SystemConfigOmitted struct {
	OptionGroups  []string `json:"option_groups"`
	ChannelFields []string `json:"channel_fields"`
	Data          []string `json:"data"`
}

type SystemConfigVendor struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Status      int    `json:"status"`
}

type SystemConfigModel struct {
	ModelName    string `json:"model_name"`
	Description  string `json:"description,omitempty"`
	Icon         string `json:"icon,omitempty"`
	Tags         string `json:"tags,omitempty"`
	VendorName   string `json:"vendor_name,omitempty"`
	Endpoints    string `json:"endpoints,omitempty"`
	Status       int    `json:"status"`
	SyncOfficial int    `json:"sync_official"`
	NameRule     int    `json:"name_rule"`
}

type SystemConfigChannelInfo struct {
	IsMultiKey                         bool                     `json:"is_multi_key"`
	MultiKeyAffinityTTLSeconds         int                      `json:"multi_key_affinity_ttl_seconds,omitempty"`
	MultiKeyLeastRequestsWindowSeconds int                      `json:"multi_key_least_requests_window_seconds,omitempty"`
	MultiKeyMode                       string                   `json:"multi_key_mode,omitempty"`
	ChannelOverloadProtection          model.OverloadProtection `json:"channel_overload_protection"`
	MultiKeyOverloadProtection         model.OverloadProtection `json:"multi_key_overload_protection"`
}

type SystemConfigChannel struct {
	Type               int                     `json:"type"`
	OpenAIOrganization *string                 `json:"openai_organization,omitempty"`
	TestModel          *string                 `json:"test_model,omitempty"`
	Status             int                     `json:"status"`
	Name               string                  `json:"name"`
	Weight             *uint                   `json:"weight,omitempty"`
	BaseURL            *string                 `json:"base_url,omitempty"`
	Other              string                  `json:"other,omitempty"`
	Models             string                  `json:"models,omitempty"`
	Group              string                  `json:"group,omitempty"`
	ModelMapping       *string                 `json:"model_mapping,omitempty"`
	StatusCodeMapping  *string                 `json:"status_code_mapping,omitempty"`
	Priority           *int64                  `json:"priority,omitempty"`
	AutoBan            *int                    `json:"auto_ban,omitempty"`
	Tag                *string                 `json:"tag,omitempty"`
	Setting            *string                 `json:"setting,omitempty"`
	ParamOverride      *string                 `json:"param_override,omitempty"`
	HeaderOverride     *string                 `json:"header_override,omitempty"`
	Remark             *string                 `json:"remark,omitempty"`
	ChannelInfo        SystemConfigChannelInfo `json:"channel_info"`
	OtherSettings      string                  `json:"settings,omitempty"`
}

type SystemConfigChangeCounts struct {
	Add       int `json:"add"`
	Update    int `json:"update"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
}

type SystemConfigIssue struct {
	Code string `json:"code"`
	Item string `json:"item,omitempty"`
}

type SystemConfigImportPreview struct {
	Hash         string                   `json:"hash"`
	Options      SystemConfigChangeCounts `json:"options"`
	Vendors      SystemConfigChangeCounts `json:"vendors"`
	Models       SystemConfigChangeCounts `json:"models"`
	Channels     SystemConfigChangeCounts `json:"channels"`
	Warnings     []SystemConfigIssue      `json:"warnings"`
	Conflicts    []SystemConfigIssue      `json:"conflicts"`
	Omitted      SystemConfigOmitted      `json:"omitted"`
	ReloadNeeded bool                     `json:"reload_needed"`
}

func (preview SystemConfigImportPreview) HasConflicts() bool {
	return len(preview.Conflicts) > 0
}

var excludedSystemConfigOptionPrefixes = []string{
	"GitHub",
	"LinuxDO",
	"Telegram",
	"WeChat",
	"Turnstile",
	"discord.",
	"oidc.",
	"SMTP",
	"Worker",
	"Epay",
	"Stripe",
	"Creem",
	"Waffo",
	"payment_setting.",
	"model_deployment.",
}

var excludedSystemConfigOptionKeys = map[string]struct{}{
	"PayAddress":            {},
	"CustomCallbackAddress": {},
	"PayMethods":            {},
}

func isExcludedSystemConfigOption(key string) bool {
	if _, ok := excludedSystemConfigOptionKeys[key]; ok {
		return true
	}
	lowerKey := strings.ToLower(key)
	for _, suffix := range []string{"token", "secret", "api_key", "private_key", "valid_key", "password", "credential"} {
		if strings.HasSuffix(lowerKey, suffix) {
			return true
		}
	}
	for _, prefix := range excludedSystemConfigOptionPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func ExportSystemConfig() (*SystemConfigFile, error) {
	options := make(map[string]string)
	common.OptionMapRWMutex.RLock()
	for key, rawValue := range common.OptionMap {
		if !isExcludedSystemConfigOption(key) {
			options[key] = common.Interface2String(rawValue)
		}
	}
	common.OptionMapRWMutex.RUnlock()

	var vendors []model.Vendor
	if err := model.DB.Order("name asc").Find(&vendors).Error; err != nil {
		return nil, err
	}
	vendorNames := make(map[int]string, len(vendors))
	exportedVendors := make([]SystemConfigVendor, 0, len(vendors))
	for _, vendor := range vendors {
		vendorNames[vendor.Id] = vendor.Name
		exportedVendors = append(exportedVendors, systemConfigVendorFromModel(vendor))
	}

	var models []model.Model
	if err := model.DB.Order("model_name asc").Find(&models).Error; err != nil {
		return nil, err
	}
	exportedModels := make([]SystemConfigModel, 0, len(models))
	for _, item := range models {
		exportedModels = append(exportedModels, systemConfigModelFromModel(item, vendorNames[item.VendorID]))
	}

	var channels []model.Channel
	if err := model.DB.Order("name asc, type asc, id asc").Find(&channels).Error; err != nil {
		return nil, err
	}
	exportedChannels := make([]SystemConfigChannel, 0, len(channels))
	for _, channel := range channels {
		exportedChannels = append(exportedChannels, systemConfigChannelFromModel(channel))
	}

	return &SystemConfigFile{
		Schema:             SystemConfigSchema,
		Version:            SystemConfigVersion,
		ExportedAt:         time.Now().UTC().Format(time.RFC3339),
		ApplicationVersion: common.Version,
		Options:            options,
		Vendors:            exportedVendors,
		Models:             exportedModels,
		Channels:           exportedChannels,
		Omitted:            defaultSystemConfigOmitted(),
	}, nil
}

func PreviewSystemConfigImport(data []byte) (*SystemConfigImportPreview, error) {
	configFile, hash, err := decodeSystemConfig(data)
	if err != nil {
		return nil, err
	}
	preview, err := buildSystemConfigImportPreview(configFile, hash)
	if err != nil {
		return nil, err
	}
	return preview, nil
}

func ApplySystemConfigImport(data []byte, expectedHash string) (*SystemConfigImportPreview, error) {
	configFile, hash, err := decodeSystemConfig(data)
	if err != nil {
		return nil, err
	}
	if expectedHash == "" || !strings.EqualFold(expectedHash, hash) {
		return nil, errors.New("import file changed after preview")
	}
	preview, err := buildSystemConfigImportPreview(configFile, hash)
	if err != nil {
		return nil, err
	}
	if preview.HasConflicts() {
		return preview, errors.New("import has unresolved conflicts")
	}

	knownOptions := currentSystemConfigOptions()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := importSystemConfigOptions(tx, configFile.Options, knownOptions); err != nil {
			return err
		}
		vendorIDs, err := importSystemConfigVendors(tx, configFile.Vendors)
		if err != nil {
			return err
		}
		if err := importSystemConfigModels(tx, configFile.Models, vendorIDs); err != nil {
			return err
		}
		return importSystemConfigChannels(tx, configFile.Channels)
	})
	if err != nil {
		return nil, err
	}

	model.InitOptionMap()
	model.InitChannelCache()
	model.RefreshPricing()
	return preview, nil
}

func decodeSystemConfig(data []byte) (*SystemConfigFile, string, error) {
	if len(data) == 0 {
		return nil, "", errors.New("import file is empty")
	}
	if len(data) > SystemConfigMaxImportSize {
		return nil, "", fmt.Errorf("import file exceeds %d MiB", SystemConfigMaxImportSize>>20)
	}

	var raw struct {
		Channels []map[string]json.RawMessage `json:"channels"`
	}
	if err := common.Unmarshal(data, &raw); err != nil {
		return nil, "", fmt.Errorf("invalid import JSON: %w", err)
	}
	for index, channel := range raw.Channels {
		if _, ok := channel["key"]; ok {
			return nil, "", fmt.Errorf("channels[%d].key is forbidden", index)
		}
	}

	var configFile SystemConfigFile
	if err := common.Unmarshal(data, &configFile); err != nil {
		return nil, "", fmt.Errorf("invalid import file: %w", err)
	}
	if configFile.Schema != SystemConfigSchema {
		return nil, "", fmt.Errorf("unsupported config schema %q", configFile.Schema)
	}
	if configFile.Version != SystemConfigVersion {
		return nil, "", fmt.Errorf("unsupported config version %d", configFile.Version)
	}
	for key := range configFile.Options {
		if isExcludedSystemConfigOption(key) {
			return nil, "", fmt.Errorf("option %q is forbidden in import files", key)
		}
	}
	if err := validateSystemConfigFile(&configFile); err != nil {
		return nil, "", err
	}

	digest := sha256.Sum256(data)
	return &configFile, hex.EncodeToString(digest[:]), nil
}

func validateSystemConfigFile(configFile *SystemConfigFile) error {
	for index, vendor := range configFile.Vendors {
		name := strings.TrimSpace(vendor.Name)
		if name == "" {
			return fmt.Errorf("vendors[%d].name is required", index)
		}
		configFile.Vendors[index].Name = name
	}

	for index, item := range configFile.Models {
		name := strings.TrimSpace(item.ModelName)
		if name == "" {
			return fmt.Errorf("models[%d].model_name is required", index)
		}
		configFile.Models[index].ModelName = name
		configFile.Models[index].VendorName = strings.TrimSpace(item.VendorName)
	}

	for index, item := range configFile.Channels {
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			return fmt.Errorf("channels[%d].name is required", index)
		}
		channel := systemConfigChannelToModel(item)
		if channel.Status == 0 {
			channel.Status = common.ChannelStatusEnabled
		}
		if channel.Weight == nil {
			channel.Weight = common.GetPointer(uint(0))
		}
		if channel.BaseURL == nil {
			channel.BaseURL = common.GetPointer("")
		}
		if channel.Group == "" {
			channel.Group = "default"
		}
		if channel.StatusCodeMapping == nil {
			channel.StatusCodeMapping = common.GetPointer("")
		}
		if channel.Priority == nil {
			channel.Priority = common.GetPointer(int64(0))
		}
		if channel.AutoBan == nil {
			channel.AutoBan = common.GetPointer(1)
		}
		if err := model.ValidateAndNormalizeChannelInfo(&channel.ChannelInfo); err != nil {
			return fmt.Errorf("channel %q metadata is invalid: %w", item.Name, err)
		}
		if err := channel.ValidateSettings(); err != nil {
			return fmt.Errorf("channel %q settings are invalid: %w", item.Name, err)
		}
		for _, modelName := range channel.GetModels() {
			if len(modelName) > 255 {
				return fmt.Errorf("channel %q contains an overlong model name", item.Name)
			}
		}
		configFile.Channels[index] = systemConfigChannelFromModel(channel)
	}
	return nil
}

func buildSystemConfigImportPreview(configFile *SystemConfigFile, hash string) (*SystemConfigImportPreview, error) {
	preview := &SystemConfigImportPreview{
		Hash:      hash,
		Warnings:  make([]SystemConfigIssue, 0),
		Conflicts: make([]SystemConfigIssue, 0),
		Omitted:   configFile.Omitted,
	}

	knownOptions := currentSystemConfigOptions()
	for key, value := range configFile.Options {
		current, ok := knownOptions[key]
		if !ok {
			preview.Options.Skipped++
			preview.Warnings = append(preview.Warnings, SystemConfigIssue{Code: "unknown_option", Item: key})
			continue
		}
		if err := validateSystemConfigOption(key, value); err != nil {
			return nil, err
		}
		if current == value {
			preview.Options.Unchanged++
		} else {
			preview.Options.Update++
			if key == "theme.frontend" {
				preview.ReloadNeeded = true
			}
		}
	}

	var existingVendors []model.Vendor
	if err := model.DB.Find(&existingVendors).Error; err != nil {
		return nil, err
	}
	vendorsByName := make(map[string]model.Vendor, len(existingVendors))
	for _, vendor := range existingVendors {
		vendorsByName[vendor.Name] = vendor
	}
	availableVendors := make(map[string]struct{}, len(existingVendors)+len(configFile.Vendors))
	for name := range vendorsByName {
		availableVendors[name] = struct{}{}
	}
	seenImportedVendors := make(map[string]struct{}, len(configFile.Vendors))
	for _, vendor := range configFile.Vendors {
		if _, ok := seenImportedVendors[vendor.Name]; ok {
			preview.Conflicts = append(preview.Conflicts, SystemConfigIssue{Code: "duplicate_vendor", Item: vendor.Name})
		} else {
			seenImportedVendors[vendor.Name] = struct{}{}
		}
		availableVendors[vendor.Name] = struct{}{}
		current, ok := vendorsByName[vendor.Name]
		if !ok {
			preview.Vendors.Add++
		} else if reflect.DeepEqual(systemConfigVendorFromModel(current), vendor) {
			preview.Vendors.Unchanged++
		} else {
			preview.Vendors.Update++
		}
	}

	var existingModels []model.Model
	if err := model.DB.Find(&existingModels).Error; err != nil {
		return nil, err
	}
	vendorNamesByID := make(map[int]string, len(existingVendors))
	for _, vendor := range existingVendors {
		vendorNamesByID[vendor.Id] = vendor.Name
	}
	modelsByName := make(map[string]model.Model, len(existingModels))
	for _, item := range existingModels {
		modelsByName[item.ModelName] = item
	}
	seenImportedModels := make(map[string]struct{}, len(configFile.Models))
	for _, item := range configFile.Models {
		if _, ok := seenImportedModels[item.ModelName]; ok {
			preview.Conflicts = append(preview.Conflicts, SystemConfigIssue{Code: "duplicate_model", Item: item.ModelName})
		} else {
			seenImportedModels[item.ModelName] = struct{}{}
		}
		if item.VendorName != "" {
			if _, ok := availableVendors[item.VendorName]; !ok {
				preview.Conflicts = append(preview.Conflicts, SystemConfigIssue{Code: "missing_vendor", Item: item.ModelName + ": " + item.VendorName})
			}
		}
		current, ok := modelsByName[item.ModelName]
		if !ok {
			preview.Models.Add++
		} else if reflect.DeepEqual(systemConfigModelFromModel(current, vendorNamesByID[current.VendorID]), item) {
			preview.Models.Unchanged++
		} else {
			preview.Models.Update++
		}
	}

	var existingChannels []model.Channel
	if err := model.DB.Find(&existingChannels).Error; err != nil {
		return nil, err
	}
	channelsByIdentity := make(map[string][]model.Channel, len(existingChannels))
	for _, channel := range existingChannels {
		identity := systemConfigChannelIdentity(channel.Name, channel.Type)
		channelsByIdentity[identity] = append(channelsByIdentity[identity], channel)
	}
	seenImportedChannels := make(map[string]struct{}, len(configFile.Channels))
	for _, item := range configFile.Channels {
		identity := systemConfigChannelIdentity(item.Name, item.Type)
		if _, ok := seenImportedChannels[identity]; ok {
			preview.Conflicts = append(preview.Conflicts, SystemConfigIssue{Code: "duplicate_channel", Item: item.Name})
		} else {
			seenImportedChannels[identity] = struct{}{}
		}
		matches := channelsByIdentity[identity]
		switch len(matches) {
		case 0:
			preview.Channels.Add++
			preview.Warnings = append(preview.Warnings, SystemConfigIssue{Code: "new_channel_disabled", Item: item.Name})
		case 1:
			desired := item
			desired.ChannelInfo.IsMultiKey = matches[0].ChannelInfo.IsMultiKey
			if matches[0].Key == "" {
				desired.Status = common.ChannelStatusManuallyDisabled
			}
			desiredChannel := systemConfigChannelToModel(desired)
			if err := model.ValidateAndNormalizeChannelInfo(&desiredChannel.ChannelInfo); err != nil {
				return nil, err
			}
			desired = systemConfigChannelFromModel(desiredChannel)
			if reflect.DeepEqual(systemConfigChannelFromModel(matches[0]), desired) {
				preview.Channels.Unchanged++
			} else {
				preview.Channels.Update++
			}
		default:
			preview.Conflicts = append(preview.Conflicts, SystemConfigIssue{Code: "ambiguous_channel", Item: item.Name})
		}
	}

	sortSystemConfigIssues(preview.Warnings)
	sortSystemConfigIssues(preview.Conflicts)
	return preview, nil
}

func currentSystemConfigOptions() map[string]string {
	options := make(map[string]string)
	common.OptionMapRWMutex.RLock()
	for key, rawValue := range common.OptionMap {
		if !isExcludedSystemConfigOption(key) {
			options[key] = common.Interface2String(rawValue)
		}
	}
	common.OptionMapRWMutex.RUnlock()
	return options
}

func importSystemConfigOptions(tx *gorm.DB, imported map[string]string, known map[string]string) error {
	keys := make([]string, 0, len(imported))
	for key := range imported {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := known[key]; !ok {
			continue
		}
		value := imported[key]
		if err := validateSystemConfigOption(key, value); err != nil {
			return err
		}
		option := model.Option{Key: key}
		if err := tx.FirstOrCreate(&option, model.Option{Key: key}).Error; err != nil {
			return err
		}
		option.Value = value
		if err := tx.Save(&option).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateSystemConfigOption(key string, value string) error {
	switch key {
	case "performance_setting.simulated_model_cache_memory_budget_mb":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || !common.IsValidSimulatedModelCacheMemoryBudgetMB(parsed) {
			return fmt.Errorf("invalid value for %s", key)
		}
	case "performance_setting.simulated_model_cache_max_entries_per_scope":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || !common.IsValidSimulatedModelCacheEntriesPerScope(parsed) {
			return fmt.Errorf("invalid value for %s", key)
		}
	case "performance_setting.simulated_model_cache_min_input_tokens":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || !common.IsValidSimulatedModelCacheMinInputTokens(parsed) {
			return fmt.Errorf("invalid value for %s", key)
		}
	case "console_setting.announcements":
		if err := console_setting.ValidateConsoleSettings(value, "Announcements"); err != nil {
			return err
		}
	case "console_setting.api_info":
		if err := console_setting.ValidateConsoleSettings(value, "ApiInfo"); err != nil {
			return err
		}
	case "console_setting.faq":
		if err := console_setting.ValidateConsoleSettings(value, "FAQ"); err != nil {
			return err
		}
	case "console_setting.uptime_kuma_groups":
		if err := console_setting.ValidateConsoleSettings(value, "UptimeKumaGroups"); err != nil {
			return err
		}
	}
	return nil
}

func importSystemConfigVendors(tx *gorm.DB, imported []SystemConfigVendor) (map[string]int, error) {
	var existing []model.Vendor
	if err := tx.Find(&existing).Error; err != nil {
		return nil, err
	}
	byName := make(map[string]*model.Vendor, len(existing)+len(imported))
	for index := range existing {
		byName[existing[index].Name] = &existing[index]
	}
	now := common.GetTimestamp()
	for _, item := range imported {
		vendor, ok := byName[item.Name]
		if !ok {
			vendor = &model.Vendor{Name: item.Name, CreatedTime: now}
			byName[item.Name] = vendor
		}
		vendor.Description = item.Description
		vendor.Icon = item.Icon
		vendor.Status = item.Status
		vendor.UpdatedTime = now
		if vendor.Id == 0 {
			if err := tx.Create(vendor).Error; err != nil {
				return nil, err
			}
			if err := tx.Model(vendor).Update("status", item.Status).Error; err != nil {
				return nil, err
			}
		} else if err := tx.Model(&model.Vendor{}).Where("id = ?", vendor.Id).Updates(map[string]any{
			"description":  vendor.Description,
			"icon":         vendor.Icon,
			"status":       vendor.Status,
			"updated_time": vendor.UpdatedTime,
		}).Error; err != nil {
			return nil, err
		}
	}
	ids := make(map[string]int, len(byName))
	for name, vendor := range byName {
		ids[name] = vendor.Id
	}
	return ids, nil
}

func importSystemConfigModels(tx *gorm.DB, imported []SystemConfigModel, vendorIDs map[string]int) error {
	var existing []model.Model
	if err := tx.Find(&existing).Error; err != nil {
		return err
	}
	byName := make(map[string]*model.Model, len(existing))
	for index := range existing {
		byName[existing[index].ModelName] = &existing[index]
	}
	now := common.GetTimestamp()
	for _, item := range imported {
		vendorID := 0
		if item.VendorName != "" {
			var ok bool
			vendorID, ok = vendorIDs[item.VendorName]
			if !ok {
				return fmt.Errorf("model %q references missing vendor %q", item.ModelName, item.VendorName)
			}
		}
		current, ok := byName[item.ModelName]
		if !ok {
			created := model.Model{
				ModelName: item.ModelName, Description: item.Description, Icon: item.Icon,
				Tags: item.Tags, VendorID: vendorID, Endpoints: item.Endpoints,
				Status: item.Status, SyncOfficial: item.SyncOfficial, NameRule: item.NameRule,
				CreatedTime: now, UpdatedTime: now,
			}
			if err := tx.Create(&created).Error; err != nil {
				return err
			}
			if err := tx.Model(&created).Updates(map[string]any{"status": item.Status, "sync_official": item.SyncOfficial}).Error; err != nil {
				return err
			}
			continue
		}
		if err := tx.Model(&model.Model{}).Where("id = ?", current.Id).Updates(map[string]any{
			"description": item.Description, "icon": item.Icon, "tags": item.Tags,
			"vendor_id": vendorID, "endpoints": item.Endpoints, "status": item.Status,
			"sync_official": item.SyncOfficial, "name_rule": item.NameRule, "updated_time": now,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func importSystemConfigChannels(tx *gorm.DB, imported []SystemConfigChannel) error {
	var existing []model.Channel
	if err := tx.Find(&existing).Error; err != nil {
		return err
	}
	byIdentity := make(map[string][]*model.Channel, len(existing))
	for index := range existing {
		identity := systemConfigChannelIdentity(existing[index].Name, existing[index].Type)
		byIdentity[identity] = append(byIdentity[identity], &existing[index])
	}

	for _, item := range imported {
		identity := systemConfigChannelIdentity(item.Name, item.Type)
		matches := byIdentity[identity]
		if len(matches) > 1 {
			return fmt.Errorf("channel %q is ambiguous", item.Name)
		}
		if len(matches) == 0 {
			channel := systemConfigChannelToModel(item)
			channel.Key = ""
			channel.Status = common.ChannelStatusManuallyDisabled
			channel.CreatedTime = common.GetTimestamp()
			if err := model.ValidateAndNormalizeChannelInfo(&channel.ChannelInfo); err != nil {
				return err
			}
			if err := tx.Create(&channel).Error; err != nil {
				return err
			}
			if err := tx.Model(&channel).Update("status", common.ChannelStatusManuallyDisabled).Error; err != nil {
				return err
			}
			if err := channel.UpdateAbilities(tx); err != nil {
				return err
			}
			continue
		}

		current := matches[0]
		updated := systemConfigChannelToModel(item)
		updated.Id = current.Id
		updated.Key = current.Key
		updated.CreatedTime = current.CreatedTime
		updated.ChannelInfo.IsMultiKey = current.ChannelInfo.IsMultiKey
		updated.ChannelInfo.MultiKeySize = current.ChannelInfo.MultiKeySize
		updated.ChannelInfo.MultiKeyStatusList = current.ChannelInfo.MultiKeyStatusList
		updated.ChannelInfo.MultiKeyDisabledReason = current.ChannelInfo.MultiKeyDisabledReason
		updated.ChannelInfo.MultiKeyDisabledTime = current.ChannelInfo.MultiKeyDisabledTime
		updated.ChannelInfo.MultiKeyPollingIndex = current.ChannelInfo.MultiKeyPollingIndex
		if current.Key == "" {
			updated.Status = common.ChannelStatusManuallyDisabled
		}
		if err := model.ValidateAndNormalizeChannelInfo(&updated.ChannelInfo); err != nil {
			return err
		}
		if err := tx.Model(&model.Channel{}).Where("id = ?", current.Id).Updates(map[string]any{
			"open_ai_organization": updated.OpenAIOrganization, "test_model": updated.TestModel,
			"status": updated.Status, "name": updated.Name, "weight": updated.Weight,
			"base_url": updated.BaseURL, "other": updated.Other, "models": updated.Models,
			"group": updated.Group, "model_mapping": updated.ModelMapping,
			"status_code_mapping": updated.StatusCodeMapping, "priority": updated.Priority,
			"auto_ban": updated.AutoBan, "tag": updated.Tag, "setting": updated.Setting,
			"param_override": updated.ParamOverride, "header_override": updated.HeaderOverride,
			"remark": updated.Remark, "channel_info": updated.ChannelInfo, "settings": updated.OtherSettings,
		}).Error; err != nil {
			return err
		}
		if err := updated.UpdateAbilities(tx); err != nil {
			return err
		}
	}
	return nil
}

func systemConfigVendorFromModel(vendor model.Vendor) SystemConfigVendor {
	return SystemConfigVendor{Name: vendor.Name, Description: vendor.Description, Icon: vendor.Icon, Status: vendor.Status}
}

func systemConfigModelFromModel(item model.Model, vendorName string) SystemConfigModel {
	return SystemConfigModel{
		ModelName: item.ModelName, Description: item.Description, Icon: item.Icon,
		Tags: item.Tags, VendorName: vendorName, Endpoints: item.Endpoints,
		Status: item.Status, SyncOfficial: item.SyncOfficial, NameRule: item.NameRule,
	}
}

func systemConfigChannelFromModel(channel model.Channel) SystemConfigChannel {
	return SystemConfigChannel{
		Type: channel.Type, OpenAIOrganization: channel.OpenAIOrganization, TestModel: channel.TestModel,
		Status: channel.Status, Name: channel.Name, Weight: channel.Weight, BaseURL: channel.BaseURL,
		Other: channel.Other, Models: channel.Models, Group: channel.Group, ModelMapping: channel.ModelMapping,
		StatusCodeMapping: channel.StatusCodeMapping, Priority: channel.Priority, AutoBan: channel.AutoBan,
		Tag: channel.Tag, Setting: channel.Setting, ParamOverride: channel.ParamOverride,
		HeaderOverride: channel.HeaderOverride, Remark: channel.Remark, OtherSettings: channel.OtherSettings,
		ChannelInfo: SystemConfigChannelInfo{
			IsMultiKey:                         channel.ChannelInfo.IsMultiKey,
			MultiKeyAffinityTTLSeconds:         channel.ChannelInfo.MultiKeyAffinityTTLSeconds,
			MultiKeyLeastRequestsWindowSeconds: channel.ChannelInfo.MultiKeyLeastRequestsWindowSeconds,
			MultiKeyMode:                       string(channel.ChannelInfo.MultiKeyMode),
			ChannelOverloadProtection:          channel.ChannelInfo.ChannelOverloadProtection,
			MultiKeyOverloadProtection:         channel.ChannelInfo.MultiKeyOverloadProtection,
		},
	}
}

func systemConfigChannelToModel(item SystemConfigChannel) model.Channel {
	return model.Channel{
		Type: item.Type, OpenAIOrganization: item.OpenAIOrganization, TestModel: item.TestModel,
		Status: item.Status, Name: item.Name, Weight: item.Weight, BaseURL: item.BaseURL,
		Other: item.Other, Models: item.Models, Group: item.Group, ModelMapping: item.ModelMapping,
		StatusCodeMapping: item.StatusCodeMapping, Priority: item.Priority, AutoBan: item.AutoBan,
		Tag: item.Tag, Setting: item.Setting, ParamOverride: item.ParamOverride,
		HeaderOverride: item.HeaderOverride, Remark: item.Remark, OtherSettings: item.OtherSettings,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:                         item.ChannelInfo.IsMultiKey,
			MultiKeyAffinityTTLSeconds:         item.ChannelInfo.MultiKeyAffinityTTLSeconds,
			MultiKeyLeastRequestsWindowSeconds: item.ChannelInfo.MultiKeyLeastRequestsWindowSeconds,
			MultiKeyMode:                       constant.MultiKeyMode(item.ChannelInfo.MultiKeyMode),
			ChannelOverloadProtection:          item.ChannelInfo.ChannelOverloadProtection,
			MultiKeyOverloadProtection:         item.ChannelInfo.MultiKeyOverloadProtection,
		},
	}
}

func systemConfigChannelIdentity(name string, channelType int) string {
	return fmt.Sprintf("%s\x00%d", strings.TrimSpace(name), channelType)
}

func sortSystemConfigIssues(issues []SystemConfigIssue) {
	sort.Slice(issues, func(i int, j int) bool {
		if issues[i].Code == issues[j].Code {
			return issues[i].Item < issues[j].Item
		}
		return issues[i].Code < issues[j].Code
	})
}

func defaultSystemConfigOmitted() SystemConfigOmitted {
	return SystemConfigOmitted{
		OptionGroups:  []string{"OAuth", "SMTP", "Worker", "external payment providers", "payment compliance", "custom OAuth providers"},
		ChannelFields: []string{"key", "multi-key contents and runtime state", "balance", "used quota", "test and response statistics", "other_info"},
		Data:          []string{"users", "API tokens", "logs", "usage", "tasks", "deployments", "cache contents"},
	}
}
