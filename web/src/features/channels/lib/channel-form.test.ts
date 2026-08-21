/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { Channel } from '../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from './channel-form'

function testChannel(settings: string): Channel {
  return {
    id: 1,
    type: 1,
    key: '',
    openai_organization: null,
    test_model: null,
    status: 1,
    name: 'test',
    weight: 0,
    created_time: 0,
    test_time: 0,
    response_time: 0,
    base_url: null,
    other: '',
    balance: 0,
    balance_updated_time: 0,
    models: 'test-model',
    group: 'default',
    used_quota: 0,
    model_mapping: null,
    status_code_mapping: null,
    priority: 0,
    auto_ban: 1,
    other_info: '',
    tag: null,
    setting: '{}',
    param_override: null,
    header_override: null,
    remark: '',
    max_input_tokens: 0,
    channel_info: {
      is_multi_key: false,
      multi_key_size: 0,
      multi_key_polling_index: 0,
      multi_key_mode: 'random',
      multi_key_affinity_ttl_seconds: 3600,
      multi_key_least_requests_window_seconds: 60,
      multi_key_cache_affinity_threshold_percent: 35,
      channel_overload_protection: {
        enabled: false,
        requests_per_second: 0,
        requests_per_minute: 0,
        tokens_per_minute: 0,
        concurrent_requests: 0,
        recovery_seconds: 2,
      },
      multi_key_overload_protection: {
        enabled: false,
        requests_per_second: 0,
        requests_per_minute: 0,
        tokens_per_minute: 0,
        concurrent_requests: 0,
        recovery_seconds: 2,
      },
    },
    settings,
  }
}

describe('channel proxy fallback settings', () => {
  test('loads and saves direct fallback with the channel proxy', () => {
    const channel = testChannel('{}')
    channel.setting = JSON.stringify({
      proxy: 'socks5://127.0.0.1:1080',
      proxy_fallback_direct: true,
    })

    const form = transformChannelToFormDefaults(channel)
    assert.equal(form.proxy, 'socks5://127.0.0.1:1080')
    assert.equal(form.proxy_fallback_direct, true)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const setting = JSON.parse(String(payload.channel.setting))

    assert.equal(setting.proxy, 'socks5://127.0.0.1:1080')
    assert.equal(setting.proxy_fallback_direct, true)
  })

  test('loads and saves newline-separated proxy addresses', () => {
    const channel = testChannel('{}')
    channel.setting = JSON.stringify({
      proxy: 'socks5://127.0.0.1:1080\nsocks5h://127.0.0.2:1080',
    })

    const form = transformChannelToFormDefaults(channel)
    assert.equal(
      form.proxy,
      'socks5://127.0.0.1:1080\nsocks5h://127.0.0.2:1080'
    )

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const setting = JSON.parse(String(payload.channel.setting))
    assert.equal(
      setting.proxy,
      'socks5://127.0.0.1:1080\nsocks5h://127.0.0.2:1080'
    )
  })
})

describe('channel form status code retry settings', () => {
  test('saves retry interval milliseconds into settings JSON', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      status_code_retry_enabled: true,
      status_code_retry_times: 20,
      status_code_retry_interval_ms: 75,
      status_code_retry_status_codes: '500-503',
    })

    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.status_code_retry.enabled, true)
    assert.equal(settings.status_code_retry.retry_times, 20)
    assert.equal(settings.status_code_retry.retry_interval_ms, 75)
    assert.equal(settings.status_code_retry.status_codes, '500-503')
  })

  test('uses 50 ms when existing status code retry has no interval', () => {
    const form = transformChannelToFormDefaults(
      testChannel(
        '{"status_code_retry":{"enabled":true,"retry_times":3,"status_codes":"429"}}'
      )
    )

    assert.equal(form.status_code_retry_enabled, true)
    assert.equal(form.status_code_retry_interval_ms, 50)
  })
})

