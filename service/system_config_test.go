package service

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSystemConfigTest(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Vendor{}, &model.Model{}, &model.Channel{}, &model.Ability{}))

	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalSystemName := common.SystemName
	common.OptionMapRWMutex.Lock()
	originalOptionMap := common.OptionMap
	common.OptionMap = map[string]string{
		"SystemName":                     "Target",
		"SMTPToken":                      "smtp-secret",
		"WorkerUrl":                      "https://worker.example.com",
		"model_deployment.ionet.api_key": "deployment-secret",
		"custom.integration_secret":      "future-secret",
	}
	common.OptionMapRWMutex.Unlock()
	model.DB = db
	model.LOG_DB = db
	common.MemoryCacheEnabled = false

	t.Cleanup(func() {
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.SystemName = originalSystemName
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalOptionMap
		common.OptionMapRWMutex.Unlock()
	})
	return db
}

func TestExportSystemConfigExcludesCredentialsAndRuntimeState(t *testing.T) {
	db := setupSystemConfigTest(t)
	require.NoError(t, db.Create(&model.Vendor{Name: "OpenAI", Status: 1}).Error)
	require.NoError(t, db.Create(&model.Model{ModelName: "gpt-test", VendorID: 1, Status: 1}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Name: "primary", Type: constant.ChannelTypeOpenAI, Key: "sk-secret", Status: common.ChannelStatusEnabled,
		Models: "gpt-test", Group: "default", Balance: 42, UsedQuota: 99, OtherInfo: `{"runtime":true}`,
		HeaderOverride: common.GetPointer(`{"X-Trace":"keep-me"}`),
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{0: 1},
		},
	}).Error)

	configFile, err := ExportSystemConfig()
	require.NoError(t, err)
	assert.Equal(t, "Target", configFile.Options["SystemName"])
	assert.NotContains(t, configFile.Options, "SMTPToken")
	assert.NotContains(t, configFile.Options, "WorkerUrl")
	assert.NotContains(t, configFile.Options, "model_deployment.ionet.api_key")
	assert.NotContains(t, configFile.Options, "custom.integration_secret")
	require.Len(t, configFile.Channels, 1)
	assert.Equal(t, `{"X-Trace":"keep-me"}`, *configFile.Channels[0].HeaderOverride)
	assert.True(t, configFile.Channels[0].ChannelInfo.IsMultiKey)

	raw, err := common.Marshal(configFile)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sk-secret")
	assert.NotContains(t, string(raw), "smtp-secret")
	assert.NotContains(t, string(raw), "deployment-secret")
	assert.NotContains(t, string(raw), "future-secret")
	assert.NotContains(t, string(raw), "used_quota")
	assert.NotContains(t, string(raw), `"other_info":`)
}

func TestSystemConfigImportMergesAndBecomesIdempotent(t *testing.T) {
	db := setupSystemConfigTest(t)
	vendor := model.Vendor{Name: "Existing vendor", Description: "old", Status: 1}
	require.NoError(t, db.Create(&vendor).Error)
	existingChannel := model.Channel{
		Name: "primary", Type: constant.ChannelTypeOpenAI, Key: "sk-keep", Status: common.ChannelStatusEnabled,
		Models: "old-model", Group: "default", Balance: 12, UsedQuota: 34, OtherInfo: `{"runtime":true}`,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 1, MultiKeyStatusList: map[int]int{0: 1}},
	}
	require.NoError(t, db.Create(&existingChannel).Error)

	configFile := SystemConfigFile{
		Schema: SystemConfigSchema, Version: SystemConfigVersion,
		Options: map[string]string{"SystemName": "Imported"},
		Vendors: []SystemConfigVendor{{Name: "Existing vendor", Description: "new", Status: 1}},
		Models:  []SystemConfigModel{{ModelName: "new-model", VendorName: "Existing vendor", Status: 1, SyncOfficial: 0}},
		Channels: []SystemConfigChannel{
			{
				Name: "primary", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled,
				Models: "new-model", Group: "default", HeaderOverride: common.GetPointer(`{"X-Test":"yes"}`),
				ChannelInfo: SystemConfigChannelInfo{IsMultiKey: false},
			},
			{
				Name: "imported without key", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled,
				Models: "new-model", Group: "default",
			},
		},
	}
	data, err := common.Marshal(configFile)
	require.NoError(t, err)

	preview, err := PreviewSystemConfigImport(data)
	require.NoError(t, err)
	assert.Equal(t, 1, preview.Options.Update)
	assert.Equal(t, 1, preview.Vendors.Update)
	assert.Equal(t, 1, preview.Models.Add)
	assert.Equal(t, 1, preview.Channels.Add)
	assert.Equal(t, 1, preview.Channels.Update)
	assert.Empty(t, preview.Conflicts)

	_, err = ApplySystemConfigImport(data, preview.Hash)
	require.NoError(t, err)

	var importedOption model.Option
	require.NoError(t, db.First(&importedOption, "key = ?", "SystemName").Error)
	assert.Equal(t, "Imported", importedOption.Value)
	var channels []model.Channel
	require.NoError(t, db.Order("id asc").Find(&channels).Error)
	require.Len(t, channels, 2)
	assert.Equal(t, "sk-keep", channels[0].Key)
	assert.Equal(t, float64(12), channels[0].Balance)
	assert.Equal(t, int64(34), channels[0].UsedQuota)
	assert.Equal(t, `{"runtime":true}`, channels[0].OtherInfo)
	assert.True(t, channels[0].ChannelInfo.IsMultiKey)
	assert.Equal(t, "new-model", channels[0].Models)
	assert.Empty(t, channels[1].Key)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, channels[1].Status)

	var ability model.Ability
	require.NoError(t, db.First(&ability, "channel_id = ? AND model = ?", channels[0].Id, "new-model").Error)
	assert.True(t, ability.Enabled)

	secondPreview, err := PreviewSystemConfigImport(data)
	require.NoError(t, err)
	assert.Equal(t, 2, secondPreview.Channels.Unchanged)
	assert.Zero(t, secondPreview.Channels.Update)
}

