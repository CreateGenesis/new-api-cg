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
    assert.equal(form.input_token_routing_glm_5_2_mode, true)
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