describe('channel form response header timeout settings', () => {
  test('enables the 180 second policy for new channels', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(
      CHANNEL_FORM_DEFAULT_VALUES.response_header_timeout_enabled,
      true
    )
    assert.deepEqual(settings.response_header_timeout, {
      enabled: true,
      timeout_seconds: 180,
    })
  })

  test('treats a missing saved setting as enabled', () => {
    const form = transformChannelToFormDefaults(testChannel('{}'))

    assert.equal(form.response_header_timeout_enabled, true)
    assert.equal(form.response_header_timeout_seconds, 180)
  })

  test('persists an explicit disabled setting', () => {
    const form = transformChannelToFormDefaults(
      testChannel('{"response_header_timeout":{"enabled":false}}')
    )
    assert.equal(form.response_header_timeout_enabled, false)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.deepEqual(settings.response_header_timeout, { enabled: false })
  })
})

describe('channel fallback policy settings', () => {
  test('loads and saves fallback switches and usage token limits', () => {
    const form = transformChannelToFormDefaults(
      testChannel(
        '{"retry_zero_output":true,"disable_stream":true,"usage_token_limit":{"input_tokens":1000000,"output_tokens":200000}}'
      )
    )

    assert.equal(form.retry_zero_output, true)
    assert.equal(form.disable_stream, true)
    assert.equal(form.usage_token_limit_input_tokens, 1000000)
    assert.equal(form.usage_token_limit_output_tokens, 200000)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.retry_zero_output, true)
    assert.equal(settings.disable_stream, true)
    assert.deepEqual(settings.usage_token_limit, {
      input_tokens: 1000000,
      output_tokens: 200000,
    })
  })

  test('drops legacy estimation settings when saving', () => {
    const form = transformChannelToFormDefaults(
      testChannel(
        '{"retry_zero_billed_output":true,"missing_output_token_multiplier":2}'
      )
    )
    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.retry_zero_billed_output, undefined)
    assert.equal(settings.missing_output_token_multiplier, undefined)
  })

  test('saves one usage token limit independently', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      usage_token_limit_input_tokens: 0,
      usage_token_limit_output_tokens: 500,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.deepEqual(settings.usage_token_limit, {
      input_tokens: 0,
      output_tokens: 500,
    })
  })

  test('loads and saves the non-stream request policy independently', () => {
    const form = transformChannelToFormDefaults(
      testChannel('{"disable_non_stream":true}')
    )

    assert.equal(form.disable_stream, false)
    assert.equal(form.disable_non_stream, true)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.disable_stream, undefined)
    assert.equal(settings.disable_non_stream, true)
  })

  test('omits disabled fallback defaults and rejects invalid token limits', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.retry_zero_output, undefined)
    assert.equal(settings.disable_stream, undefined)
    assert.equal(settings.disable_non_stream, undefined)
    assert.equal(settings.usage_token_limit, undefined)

    for (const tokenLimit of [-1, 1.5, 2147483648, Number.POSITIVE_INFINITY]) {
      const parsed = channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        usage_token_limit_input_tokens: tokenLimit,
      })
      assert.equal(parsed.success, false)
    }
  })

  test('rejects disabling stream and non-stream requests together', () => {
    const parsed = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      disable_stream: true,
      disable_non_stream: true,
    })

    assert.equal(parsed.success, false)
    if (parsed.success) return
    const paths = new Set(parsed.error.issues.map((issue) => issue.path[0]))
    assert.equal(paths.has('disable_stream'), true)
    assert.equal(paths.has('disable_non_stream'), true)
  })
})

describe('channel form stream interruption billing settings', () => {
  test('loads and saves input-only interruption billing', () => {
    const form = transformChannelToFormDefaults(
      testChannel('{"stream_interruption_billing":{"mode":"input_only_free"}}')
    )

    assert.equal(form.stream_interruption_billing_mode, 'input_only_free')

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.deepEqual(settings.stream_interruption_billing, {
      mode: 'input_only_free',
    })
  })

  test('removes interruption billing settings when disabled', () => {
    const form = transformChannelToFormDefaults(
      testChannel(
        '{"stream_interruption_billing":{"mode":"all_interrupted_free"}}'
      )
    )

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      stream_interruption_billing_mode: 'off',
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.stream_interruption_billing, undefined)
  })
})

