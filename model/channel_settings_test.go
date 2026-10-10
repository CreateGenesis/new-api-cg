package model

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	filterdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestChannelValidateSettingsRejectsInvalidHTTPTransport(t *testing.T) {
	tests := []struct {
		name    string
		setting dto.ChannelSettings
		wantErr string
	}{
		{
			name:    "auto with shards is valid",
			setting: dto.ChannelSettings{HTTPProtocol: "auto", HTTP2ConnectionShards: 4},
		},
		{
			name:    "http1 with shards greater than one rejected",
			setting: dto.ChannelSettings{HTTPProtocol: "http1", HTTP2ConnectionShards: 2},
			wantErr: "http2_connection_shards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{}
			channel.SetSetting(tt.setting)
			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestChannelValidateSettingsAcceptsMultipleProxyAddresses(t *testing.T) {
	channel := &Channel{}
	channel.SetSetting(dto.ChannelSettings{
		Proxy: "socks5://proxy-a:1080\nsocks5h://proxy-b:1080",
	})

	require.NoError(t, channel.ValidateSettings())
}

func TestChannelValidateSettingsRejectsInvalidProxyAddressInList(t *testing.T) {
	channel := &Channel{}
	channel.SetSetting(dto.ChannelSettings{
		Proxy: "socks5://proxy-a:1080\nftp://proxy-b:21",
	})

	err := channel.ValidateSettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid channel proxy")
}

func TestChannelValidateSettingsAcceptsStatusCodeRetryDefaults(t *testing.T) {
	channel := &Channel{
		OtherSettings: `{"status_code_retry":{"enabled":true}}`,
	}

	require.NoError(t, channel.ValidateSettings())
}

func TestChannelValidateSettingsRejectsInvalidStatusCodeRetryRules(t *testing.T) {
	channel := &Channel{
		OtherSettings: `{"status_code_retry":{"enabled":true,"status_codes":"99,600"}}`,
	}

	err := channel.ValidateSettings()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status_code_retry.status_codes")
}

func TestChannelValidateSettingsAcceptsResponseHeaderTimeoutDefault(t *testing.T) {
	channel := &Channel{
		OtherSettings: `{"response_header_timeout":{"enabled":true}}`,
	}

	require.NoError(t, channel.ValidateSettings())
}

func TestChannelValidateSettingsRejectsInvalidResponseHeaderTimeout(t *testing.T) {
	for _, timeoutSeconds := range []int{0, 86401} {
		channel := &Channel{
			OtherSettings: fmt.Sprintf(`{"response_header_timeout":{"enabled":true,"timeout_seconds":%d}}`, timeoutSeconds),
		}

		err := channel.ValidateSettings()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "response_header_timeout.timeout_seconds")
	}
}

func TestChannelValidateSettingsRejectsUnknownStreamInterruptionBillingMode(t *testing.T) {
	channel := &Channel{
		OtherSettings: `{"stream_interruption_billing":{"mode":"unknown"}}`,
	}

	err := channel.ValidateSettings()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream_interruption_billing.mode")
}

func TestChannelValidateSettingsAcceptsStreamInterruptionBillingModes(t *testing.T) {
	for _, mode := range []string{"", "input_only_free", "all_interrupted_free"} {
		channel := &Channel{
			OtherSettings: `{"stream_interruption_billing":{"mode":"` + mode + `"}}`,
		}
		require.NoError(t, channel.ValidateSettings(), "mode=%q", mode)
	}
}

func TestChannelValidateSettingsEnforcesRequestModePolicyExclusivity(t *testing.T) {
	for _, settings := range []string{
		`{}`,
		`{"disable_stream":true}`,
		`{"disable_non_stream":true}`,
	} {
		channel := &Channel{OtherSettings: settings}
		require.NoError(t, channel.ValidateSettings(), "settings=%s", settings)
	}

	channel := &Channel{OtherSettings: `{"disable_stream":true,"disable_non_stream":true}`}
	err := channel.ValidateSettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot both be enabled")
}

func TestChannelValidateSettingsScopesMoonshotQuotaAutoDisable(t *testing.T) {
	moonshot := &Channel{Type: constant.ChannelTypeMoonshot, OtherSettings: `{"moonshot_quota_auto_disable":{"enabled":true}}`}
	require.NoError(t, moonshot.ValidateSettings())

	nonMoonshot := &Channel{Type: constant.ChannelTypeOpenAI, OtherSettings: `{"moonshot_quota_auto_disable":{"enabled":true}}`}
	err := nonMoonshot.ValidateSettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only supported for Moonshot")
}

func TestChannelValidateSettingsScopesTNTTencentConversion(t *testing.T) {
	t.Run("accepts Anthropic channel", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			OtherSettings: `{"tnt_tencent_openai_conversion":true}`,
		}
		require.NoError(t, channel.ValidateSettings())
	})

	t.Run("rejects other channel types", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeOpenAI,
			OtherSettings: `{"tnt_tencent_openai_conversion":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported for Anthropic channels")
	})

	t.Run("rejects body passthrough", func(t *testing.T) {
		setting := `{"pass_through_body_enabled":true}`
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			Setting:       &setting,
			OtherSettings: `{"tnt_tencent_openai_conversion":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot both be enabled")
	})
}

