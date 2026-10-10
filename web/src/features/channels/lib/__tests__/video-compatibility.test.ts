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
import { describe, expect, test } from 'vitest'

import type { Channel } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from '../channel-form'
import {
  VIDEO_COMPATIBILITY_DEFAULTS,
  readVideoCompatibility,
  videoCompatibilitySchema,
} from '../video-compatibility'

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

describe('video compatibility settings', () => {
  test('missing optional channel settings stay disabled and do not rewrite existing settings', () => {
    const values = transformChannelToFormDefaults(testChannel('{}'))
    expect(values.video_compatibility).toEqual(VIDEO_COMPATIBILITY_DEFAULTS)
    expect(
      JSON.parse(
        String(transformFormDataToCreatePayload(values).channel.settings)
      )
    ).not.toHaveProperty('video_compatibility')
  })
  test('partial backend settings receive defaults without losing selected quality', () => {
    expect(
      readVideoCompatibility({
        enabled: true,
        pixel_percent: 25,
        jpeg_quality: 45,
      })
    ).toEqual({
      ...VIDEO_COMPATIBILITY_DEFAULTS,
      enabled: true,
      pixel_percent: 25,
      jpeg_quality: 45,
    })
  })
  test('editing and copying preserve quality settings and unknown request options', () => {
    const video = {
      ...VIDEO_COMPATIBILITY_DEFAULTS,
      enabled: true,
      fps: 1.5,
      pixel_percent: 25,
      jpeg_quality: 45,
    }
    const values = transformChannelToFormDefaults(
      testChannel(
        JSON.stringify({ video_compatibility: video, custom_option: 'keep' })
      )
    )
    const settings = JSON.parse(
      String(
        transformFormDataToCreatePayload({
          ...values,
          name: 'copy',
          key: 'test-key',
        }).channel.settings
      )
    )
    expect(settings).toMatchObject({
      video_compatibility: video,
      custom_option: 'keep',
    })
  })
  test('disabling extraction retains manually tuned settings for future enablement', () => {
    const values = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      video_compatibility: {
        ...VIDEO_COMPATIBILITY_DEFAULTS,
        pixel_percent: 25,
      },
    }
    expect(
      JSON.parse(
        String(transformFormDataToCreatePayload(values).channel.settings)
      )
    ).toHaveProperty('video_compatibility.pixel_percent', 25)
  })
  test.each([
    { fps: 0 },
    { fps: 0.15 },
    { max_frames: 513 },
    { max_side: 255 },
    { pixel_percent: 0 },
    { jpeg_quality: 101 },
  ])('invalid quality setting %j is rejected', (invalid) => {
    expect(
      videoCompatibilitySchema.safeParse({
        ...VIDEO_COMPATIBILITY_DEFAULTS,
        ...invalid,
      }).success
    ).toBe(false)
  })
  test('video disablement and compatibility cannot both be enabled', () => {
    const result = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'video',
      key: 'test-key',
      models: 'k3',
      disable_video_understanding: true,
      video_compatibility: { ...VIDEO_COMPATIBILITY_DEFAULTS, enabled: true },
    })
    expect(result.success).toBe(false)
    if (!result.success) {
      expect(
        result.error.issues.some(
          (issue) => issue.path.join('.') === 'video_compatibility.enabled'
        )
      ).toBe(true)
    }
  })
})