describe('channel form input token routing settings', () => {
  test('round trips GLM-5.2 mode and an open-ended range', () => {
    const form = transformChannelToFormDefaults(
      testChannel(
        '{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"ranges":[{"min_tokens":50000,"max_tokens":0}]}}'
      )
    )

    assert.equal(form.input_token_routing_enabled, true)
    assert.equal(form.input_token_routing_estimation_mode, 'glm_5_2')
    assert.equal(form.input_token_routing_ranges, '50000-')

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.deepEqual(settings.input_token_routing, {
      enabled: true,
      glm_5_2_mode: true,
      ranges: [{ min_tokens: 50000, max_tokens: 0 }],
    })
  })
})

describe('channel form DeepSeek V4 request sanitization', () => {
  test('loads and saves the setting for DeepSeek channels', () => {
    const channel = testChannel('{"deepseek_v4_request_sanitization":true}')
    channel.type = 43
    const form = transformChannelToFormDefaults(channel)

    assert.equal(form.deepseek_v4_request_sanitization_enabled, true)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'deepseek-v4-pro',
      group: ['default'],
      status: 1,
      type: 43,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.deepseek_v4_request_sanitization, true)
  })

  test('removes the setting after changing to a non-DeepSeek channel', () => {
    const channel = testChannel('{"deepseek_v4_request_sanitization":true}')
    channel.type = 43
    const form = transformChannelToFormDefaults(channel)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'gpt-4.1',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.deepseek_v4_request_sanitization, undefined)
  })
})

describe('channel form simulated model cache settings', () => {
  test('loads enabled simulated cache settings', () => {
    const form = transformChannelToFormDefaults(
      testChannel('{"simulated_model_cache":{"enabled":true}}')
    )

    assert.equal(form.simulated_model_cache_enabled, true)
  })

  test('drops legacy exact replay settings when simulated cache is disabled', () => {
    const form = transformChannelToFormDefaults(
      testChannel(
        '{"simulated_model_cache":{"enabled":false,"exact_replay_enabled":true,"reuse_limit":8}}'
      )
    )
    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })

    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.simulated_model_cache, undefined)
  })

  test('saves fingerprint simulated cache settings only', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      simulated_model_cache_enabled: true,
      simulated_model_cache_ttl_seconds: 120,
      simulated_model_cache_min_match_ratio: 0.25,
    })

    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.simulated_model_cache.enabled, true)
    assert.equal(settings.simulated_model_cache.ttl_seconds, 120)
    assert.equal(settings.simulated_model_cache.min_match_ratio, 0.25)
    assert.equal(settings.simulated_model_cache.exact_replay_enabled, undefined)
    assert.equal(settings.simulated_model_cache.reuse_limit, undefined)
  })

  test('keeps hidden simulated cache settings for cache-aware routing', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'key-a\nkey-b',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      multi_key_mode: 'multi_to_single',
      multi_key_type: 'cache_affinity_least_requests',
      simulated_model_cache_enabled: false,
      simulated_model_cache_ttl_seconds: 120,
      simulated_model_cache_min_match_ratio: 0.25,
    })

    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.simulated_model_cache.enabled, false)
    assert.equal(settings.simulated_model_cache.ttl_seconds, 120)
    assert.equal(settings.simulated_model_cache.min_match_ratio, 0.25)
  })
})

describe('channel form cache validation split setting', () => {
  test('loads and saves the enabled channel setting', () => {
    const form = transformChannelToFormDefaults(
      testChannel('{"cache_usage_validation_split":true}')
    )
    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(form.cache_usage_validation_split, true)
    assert.equal(settings.cache_usage_validation_split, true)
  })

  test('keeps the setting disabled and removes stale JSON by default', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      settings: '{"cache_usage_validation_split":true}',
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(
      CHANNEL_FORM_DEFAULT_VALUES.cache_usage_validation_split,
      false
    )
    assert.equal(settings.cache_usage_validation_split, undefined)
  })
})

describe('channel form multi-key affinity settings', () => {
  test('saves affinity strategy and ttl for multi-key create payload', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'key-a\nkey-b',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      multi_key_mode: 'multi_to_single',
      multi_key_type: 'affinity',
      multi_key_affinity_ttl_seconds: 7200,
    })

    assert.equal(payload.multi_key_mode, 'affinity')
    assert.equal(payload.multi_key_affinity_ttl_seconds, 7200)
  })

  test('loads affinity ttl from existing multi-key channel', () => {
    const channel = testChannel('{}')
    channel.channel_info.is_multi_key = true
    channel.channel_info.multi_key_mode = 'affinity'
    channel.channel_info.multi_key_affinity_ttl_seconds = 1800

    const form = transformChannelToFormDefaults(channel)

    assert.equal(form.multi_key_type, 'affinity')
    assert.equal(form.multi_key_affinity_ttl_seconds, 1800)
  })
})