func TestChannelValidateSettingsScopesAnthropicInputIncludesCache(t *testing.T) {
	t.Run("accepts Anthropic and TNT conversion", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			OtherSettings: `{"anthropic_input_includes_cache":true,"tnt_tencent_openai_conversion":true}`,
		}
		require.NoError(t, channel.ValidateSettings())
	})

	t.Run("rejects other channel types", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeOpenAI,
			OtherSettings: `{"anthropic_input_includes_cache":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported for Anthropic channels")
	})

	t.Run("rejects body passthrough", func(t *testing.T) {
		setting := `{"pass_through_body_enabled":true}`
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			Setting:       &setting,
			OtherSettings: `{"anthropic_input_includes_cache":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot both be enabled")
	})
}

func TestChannelValidateSettingsScopesKimiK3OfficialCompatibility(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic, constant.ChannelTypeMoonshot} {
		channel := &Channel{Type: channelType, OtherSettings: `{"kimi_k3_official_compatibility":true}`}
		require.NoError(t, channel.ValidateSettings(), "channel type %d", channelType)
	}

	t.Run("rejects unsupported channel type", func(t *testing.T) {
		channel := &Channel{Type: constant.ChannelTypeGemini, OtherSettings: `{"kimi_k3_official_compatibility":true}`}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported for OpenAI, Anthropic, and Moonshot")
	})

	t.Run("rejects body passthrough", func(t *testing.T) {
		setting := `{"pass_through_body_enabled":true}`
		channel := &Channel{
			Type:          constant.ChannelTypeOpenAI,
			Setting:       &setting,
			OtherSettings: `{"kimi_k3_official_compatibility":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot both be enabled")
	})

	t.Run("allows TNT conversion on Anthropic channel", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			OtherSettings: `{"kimi_k3_official_compatibility":true,"tnt_tencent_openai_conversion":true}`,
		}
		require.NoError(t, channel.ValidateSettings())
	})
}

func TestChannelValidateSettingsScopesGLM53OfficialCompatibility(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic} {
		channel := &Channel{Type: channelType, OtherSettings: `{"glm_5_3_official_compatibility":true}`}
		require.NoError(t, channel.ValidateSettings(), "channel type %d", channelType)
	}

	t.Run("rejects unsupported channel type", func(t *testing.T) {
		channel := &Channel{Type: constant.ChannelTypeMoonshot, OtherSettings: `{"glm_5_3_official_compatibility":true}`}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported for OpenAI and Anthropic")
	})

	t.Run("rejects body passthrough", func(t *testing.T) {
		setting := `{"pass_through_body_enabled":true}`
		channel := &Channel{
			Type:          constant.ChannelTypeOpenAI,
			Setting:       &setting,
			OtherSettings: `{"glm_5_3_official_compatibility":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot both be enabled")
	})

	t.Run("rejects Kimi compatibility", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			OtherSettings: `{"glm_5_3_official_compatibility":true,"kimi_k3_official_compatibility":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot both be enabled")
	})

	t.Run("allows TNT conversion", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			OtherSettings: `{"glm_5_3_official_compatibility":true,"tnt_tencent_openai_conversion":true}`,
		}
		require.NoError(t, channel.ValidateSettings())
	})
}

func TestChannelValidateSettingsScopesDeepSeekV4OfficialCompatibility(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic, constant.ChannelTypeDeepSeek} {
		channel := &Channel{Type: channelType, OtherSettings: `{"deepseek_v4_official_compatibility":true}`}
		require.NoError(t, channel.ValidateSettings(), "channel type %d", channelType)
	}

	t.Run("rejects unsupported channel type", func(t *testing.T) {
		channel := &Channel{Type: constant.ChannelTypeGemini, OtherSettings: `{"deepseek_v4_official_compatibility":true}`}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported for OpenAI, Anthropic, and DeepSeek")
	})

	t.Run("rejects body passthrough", func(t *testing.T) {
		setting := `{"pass_through_body_enabled":true}`
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			Setting:       &setting,
			OtherSettings: `{"deepseek_v4_official_compatibility":true,"tnt_tencent_openai_conversion":true}`,
		}
		err := channel.ValidateSettings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot both be enabled")
	})

	t.Run("rejects Kimi and GLM compatibility", func(t *testing.T) {
		for _, conflict := range []string{"kimi_k3_official_compatibility", "glm_5_3_official_compatibility"} {
			channel := &Channel{
				Type:          constant.ChannelTypeAnthropic,
				OtherSettings: fmt.Sprintf(`{"deepseek_v4_official_compatibility":true,%q:true}`, conflict),
			}
			err := channel.ValidateSettings()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be enabled")
		}
	})

	t.Run("allows TNT conversion", func(t *testing.T) {
		channel := &Channel{
			Type:          constant.ChannelTypeAnthropic,
			OtherSettings: `{"deepseek_v4_official_compatibility":true,"tnt_tencent_openai_conversion":true}`,
		}
		require.NoError(t, channel.ValidateSettings())
	})
}

func TestChannelValidateSettingsRejectsConflictingInputTokenEstimationModes(t *testing.T) {
	channel := &Channel{
		OtherSettings: `{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"kimi_k3_mode":true}}`,
	}

	err := channel.ValidateSettings()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "input_token_routing")
}

func TestChannelMatchesInputTokenRouting(t *testing.T) {
	tests := []struct {
		name      string
		settings  string
		estimates *dto.InputTokenEstimates
		want      bool
	}{
		{
			name:      "nil estimate ignores routing",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimates: nil,
			want:      true,
		},
		{
			name:      "unconfigured channel remains eligible",
			settings:  `{}`,
			estimates: inputTokenEstimates(12000, 12000),
			want:      true,
		},
		{
			name:      "below or equal max matches",
			settings:  `{"input_token_routing":{"enabled":true,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(8000, 8000),
			want:      true,
		},
		{
			name:      "above max does not match",
			settings:  `{"input_token_routing":{"enabled":true,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(8001, 8001),
			want:      false,
		},
		{
			name:      "above or equal min matches",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimates: inputTokenEstimates(9000, 9000),
			want:      true,
		},
		{
			name:      "below min does not match",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":8001}}`,
			estimates: inputTokenEstimates(8000, 8000),
			want:      false,
		},
		{
			name:      "inside bounded range matches",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":4001,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(6000, 6000),
			want:      true,
		},
		{
			name:      "outside bounded range does not match",
			settings:  `{"input_token_routing":{"enabled":true,"min_tokens":4001,"max_tokens":8000}}`,
			estimates: inputTokenEstimates(9000, 9000),
			want:      false,
		},
		{
			name:      "inside any configured range matches",
			settings:  `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":0,"max_tokens":200},{"min_tokens":5000000,"max_tokens":1000000}]}}`,
			estimates: inputTokenEstimates(1500000, 1500000),
			want:      true,
		},
		{
			name:      "outside all configured ranges does not match",
			settings:  `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":0,"max_tokens":200},{"min_tokens":1000000,"max_tokens":5000000}]}}`,
			estimates: inputTokenEstimates(500000, 500000),
			want:      false,
		},
		{
			name:      "glm mode uses its own estimate",
			settings:  `{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"ranges":[{"min_tokens":200001,"max_tokens":500000}]}}`,
			estimates: inputTokenEstimates(520000, 350000),
			want:      true,
		},
		{
			name:      "default mode ignores glm estimate",
			settings:  `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":200001,"max_tokens":500000}]}}`,
			estimates: inputTokenEstimates(520000, 350000),
			want:      false,
		},
		{
			name:      "open ended range includes lower boundary",
			settings:  `{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"ranges":[{"min_tokens":500000,"max_tokens":0}]}}`,
			estimates: inputTokenEstimates(100, 500000),
			want:      true,
		},
		{
			name:      "kimi k3 mode uses its own estimate",
			settings:  `{"input_token_routing":{"enabled":true,"kimi_k3_mode":true,"ranges":[{"min_tokens":200001,"max_tokens":300000}]}}`,
			estimates: inputTokenEstimates(520000, 350000, 250000),
			want:      true,
		},
		{
			name:      "default mode ignores kimi k3 estimate",
			settings:  `{"input_token_routing":{"enabled":true,"ranges":[{"min_tokens":200001,"max_tokens":300000}]}}`,
			estimates: inputTokenEstimates(520000, 350000, 250000),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{OtherSettings: tt.settings}

			assert.Equal(t, tt.want, channel.MatchesInputTokenRouting(tt.estimates))
		})
	}
}

