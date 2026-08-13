/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

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

const multimodalValues = {
  simulated_model_cache_image_tokens_per_megapixel: 520,
  simulated_model_cache_video_tokens_per_second_megapixel: 820,
  simulated_model_cache_audio_tokens_per_second: 25,
  simulated_model_cache_file_tokens_per_mib: 4096,
  simulated_model_cache_image_fallback_tokens: 520,
  simulated_model_cache_video_fallback_tokens: 8192,
  simulated_model_cache_audio_fallback_tokens: 256,
  simulated_model_cache_file_fallback_tokens: 4096,
} satisfies Partial<ChannelFormValues>

function validChannelForm(
  values: Partial<ChannelFormValues> = {}
): ChannelFormValues {
  return {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'multimedia-cache',
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

describe('simulated model cache multimedia form settings', () => {
  test('loads and saves all nested multimedia settings', () => {
    const nestedSettings = {
      enabled: true,
      ttl_seconds: 120,
      min_match_ratio: 0.25,
      multimodal: {
        enabled: true,
        image_tokens_per_megapixel: 520,
        video_tokens_per_second_megapixel: 820,
        audio_tokens_per_second: 25,
        file_tokens_per_mib: 4096,
        image_fallback_tokens: 520,
        video_fallback_tokens: 8192,
        audio_fallback_tokens: 256,
        file_fallback_tokens: 4096,
      },
    }
    const form = transformChannelToFormDefaults(
      channelWithSettings(
        JSON.stringify({ simulated_model_cache: nestedSettings })
      )
    )

    assert.equal(form.simulated_model_cache_multimodal_enabled, true)
    for (const [field, value] of Object.entries(multimodalValues)) {
      assert.equal(form[field as keyof ChannelFormValues], value)
    }

    const payload = transformFormDataToCreatePayload(validChannelForm(form))
    const saved = JSON.parse(String(payload.channel.settings))
    assert.deepEqual(saved.simulated_model_cache, nestedSettings)
  })

  test('requires every rate and fallback when multimedia matching is enabled', () => {
    const result = channelFormSchema.safeParse(
      validChannelForm({
        simulated_model_cache_enabled: true,
        simulated_model_cache_multimodal_enabled: true,
      })
    )

    assert.equal(result.success, false)
    if (result.success) return
    const paths = new Set(result.error.issues.map((issue) => issue.path[0]))
    for (const field of Object.keys(multimodalValues)) {
      assert.equal(paths.has(field), true, `${field} should be required`)
    }
  })

  test('rejects non-finite, out-of-range, and fractional multimedia values', () => {
    const cases: Array<[keyof typeof multimodalValues, number]> = [
      ['simulated_model_cache_image_tokens_per_megapixel', Number.NaN],
      [
        'simulated_model_cache_audio_tokens_per_second',
        Number.POSITIVE_INFINITY,
      ],
      ['simulated_model_cache_file_tokens_per_mib', 0.0000009],
      ['simulated_model_cache_video_tokens_per_second_megapixel', 1_000_001],
      ['simulated_model_cache_image_fallback_tokens', 0],
      ['simulated_model_cache_video_fallback_tokens', 1.5],
      ['simulated_model_cache_file_fallback_tokens', 1_000_001],
    ]

    for (const [field, value] of cases) {
      const result = channelFormSchema.safeParse(
        validChannelForm({
          simulated_model_cache_enabled: true,
          simulated_model_cache_multimodal_enabled: true,
          ...multimodalValues,
          [field]: value,
        })
      )
      assert.equal(result.success, false, `${field}=${value} should fail`)
      if (!result.success) {
        assert.equal(
          result.error.issues.some((issue) => issue.path[0] === field),
          true
        )
      }
    }
  })

  test('keeps channels without multimedia configuration valid', () => {
    const legacy = transformChannelToFormDefaults(
      channelWithSettings(
        '{"simulated_model_cache":{"enabled":false,"ttl_seconds":60}}'
      )
    )

    assert.equal(legacy.simulated_model_cache_multimodal_enabled, false)
    assert.equal(
      legacy.simulated_model_cache_image_tokens_per_megapixel,
      undefined
    )
    assert.equal(
      channelFormSchema.safeParse(validChannelForm(legacy)).success,
      true
    )
  })

  test('drops legacy missing input estimation settings', () => {
    const form = transformChannelToFormDefaults(
      channelWithSettings(
        '{"simulated_model_cache":{"enabled":true,"estimate_missing_input_tokens":true,"missing_input_token_multiplier":1.25,"ttl_seconds":60}}'
      )
    )

    const payload = transformFormDataToCreatePayload(validChannelForm(form))
    const saved = JSON.parse(String(payload.channel.settings))
    assert.equal(saved.simulated_model_cache.enabled, true)
    assert.equal(saved.simulated_model_cache.ttl_seconds, 60)
    assert.equal(
      saved.simulated_model_cache.estimate_missing_input_tokens,
      undefined
    )
    assert.equal(
      saved.simulated_model_cache.missing_input_token_multiplier,
      undefined
    )
  })
})