func TestSystemConfigImportRejectsForbiddenFields(t *testing.T) {
	setupSystemConfigTest(t)

	forbiddenOption := []byte(`{"schema":"new-api.system-config","version":1,"options":{"SMTPToken":"secret"}}`)
	_, err := PreviewSystemConfigImport(forbiddenOption)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")

	forbiddenChannelKey := []byte(`{"schema":"new-api.system-config","version":1,"channels":[{"name":"bad","type":1,"key":"secret"}]}`)
	_, err = PreviewSystemConfigImport(forbiddenChannelKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channels[0].key")
}

func TestSystemConfigImportReportsAmbiguousTargetChannel(t *testing.T) {
	db := setupSystemConfigTest(t)
	require.NoError(t, db.Create(&model.Channel{Name: "duplicate", Type: 1, Key: "a"}).Error)
	require.NoError(t, db.Create(&model.Channel{Name: "duplicate", Type: 1, Key: "b"}).Error)
	data := []byte(`{"schema":"new-api.system-config","version":1,"channels":[{"name":"duplicate","type":1}]}`)

	preview, err := PreviewSystemConfigImport(data)
	require.NoError(t, err)
	require.Len(t, preview.Conflicts, 1)
	assert.Equal(t, "ambiguous_channel", preview.Conflicts[0].Code)
}

func TestSystemConfigImportReportsDuplicateFileRecords(t *testing.T) {
	setupSystemConfigTest(t)
	data := []byte(`{
		"schema":"new-api.system-config",
		"version":1,
		"vendors":[{"name":"vendor"},{"name":"vendor"}],
		"models":[{"model_name":"model","vendor_name":"vendor"},{"model_name":"model","vendor_name":"vendor"}],
		"channels":[{"name":"channel","type":1},{"name":"channel","type":1}]
	}`)

	preview, err := PreviewSystemConfigImport(data)
	require.NoError(t, err)
	codes := make([]string, 0, len(preview.Conflicts))
	for _, conflict := range preview.Conflicts {
		codes = append(codes, conflict.Code)
	}
	assert.ElementsMatch(t, []string{"duplicate_vendor", "duplicate_model", "duplicate_channel"}, codes)
}

func TestSystemConfigImportRollsBackEverySection(t *testing.T) {
	db := setupSystemConfigTest(t)
	channel := model.Channel{Name: "primary", Type: 1, Key: "keep", Models: "old", Group: "default"}
	require.NoError(t, db.Create(&channel).Error)
	configFile := SystemConfigFile{
		Schema: SystemConfigSchema, Version: SystemConfigVersion,
		Options:  map[string]string{"SystemName": "must-roll-back"},
		Vendors:  []SystemConfigVendor{{Name: "must-roll-back", Status: 1}},
		Channels: []SystemConfigChannel{{Name: "primary", Type: 1, Models: "new", Group: "default"}},
	}
	data, err := common.Marshal(configFile)
	require.NoError(t, err)
	preview, err := PreviewSystemConfigImport(data)
	require.NoError(t, err)
	require.NoError(t, db.Migrator().DropTable(&model.Ability{}))

	_, err = ApplySystemConfigImport(data, preview.Hash)
	require.Error(t, err)
	var optionCount int64
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", "SystemName").Count(&optionCount).Error)
	assert.Zero(t, optionCount)
	var vendorCount int64
	require.NoError(t, db.Model(&model.Vendor{}).Where("name = ?", "must-roll-back").Count(&vendorCount).Error)
	assert.Zero(t, vendorCount)
	var persisted model.Channel
	require.NoError(t, db.First(&persisted, channel.Id).Error)
	assert.Equal(t, "old", persisted.Models)
}
