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
    },
    settings,
  }
}

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

describe('channel form simulated model cache settings', () => {
  test('loads legacy enabled cache as exact replay and simulated cache enabled', () => {
    const form = transformChannelToFormDefaults(
      testChannel('{"simulated_model_cache":{"enabled":true}}')
    )

    assert.equal(form.simulated_model_cache_enabled, true)
    assert.equal(form.simulated_model_cache_exact_replay_enabled, true)
  })

  test('saves exact replay without partial simulated cache', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      simulated_model_cache_enabled: false,
      simulated_model_cache_exact_replay_enabled: true,
      simulated_model_cache_ttl_seconds: 120,
      simulated_model_cache_reuse_limit: 8,
    })

    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.simulated_model_cache.enabled, false)
    assert.equal(settings.simulated_model_cache.exact_replay_enabled, true)
    assert.equal(settings.simulated_model_cache.ttl_seconds, 120)
    assert.equal(settings.simulated_model_cache.reuse_limit, 8)
    assert.equal(settings.simulated_model_cache.min_match_ratio, undefined)
  })

  test('saves partial simulated cache with exact replay disabled', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'sk-test',
      models: 'test-model',
      group: ['default'],
      status: 1,
      type: 1,
      simulated_model_cache_enabled: true,
      simulated_model_cache_exact_replay_enabled: false,
      simulated_model_cache_min_match_ratio: 0.25,
    })

    const settings = JSON.parse(String(payload.channel.settings))

    assert.equal(settings.simulated_model_cache.enabled, true)
    assert.equal(settings.simulated_model_cache.exact_replay_enabled, false)
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