describe('channel form least requests settings', () => {
  test('saves least requests strategy and window for multi-key create payload', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'key-a\nkey-b',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      multi_key_mode: 'multi_to_single' as const,
      multi_key_type: 'least_requests' as const,
      multi_key_least_requests_window_seconds: 120,
    }

    const parsed = channelFormSchema.safeParse(form)
    assert.equal(parsed.success, true)
    const payload = transformFormDataToCreatePayload(form)
    assert.equal(payload.multi_key_mode, 'least_requests')
    assert.equal(payload.multi_key_least_requests_window_seconds, 120)
  })

  test('loads least requests window from existing multi-key channel', () => {
    const channel = testChannel('{}')
    channel.channel_info.is_multi_key = true
    channel.channel_info.multi_key_mode = 'least_requests'
    channel.channel_info.multi_key_least_requests_window_seconds = 180

    const form = transformChannelToFormDefaults(channel)

    assert.equal(form.multi_key_type, 'least_requests')
    assert.equal(form.multi_key_least_requests_window_seconds, 180)
  })

  test('rejects invalid least requests windows', () => {
    for (const windowSeconds of [9, 15, 3610]) {
      const parsed = channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        multi_key_type: 'least_requests',
        multi_key_least_requests_window_seconds: windowSeconds,
      })

      assert.equal(parsed.success, false)
      if (!parsed.success) {
        assert.equal(
          parsed.error.issues.some(
            (issue) =>
              issue.path[0] === 'multi_key_least_requests_window_seconds'
          ),
          true
        )
      }
    }
  })

  test('saves cache-aware strategy, window, and threshold', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'key-a\nkey-b',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      multi_key_mode: 'multi_to_single' as const,
      multi_key_type: 'cache_affinity_least_requests' as const,
      multi_key_least_requests_window_seconds: 120,
      multi_key_cache_affinity_threshold_percent: 35,
    }

    const parsed = channelFormSchema.safeParse(form)
    assert.equal(parsed.success, true)
    const payload = transformFormDataToCreatePayload(form)
    assert.equal(payload.multi_key_mode, 'cache_affinity_least_requests')
    assert.equal(payload.multi_key_least_requests_window_seconds, 120)
    assert.equal(payload.multi_key_cache_affinity_threshold_percent, 35)
  })

  test('rejects invalid cache-aware thresholds', () => {
    for (const threshold of [-1, 1.5, 101]) {
      const parsed = channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        multi_key_type: 'cache_affinity_least_requests',
        multi_key_cache_affinity_threshold_percent: threshold,
      })

      assert.equal(parsed.success, false)
      if (!parsed.success) {
        assert.equal(
          parsed.error.issues.some(
            (issue) =>
              issue.path[0] === 'multi_key_cache_affinity_threshold_percent'
          ),
          true
        )
      }
    }
  })
})

