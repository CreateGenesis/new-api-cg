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
  type ChannelFormValues,
} from '../channel-form'

function validChannelForm(
  values: Partial<ChannelFormValues> = {}
): ChannelFormValues {
  return {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'response-header-timeout',
    key: 'sk-test',
    models: 'gpt-test',
    group: ['default'],
    status: 1,
    type: 1,
    ...values,
  }
}

function channelWithSettings(settings: string): Channel {
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
    models: 'gpt-test',
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

describe('response header timeout channel settings', () => {
  test('defaults to enabled with a 180 second value', () => {
    assert.equal(
      CHANNEL_FORM_DEFAULT_VALUES.response_header_timeout_enabled,
      true
    )
    assert.equal(
      CHANNEL_FORM_DEFAULT_VALUES.response_header_timeout_seconds,
      180
    )
  })

  test('loads and saves an enabled custom timeout', () => {
    const form = transformChannelToFormDefaults(
      channelWithSettings(
        '{"response_header_timeout":{"enabled":true,"timeout_seconds":180}}'
      )
    )

    assert.equal(form.response_header_timeout_enabled, true)
    assert.equal(form.response_header_timeout_seconds, 180)

    const payload = transformFormDataToCreatePayload(validChannelForm(form))
    const settings = JSON.parse(String(payload.channel.settings))
    assert.deepEqual(settings.response_header_timeout, {
      enabled: true,
      timeout_seconds: 180,
    })
  })

  test('persists the nested setting as explicitly disabled', () => {
    const payload = transformFormDataToCreatePayload(
      validChannelForm({
        settings:
          '{"response_header_timeout":{"enabled":true,"timeout_seconds":180}}',
        response_header_timeout_enabled: false,
      })
    )
    const settings = JSON.parse(String(payload.channel.settings))

    assert.deepEqual(settings.response_header_timeout, { enabled: false })
  })

  test('rejects enabled timeout outside the supported integer range', () => {
    for (const timeoutSeconds of [0, 1.5, 86401]) {
      const result = channelFormSchema.safeParse(
        validChannelForm({
          response_header_timeout_enabled: true,
          response_header_timeout_seconds: timeoutSeconds,
        })
      )

      assert.equal(result.success, false)
      if (!result.success) {
        assert.equal(
          result.error.issues.some(
            (issue) => issue.path[0] === 'response_header_timeout_seconds'
          ),
          true
        )
      }
    }
  })
})
