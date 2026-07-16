package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelRejectsInvalidKeyIndexWithoutExposingSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRedisEnabled := common.RedisEnabled
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RedisEnabled = originalRedisEnabled
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	require.NoError(t, db.Create(&model.Channel{
		Id:   1,
		Name: "multi-key",
		Key:  "secret-a\nsecret-b",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id:   2,
		Name: "single-key",
		Key:  "single-secret",
	}).Error)

	testCases := []struct {
		name        string
		channelID   int
		query       string
		wantMessage string
	}{
		{name: "non-integer", channelID: 1, query: "abc", wantMessage: "密钥索引无效"},
		{name: "negative", channelID: 1, query: "-1", wantMessage: "密钥索引无效"},
		{name: "out of range", channelID: 1, query: "2", wantMessage: "密钥索引超出范围"},
		{name: "single-key channel", channelID: 2, query: "0", wantMessage: "该渠道不是多密钥模式"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(
				http.MethodGet,
				fmt.Sprintf("/api/channel/test/%d?key_index=%s", testCase.channelID, testCase.query),
				nil,
			)
			ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", testCase.channelID)}}

			TestChannel(ctx)

			var response struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, testCase.wantMessage, response.Message)
			assert.NotContains(t, recorder.Body.String(), "secret-a")
			assert.NotContains(t, recorder.Body.String(), "secret-b")
			assert.NotContains(t, recorder.Body.String(), "single-secret")
		})
	}
}