describe('channel form overload protection', () => {
  test('saves channel and multi-key overload settings', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'key-a\nkey-b',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      multi_key_mode: 'multi_to_single',
      channel_overload_enabled: true,
      channel_overload_requests_per_second: 5,
      channel_overload_recovery_seconds: 7,
      multi_key_overload_enabled: true,
      multi_key_overload_tokens_per_minute: 100000,
      multi_key_overload_concurrent_requests: 2,
      multi_key_overload_recovery_seconds: 3,
    })

    assert.deepEqual(
      payload.channel.channel_info?.channel_overload_protection,
      {
        enabled: true,
        requests_per_second: 5,
        requests_per_minute: 0,
        tokens_per_minute: 0,
        concurrent_requests: 0,
        recovery_seconds: 7,
      }
    )
    assert.deepEqual(
      payload.channel.channel_info?.multi_key_overload_protection,
      {
        enabled: true,
        requests_per_second: 0,
        requests_per_minute: 0,
        tokens_per_minute: 100000,
        concurrent_requests: 2,
        recovery_seconds: 3,
      }
    )
  })

  test('loads existing overload settings', () => {
    const channel = testChannel('{}')
    channel.channel_info.is_multi_key = true
    channel.channel_info.channel_overload_protection = {
      enabled: true,
      requests_per_second: 3,
      requests_per_minute: 20,
      tokens_per_minute: 0,
      concurrent_requests: 4,
      recovery_seconds: 6,
    }
    channel.channel_info.multi_key_overload_protection = {
      enabled: true,
      requests_per_second: 1,
      requests_per_minute: 10,
      tokens_per_minute: 90000,
      concurrent_requests: 2,
      recovery_seconds: 4,
    }

    const form = transformChannelToFormDefaults(channel)
    assert.equal(form.channel_overload_requests_per_minute, 20)
    assert.equal(form.multi_key_overload_concurrent_requests, 2)
    assert.equal(form.multi_key_overload_tokens_per_minute, 90000)
    assert.equal(form.channel_overload_recovery_seconds, 6)
    assert.equal(form.multi_key_overload_recovery_seconds, 4)
  })

  test('rejects enabled overload protection with all thresholds zero', () => {
    const parsed = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      channel_overload_enabled: true,
    })
    assert.equal(parsed.success, false)
  })

  test('allows key overload protection with only TPM configured', () => {
    const parsed = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'key-a\nkey-b',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      multi_key_overload_enabled: true,
      multi_key_overload_tokens_per_minute: 1000,
    })
    assert.equal(parsed.success, true)
  })

  test('rejects invalid recovery time and unsafe TPM values', () => {
    const parsed = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'key-a\nkey-b',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      multi_key_overload_enabled: true,
      multi_key_overload_tokens_per_minute: Number.MAX_SAFE_INTEGER + 1,
      multi_key_overload_recovery_seconds: 86401,
    })
    assert.equal(parsed.success, false)
  })
})

describe('TNT Tencent-style OpenAI conversion settings', () => {
  test('loads and saves the setting only for Anthropic channels', () => {
    const channel = testChannel(
      '{"tnt_tencent_openai_conversion":true,"retry_zero_output":true}'
    )
    channel.type = 14

    const form = transformChannelToFormDefaults(channel)
    assert.equal(form.tnt_tencent_openai_conversion, true)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'tnt',
      key: 'sk-test',
      models: 'kimi-k3',
      group: ['default'],
      status: 1,
      type: 14,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.tnt_tencent_openai_conversion, true)
    assert.equal(settings.retry_zero_output, true)
  })

  test('removes the setting after changing to another channel type', () => {
    const channel = testChannel(
      '{"tnt_tencent_openai_conversion":true,"retry_zero_output":true}'
    )
    channel.type = 14
    const form = transformChannelToFormDefaults(channel)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'other',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.tnt_tencent_openai_conversion, undefined)
    assert.equal(settings.retry_zero_output, true)
  })

  test('rejects request body passthrough on an enabled TNT channel', () => {
    const parsed = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'tnt',
      key: 'sk-test',
      models: 'kimi-k3',
      group: ['default'],
      status: 1,
      type: 14,
      pass_through_body_enabled: true,
      tnt_tencent_openai_conversion: true,
    })

    assert.equal(parsed.success, false)
    if (!parsed.success) {
      const paths = new Set(
        parsed.error.issues.map((issue) => issue.path.join('.'))
      )
      assert.ok(paths.has('pass_through_body_enabled'))
      assert.ok(paths.has('tnt_tencent_openai_conversion'))
    }
  })
})

