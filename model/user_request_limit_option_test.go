package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUserRequestLimitOptionsPersistValidValuesAndRejectInvalidValues(t *testing.T) {
	previousDB := DB
	previousOptionMap := common.OptionMap
	previousConcurrencyLimit := setting.GetUserConcurrentRequestLimit()
	previousTokenLimit := setting.GetUserTokensPerMinuteLimit()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptionMap
		setting.SetUserConcurrentRequestLimit(previousConcurrencyLimit)
		setting.SetUserTokensPerMinuteLimit(previousTokenLimit)
	})

	require.NoError(t, UpdateOption("UserConcurrentRequestLimit", "8"))
	require.NoError(t, UpdateOption("UserTokensPerMinuteLimit", "120000"))
	assert.Equal(t, 8, setting.GetUserConcurrentRequestLimit())
	assert.Equal(t, 120000, setting.GetUserTokensPerMinuteLimit())

	for _, value := range []string{"-1", "not-a-number", "100000001"} {
		require.Error(t, UpdateOption("UserConcurrentRequestLimit", value))
		require.Error(t, UpdateOption("UserTokensPerMinuteLimit", value))
	}

	var option Option
	require.NoError(t, db.First(&option, "key = ?", "UserConcurrentRequestLimit").Error)
	assert.Equal(t, "8", option.Value)
}
