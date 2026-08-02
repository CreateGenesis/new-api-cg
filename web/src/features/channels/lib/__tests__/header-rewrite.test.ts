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
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from '../channel-form'

function channelWithSetting(setting: string): Channel {
  return {
    id: 1,
    type: 1,
    key: '',
    status: 1,
    name: 'header rewrite channel',
    created_time: 0,
    test_time: 0,
    response_time: 0,
    other: '',
    balance: 0,
    balance_updated_time: 0,
    models: 'gpt-test',
    group: 'default',
    used_quota: 0,
    other_info: '',
    setting,
    remark: '',
    max_input_tokens: 0,
    settings: '{}',
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

describe('channel header rewrite form', () => {
  test('round trips a live preset reference and channel JSON overrides', () => {
    const form = transformChannelToFormDefaults(
      channelWithSetting(
        JSON.stringify({
          header_rewrite: {
            preset_id: 'codex',
            remove: ['X-Custom-Source-*'],
            set: { 'User-Agent': 'codex-cli/custom' },
          },
        })
      )
    )

    assert.equal(form.header_rewrite_preset_id, 'codex')
    assert.deepEqual(JSON.parse(form.header_rewrite || '{}'), {
      remove: ['X-Custom-Source-*'],
      set: { 'User-Agent': 'codex-cli/custom' },
    })

    const payload = transformFormDataToCreatePayload({
      ...form,
      key: 'sk-test',
      group: ['default'],
    })
    const setting = JSON.parse(String(payload.channel.setting))
    assert.deepEqual(setting.header_rewrite, {
      preset_id: 'codex',
      remove: ['X-Custom-Source-*'],
      set: { 'User-Agent': 'codex-cli/custom' },
    })
  })

  test('rejects preset_id inside the channel override JSON field', () => {
    const result = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      header_rewrite: '{"preset_id":"codex"}',
    })

    assert.equal(result.success, false)
  })
})