describe('Anthropic input cache usage conversion settings', () => {
  test('loads and saves the setting for Anthropic channels', () => {
    const channel = testChannel(
      '{"anthropic_input_includes_cache":true,"tnt_tencent_openai_conversion":true}'
    )
    channel.type = 14

    const form = transformChannelToFormDefaults(channel)
    assert.equal(form.anthropic_input_includes_cache, true)
    assert.equal(form.tnt_tencent_openai_conversion, true)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'anthropic-cache',
      key: 'sk-test',
      models: 'claude-test',
      group: ['default'],
      status: 1,
      type: 14,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.anthropic_input_includes_cache, true)
    assert.equal(settings.tnt_tencent_openai_conversion, true)
  })

  test('removes the setting after changing to another channel type', () => {
    const channel = testChannel(
      '{"anthropic_input_includes_cache":true,"retry_zero_output":true}'
    )
    channel.type = 14
    const form = transformChannelToFormDefaults(channel)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'other',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.anthropic_input_includes_cache, undefined)
    assert.equal(settings.retry_zero_output, true)
  })

  test('rejects request body passthrough', () => {
    const parsed = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'anthropic-cache',
      key: 'sk-test',
      models: 'claude-test',
      group: ['default'],
      status: 1,
      type: 14,
      pass_through_body_enabled: true,
      anthropic_input_includes_cache: true,
    })

    assert.equal(parsed.success, false)
    if (!parsed.success) {
      const paths = new Set(
        parsed.error.issues.map((issue) => issue.path.join('.'))
      )
      assert.ok(paths.has('pass_through_body_enabled'))
      assert.ok(paths.has('anthropic_input_includes_cache'))
    }
  })
})

describe('Kimi K3 official compatibility settings', () => {
  test('loads and saves the setting for supported channel types', () => {
    for (const type of [1, 14, 25]) {
      const channel = testChannel('{"kimi_k3_official_compatibility":true}')
      channel.type = type

      const form = transformChannelToFormDefaults(channel)
      assert.equal(form.kimi_k3_official_compatibility, true)

      const payload = transformFormDataToCreatePayload({
        ...form,
        name: 'kimi-k3',
        key: 'sk-test',
        models: 'kimi-k3',
        group: ['default'],
        status: 1,
        type,
      })
      const settings = JSON.parse(String(payload.channel.settings))
      assert.equal(settings.kimi_k3_official_compatibility, true)
    }
  })

  test('removes the setting after changing to an unsupported channel type', () => {
    const channel = testChannel('{"kimi_k3_official_compatibility":true}')
    channel.type = 14
    const form = transformChannelToFormDefaults(channel)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'other',
      key: 'sk-test',
      models: 'kimi-k3',
      group: ['default'],
      status: 1,
      type: 15,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.kimi_k3_official_compatibility, undefined)
  })

  test('rejects request body passthrough but allows TNT conversion', () => {
    const conflict = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'kimi-k3',
      key: 'sk-test',
      models: 'kimi-k3',
      group: ['default'],
      status: 1,
      type: 14,
      pass_through_body_enabled: true,
      kimi_k3_official_compatibility: true,
    })
    assert.equal(conflict.success, false)

    const coexistence = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'kimi-k3',
      key: 'sk-test',
      models: 'kimi-k3',
      group: ['default'],
      status: 1,
      type: 14,
      tnt_tencent_openai_conversion: true,
      kimi_k3_official_compatibility: true,
    })
    assert.equal(coexistence.success, true)
  })
})

describe('GLM 5.3 official compatibility settings', () => {
  test('loads and saves the setting for OpenAI and Anthropic channels', () => {
    for (const type of [1, 14]) {
      const channel = testChannel('{"glm_5_3_official_compatibility":true}')
      channel.type = type

      const form = transformChannelToFormDefaults(channel)
      assert.equal(form.glm_5_3_official_compatibility, true)

      const payload = transformFormDataToCreatePayload({
        ...form,
        name: 'glm-5.3',
        key: 'sk-test',
        models: 'mapped-model',
        group: ['default'],
        status: 1,
        type,
      })
      const settings = JSON.parse(String(payload.channel.settings))
      assert.equal(settings.glm_5_3_official_compatibility, true)
    }
  })

  test('removes the setting after changing to an unsupported channel type', () => {
    const channel = testChannel('{"glm_5_3_official_compatibility":true}')
    channel.type = 14
    const form = transformChannelToFormDefaults(channel)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'other',
      key: 'sk-test',
      models: 'mapped-model',
      group: ['default'],
      status: 1,
      type: 25,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.glm_5_3_official_compatibility, undefined)
  })

  test('rejects passthrough and Kimi but allows TNT conversion', () => {
    const passthroughConflict = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'glm-5.3',
      key: 'sk-test',
      models: 'mapped-model',
      group: ['default'],
      status: 1,
      type: 14,
      pass_through_body_enabled: true,
      glm_5_3_official_compatibility: true,
    })
    assert.equal(passthroughConflict.success, false)

    const kimiConflict = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'glm-5.3',
      key: 'sk-test',
      models: 'mapped-model',
      group: ['default'],
      status: 1,
      type: 14,
      kimi_k3_official_compatibility: true,
      glm_5_3_official_compatibility: true,
    })
    assert.equal(kimiConflict.success, false)

    const tntCoexistence = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'glm-5.3',
      key: 'sk-test',
      models: 'mapped-model',
      group: ['default'],
      status: 1,
      type: 14,
      tnt_tencent_openai_conversion: true,
      glm_5_3_official_compatibility: true,
    })
    assert.equal(tntCoexistence.success, true)
  })
})

