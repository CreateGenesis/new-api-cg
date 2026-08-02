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

import type { Channel } from '../../types'
import {
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from '../channel-form'

function channelWithSettings(settings: string): Channel {
  return {
    id: 1,
    type: 1,
    key: '',
    status: 1,
    name: 'token routing channel',
    created_time: 0,
    test_time: 0,
    response_time: 0,
    other: '',
    balance: 0,
    balance_updated_time: 0,
    models: 'kimi-k3',
    group: 'default',
    used_quota: 0,
    other_info: '',
    setting: '{}',
    remark: '',
    max_input_tokens: 0,
    settings,
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
  }
}

describe('channel form input token routing estimation mode', () => {
  test('round trips Kimi K3 mode without enabling GLM-5.2 mode', () => {
    const form = transformChannelToFormDefaults(
      channelWithSettings(
        '{"input_token_routing":{"enabled":true,"kimi_k3_mode":true,"ranges":[{"min_tokens":5001,"max_tokens":10000}]}}'
      )
    )

    assert.equal(form.input_token_routing_estimation_mode, 'kimi_k3')

    const payload = transformFormDataToCreatePayload({
      ...form,
      key: 'sk-test',
      group: ['default'],
    })
    const settings = JSON.parse(String(payload.channel.settings))

    assert.deepEqual(settings.input_token_routing, {
      enabled: true,
      kimi_k3_mode: true,
      ranges: [{ min_tokens: 5001, max_tokens: 10000 }],
    })
  })

  test('prefers Kimi K3 when manually supplied JSON enables both legacy flags', () => {
    const form = transformChannelToFormDefaults(
      channelWithSettings(
        '{"input_token_routing":{"enabled":true,"glm_5_2_mode":true,"kimi_k3_mode":true,"ranges":[{"min_tokens":1,"max_tokens":10000}]}}'
      )
    )

    assert.equal(form.input_token_routing_estimation_mode, 'kimi_k3')
  })
})