func inputTokenEstimates(defaultTokens int, glm52Tokens int, kimiK3Tokens ...int) *dto.InputTokenEstimates {
	kimiK3 := defaultTokens
	if len(kimiK3Tokens) > 0 {
		kimiK3 = kimiK3Tokens[0]
	}
	return &dto.InputTokenEstimates{Default: defaultTokens, GLM52: glm52Tokens, KimiK3: kimiK3}
}

func TestAdvancedCustomChannelRequiresModelListRouteOnlyWhenUpdateChecksEnabled(t *testing.T) {
	inferenceRoute := dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
		Converter:    "none",
	}

	tests := []struct {
		name          string
		checksEnabled bool
		routes        []dto.AdvancedCustomRoute
		wantErr       string
	}{
		{
			name:   "legacy channel without discovery route remains valid",
			routes: []dto.AdvancedCustomRoute{inferenceRoute},
		},
		{
			name:          "enabled checks require discovery route",
			checksEnabled: true,
			routes:        []dto.AdvancedCustomRoute{inferenceRoute},
			wantErr:       dto.AdvancedCustomModelListPath,
		},
		{
			name:          "enabled checks accept discovery route",
			checksEnabled: true,
			routes: []dto.AdvancedCustomRoute{
				inferenceRoute,
				{
					IncomingPath: dto.AdvancedCustomModelListPath,
					UpstreamPath: dto.AdvancedCustomModelListPath,
					Converter:    "none",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{Type: constant.ChannelTypeAdvancedCustom}
			channel.SetOtherSettings(dto.ChannelOtherSettings{
				UpstreamModelUpdateCheckEnabled: tt.checksEnabled,
				AdvancedCustom: &dto.AdvancedCustomConfig{
					Routes: tt.routes,
				},
			})

			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInferencePresetSettingsAndDatabaseRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "presets.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			table := db.Table("inference_preset_channels").Session(&gorm.Session{})
			require.NoError(t, table.AutoMigrate(&Channel{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("inference_preset_channels")) })
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("select sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("select version()").Scan(&version).Error)
			}
			t.Logf("%s version: %s", dialect, version)
			for _, channelType := range []int{constant.ChannelTypeVLLM, constant.ChannelTypeSGLang} {
				t.Run(fmt.Sprint(channelType), func(t *testing.T) {
					channel := &Channel{Type: channelType, Key: "EMPTY", Name: "inference", Status: common.ChannelStatusEnabled}
					require.NoError(t, channel.ValidateSettings())
					require.NotNil(t, channel.GetOtherSettings().AdvancedCustom)
					require.NoError(t, table.Create(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						assert.Equal(t, channelType, loaded.Type)
						assert.Empty(t, loaded.OtherSettings)
						defaults := loaded.GetOtherSettings().AdvancedCustom
						require.NotNil(t, defaults)
						assert.True(t, defaults.SupportsPath("/v1/messages"))
						assert.Empty(t, loaded.OtherSettings, "reading defaults must not rewrite saved settings")
						defaults.Routes[0].UpstreamPath = "/changed-locally"
						assert.Equal(t, "/v1/chat/completions", loaded.GetOtherSettings().AdvancedCustom.Routes[0].UpstreamPath)
					}
					settings := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/custom/chat", Models: []string{"allowed"}, Auth: &dto.AdvancedCustomRouteAuth{Type: "none"}}}}}
					channel.SetOtherSettings(settings)
					require.NoError(t, channel.ValidateSettings())
					require.NoError(t, table.Save(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						actual := loaded.GetOtherSettings().AdvancedCustom
						require.Equal(t, common.GetAdvancedCustomPreset(channelType), actual, "named channels must ignore editable advanced_custom overrides")
						assert.Equal(t, channel.OtherSettings, loaded.OtherSettings)
						for _, tc := range []struct {
							path, model string
							allowed     bool
						}{
							{"/v1/chat/completions", "allowed", true},
							{"/v1/chat/completions", "other", true},
							{"/v1/messages", "allowed", true},
							{"/v1/images/generations", "allowed", false},
						} {
							ok, _ := ChannelSatisfiesFilters(&loaded, tc.model, []filterdto.ChannelFilter{{Kind: filterdto.FilterRequestPath, RequestPath: tc.path}})
							assert.Equal(t, tc.allowed, ok)
						}
						endpoints := getPricingEndpointTypesForAbility(AbilityWithChannel{ChannelType: channelType, Ability: Ability{Model: "allowed", ChannelId: loaded.Id}}, map[int]*dto.AdvancedCustomConfig{loaded.Id: actual})
						expectedEndpoints := []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse, constant.EndpointTypeAnthropic, constant.EndpointTypeEmbeddings}
						if channelType == constant.ChannelTypeSGLang {
							expectedEndpoints = append(expectedEndpoints, constant.EndpointTypeJinaRerank)
						}
						assert.ElementsMatch(t, expectedEndpoints, endpoints)
					}
				})
			}
		})
	}
}