describe('DeepSeek V4 official compatibility settings', () => {
  test('loads and saves the setting for supported channels', () => {
    for (const type of [1, 14, 43]) {
      const channel = testChannel('{"deepseek_v4_official_compatibility":true}')
      channel.type = type

      const form = transformChannelToFormDefaults(channel)
      assert.equal(form.deepseek_v4_official_compatibility, true)

      const payload = transformFormDataToCreatePayload({
        ...form,
        name: 'deepseek-v4',
        key: 'sk-test',
        models: 'deepseek-v4-flash-0731',
        group: ['default'],
        status: 1,
        type,
      })
      const settings = JSON.parse(String(payload.channel.settings))
      assert.equal(settings.deepseek_v4_official_compatibility, true)
    }
  })

  test('removes the setting for unsupported channel types', () => {
    const channel = testChannel('{"deepseek_v4_official_compatibility":true}')
    channel.type = 14
    const form = transformChannelToFormDefaults(channel)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'other',
      key: 'sk-test',
      models: 'deepseek-v4-flash-0731',
      group: ['default'],
      status: 1,
      type: 25,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.deepseek_v4_official_compatibility, undefined)
  })

  test('rejects passthrough and other official compatibility modes but allows TNT', () => {
    const passthroughConflict = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'deepseek-v4',
      key: 'sk-test',
      models: 'deepseek-v4-flash-0731',
      group: ['default'],
      status: 1,
      type: 14,
      pass_through_body_enabled: true,
      deepseek_v4_official_compatibility: true,
    })
    assert.equal(passthroughConflict.success, false)

    for (const conflict of [
      'kimi_k3_official_compatibility',
      'glm_5_3_official_compatibility',
    ] as const) {
      const result = channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        name: 'deepseek-v4',
        key: 'sk-test',
        models: 'deepseek-v4-flash-0731',
        group: ['default'],
        status: 1,
        type: 14,
        deepseek_v4_official_compatibility: true,
        [conflict]: true,
      })
      assert.equal(result.success, false)
    }

    const tntCoexistence = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'deepseek-v4',
      key: 'sk-test',
      models: 'deepseek-v4-flash-0731',
      group: ['default'],
      status: 1,
      type: 14,
      tnt_tencent_openai_conversion: true,
      deepseek_v4_official_compatibility: true,
    })
    assert.equal(tntCoexistence.success, true)
  })
})

describe('usage estimation settings', () => {
  test('loads and saves explicit model family and direction multipliers', () => {
    const channel = testChannel(
      '{"usage_estimation":{"enabled":true,"model_family":"deepseek","input_multiplier":1.25,"output_multiplier":1.5}}'
    )
    const form = transformChannelToFormDefaults(channel)

    assert.equal(form.usage_estimation_enabled, true)
    assert.equal(form.usage_estimation_model_family, 'deepseek')
    assert.equal(form.usage_estimation_input_multiplier, 1.25)
    assert.equal(form.usage_estimation_output_multiplier, 1.5)

    const payload = transformFormDataToCreatePayload({
      ...form,
      name: 'usage-estimation',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.deepEqual(settings.usage_estimation, {
      enabled: true,
      model_family: 'deepseek',
      input_multiplier: 1.25,
      output_multiplier: 1.5,
    })
  })

  test('omits usage estimation when disabled and validates multiplier bounds', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'disabled',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
    })
    const settings = JSON.parse(String(payload.channel.settings))
    assert.equal(settings.usage_estimation, undefined)

    const invalid = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'invalid',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      usage_estimation_enabled: true,
      usage_estimation_input_multiplier: 0.001,
    })
    assert.equal(invalid.success, false)
  })
})
