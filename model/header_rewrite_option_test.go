package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useHeaderRewriteOptionDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousOptionMap := common.OptionMap
	originalPresets := operation_setting.HeaderRewritePresets2JSONString()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}, &Channel{}))
	DB = db
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptionMap
		require.NoError(t, operation_setting.UpdateHeaderRewritePresetsByJSONString(originalPresets))
	})
	return db
}

func TestUpdateHeaderRewritePresetsRejectsRemovingReferencedPreset(t *testing.T) {
	db := useHeaderRewriteOptionDB(t)
	setting := `{"header_rewrite":{"preset_id":"custom"}}`
	require.NoError(t, db.Create(&Channel{Name: "uses custom", Setting: &setting}).Error)

	err := UpdateOption(operation_setting.HeaderRewritePresetsOptionKey, `{
		"other":{"name":"Other","set":{"User-Agent":"other/1"}}
	}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `preset "custom" is referenced by channel`)

	var count int64
	require.NoError(t, db.Model(&Option{}).Where("key = ?", operation_setting.HeaderRewritePresetsOptionKey).Count(&count).Error)
	assert.Zero(t, count)
}

func TestUpdateHeaderRewritePresetsAllowsReferencedPresetUpdate(t *testing.T) {
	db := useHeaderRewriteOptionDB(t)
	setting := `{"header_rewrite":{"preset_id":"custom"}}`
	require.NoError(t, db.Create(&Channel{Name: "uses custom", Setting: &setting}).Error)

	err := UpdateOption(operation_setting.HeaderRewritePresetsOptionKey, `{
		"custom":{"name":"Custom","set":{"User-Agent":"custom/2"}}
	}`)
	require.NoError(t, err)

	preset, ok := operation_setting.GetHeaderRewritePreset("custom")
	require.True(t, ok)
	assert.Equal(t, "custom/2", preset.Set["User-Agent"])
}
