/*
Copyright (C) 2023-2026 QuantumNous
*/

package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecoverMoonshotQuotaKeysPreservesManualAndMonthlyStates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}))
	previousDB := DB
	previousCacheEnabled := common.MemoryCacheEnabled
	DB = db
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousCacheEnabled
	})

	channel := &Channel{
		Type:          constant.ChannelTypeMoonshot,
		Key:           "expired\nmanual\nenabled",
		Status:        common.ChannelStatusAutoDisabled,
		OtherSettings: `{"moonshot_quota_auto_disable":{"enabled":true}}`,
		ChannelInfo: ChannelInfo{
			IsMultiKey:         true,
			MultiKeySize:       3,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusAutoDisabled, 1: common.ChannelStatusManuallyDisabled},
			MultiKeyMoonshotQuotaStatus: map[int]MoonshotQuotaStatus{
				0: {FiveHourUntil: 10},
				1: {MonthlyNoSubscription: true},
			},
		},
	}
	require.NoError(t, db.Create(channel).Error)

	recovered, err := RecoverMoonshotQuotaKeys(100)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	require.Equal(t, common.ChannelStatusEnabled, stored.Status)
	require.NotContains(t, stored.ChannelInfo.MultiKeyStatusList, 0)
	require.Equal(t, common.ChannelStatusManuallyDisabled, stored.ChannelInfo.MultiKeyStatusList[1])
	require.True(t, stored.ChannelInfo.MultiKeyMoonshotQuotaStatus[1].MonthlyNoSubscription)
}
