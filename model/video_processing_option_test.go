package model

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/videocompat"
	"github.com/QuantumNous/new-api/setting/performance_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestVideoProcessingOptionsDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "video-options.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: fmt.Sprintf("video_options_%x_", time.Now().UnixNano())}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType := DB, common.MainDatabaseType()
			previousVideo, previousMonitor := videocompat.GetConfig(), common.GetPerformanceMonitorConfig()
			previousPerformance := *performance_setting.GetPerformanceSetting()
			previousPolicy := CurrentRequestPolicy()
			common.OptionMapRWMutex.Lock()
			previousOptions := maps.Clone(common.OptionMap)
			common.OptionMap = maps.Clone(previousPolicy.Options)
			common.OptionMapRWMutex.Unlock()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&Option{}))
				*performance_setting.GetPerformanceSetting() = previousPerformance
				performance_setting.UpdateAndSync()
				common.SetPerformanceMonitorConfig(previousMonitor)
				require.NoError(t, videocompat.SetConfig(previousVideo))
				requestPolicySnapshot.Store(previousPolicy)
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousOptions
				common.OptionMapRWMutex.Unlock()
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				initCol()
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Option{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			for key, value := range map[string]string{
				"performance_setting.monitor_enabled":          "true",
				"performance_setting.monitor_cpu_threshold":    "90",
				"performance_setting.monitor_memory_threshold": "90",
				"performance_setting.monitor_disk_threshold":   "95",
			} {
				require.NoError(t, updateOptionMap(key, value))
			}
			require.NoError(t, videocompat.SetConfig(videocompat.DefaultConfig()))
			config := videocompat.DefaultConfig()
			config.MaxConcurrency = 7
			config.AdaptiveEnabled = true
			encoded, err := common.Marshal(config)
			require.NoError(t, err)
			require.NoError(t, UpdateOption(VideoProcessingConfigOptionKey, string(encoded)))
			assert.Equal(t, config, videocompat.GetConfig(), "the complete configuration takes effect after a successful save")
			var stored Option
			require.NoError(t, db.Where(map[string]any{"key": VideoProcessingConfigOptionKey}).First(&stored).Error)
			assert.JSONEq(t, string(encoded), stored.Value)
			common.OptionMapRWMutex.RLock()
			assert.Equal(t, stored.Value, common.OptionMap[VideoProcessingConfigOptionKey])
			common.OptionMapRWMutex.RUnlock()

			for _, invalid := range []struct {
				name   string
				change func(map[string]any)
			}{
				{"unknown", func(m map[string]any) { m["unknown"] = true }},
				{"missing limit", func(m map[string]any) { delete(m, "queue_capacity") }},
				{"null boolean", func(m map[string]any) { m["adaptive_enabled"] = nil }},
				{"fractional concurrency", func(m map[string]any) { m["max_concurrency"] = 1.5 }},
				{"zero concurrency", func(m map[string]any) { m["max_concurrency"] = 0 }},
				{"missing nested level", func(m map[string]any) { delete(m["adaptive_config"].(map[string]any), "light") }},
				{"unknown nested field", func(m map[string]any) { m["adaptive_config"].(map[string]any)["unknown"] = true }},
			} {
				t.Run(invalid.name, func(t *testing.T) {
					var candidate map[string]any
					require.NoError(t, common.Unmarshal(encoded, &candidate))
					invalid.change(candidate)
					data, err := common.Marshal(candidate)
					require.NoError(t, err)
					assert.Error(t, UpdateOption(VideoProcessingConfigOptionKey, string(data)))
					assert.Equal(t, config, videocompat.GetConfig(), "rejected writes keep the running configuration")
				})
			}
			assert.Error(t, UpdateOption("video_processing_setting.max_concurrency", "9"), "partial option writes cannot bypass complete-document validation")
			stored = Option{}
			require.NoError(t, db.Where(map[string]any{"key": VideoProcessingConfigOptionKey}).First(&stored).Error)
			assert.JSONEq(t, string(encoded), stored.Value, "invalid requests never replace the persisted configuration")

			config.MaxConcurrency = 9
			config.QueueWaitSeconds = 20
			encoded, err = common.Marshal(config)
			require.NoError(t, err)
			require.NoError(t, db.Model(&Option{}).Where(map[string]any{"key": VideoProcessingConfigOptionKey}).Update("value", string(encoded)).Error)
			loadOptionsFromDatabase()
			loadOptionsFromDatabase()
			assert.Equal(t, config, videocompat.GetConfig(), "another node's database configuration is applied idempotently")
			require.NoError(t, db.Model(&Option{}).Where(map[string]any{"key": VideoProcessingConfigOptionKey}).Update("value", `{"max_concurrency":1}`).Error)
			loadOptionsFromDatabase()
			assert.Equal(t, config, videocompat.GetConfig(), "a malformed database document keeps the last valid snapshot")
			common.OptionMapRWMutex.RLock()
			assert.JSONEq(t, string(encoded), common.OptionMap[VideoProcessingConfigOptionKey])
			common.OptionMapRWMutex.RUnlock()
			require.NoError(t, db.Model(&Option{}).Where(map[string]any{"key": VideoProcessingConfigOptionKey}).Update("value", string(encoded)).Error)

			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("reject_video_configuration", func(tx *gorm.DB) {
				if option, ok := tx.Statement.Dest.(*Option); ok && option.Key == VideoProcessingConfigOptionKey {
					tx.AddError(errors.New("configuration save rejected"))
				}
			}))
			candidate := config
			candidate.MaxConcurrency = 11
			data, err := common.Marshal(candidate)
			require.NoError(t, err)
			assert.Error(t, UpdateOptionsBulk(map[string]string{VideoProcessingConfigOptionKey: string(data), "VideoRollbackSentinel": "changed"}))
			require.NoError(t, db.Callback().Update().Remove("reject_video_configuration"))
			assert.Equal(t, config, videocompat.GetConfig(), "a failed database commit never publishes the candidate")
			stored = Option{}
			require.NoError(t, db.Where(map[string]any{"key": VideoProcessingConfigOptionKey}).First(&stored).Error)
			assert.JSONEq(t, string(encoded), stored.Value)
			var sentinelCount int64
			require.NoError(t, db.Model(&Option{}).Where(map[string]any{"key": "VideoRollbackSentinel"}).Count(&sentinelCount).Error)
			assert.Zero(t, sentinelCount, "the other option in the failed transaction is rolled back")

			assert.Error(t, UpdateOption("performance_setting.monitor_cpu_threshold", strconv.Itoa(config.AdaptiveConfig.Heavy.CPUThreshold)))
			assert.Equal(t, 90, common.GetPerformanceMonitorConfig().CPUThreshold, "a hard gate cannot be lowered before the adaptive trigger")
			candidate = config
			candidate.AdaptiveConfig.Light.CPUThreshold = 65
			candidate.AdaptiveConfig.Heavy.CPUThreshold = 75
			data, err = common.Marshal(candidate)
			require.NoError(t, err)
			require.NoError(t, UpdateOptionsBulk(map[string]string{VideoProcessingConfigOptionKey: string(data), "performance_setting.monitor_cpu_threshold": "80"}))
			assert.Equal(t, candidate, videocompat.GetConfig(), "combined threshold changes validate their final state")
			assert.Equal(t, 80, common.GetPerformanceMonitorConfig().CPUThreshold)
			loadOptionsFromDatabase()
			assert.Equal(t, candidate, videocompat.GetConfig())
			assert.Equal(t, 80, common.GetPerformanceMonitorConfig().CPUThreshold)
		})
	}
}
