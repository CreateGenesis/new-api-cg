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

import {
  DEFAULT_VIDEO_PROCESSING_CONFIG,
  parseVideoProcessingConfig,
  videoProcessingSchema,
} from '../video-processing-config'

describe('video processing configuration', () => {
  test('empty configuration uses safe defaults without reporting corruption', () => {
    const parsed = parseVideoProcessingConfig('')
    expect(parsed.invalid).toBe(false)
    expect(parsed.config.max_concurrency).toBe(32)
    expect(parsed.config.adaptive_enabled).toBe(false)
  })
  test('complete JSON roundtrip preserves high concurrency and adaptive settings', () => {
    const config = {
      ...DEFAULT_VIDEO_PROCESSING_CONFIG,
      max_concurrency: 256,
      queue_capacity: 0,
      adaptive_enabled: true,
    }
    expect(parseVideoProcessingConfig(JSON.stringify(config))).toEqual({
      config,
      invalid: false,
    })
  })
  test.each(['invalid', '{}', '{"max_concurrency":0}'])(
    'invalid saved configuration %s is flagged for review',
    (raw) => {
      expect(parseVideoProcessingConfig(raw).invalid).toBe(true)
    }
  )
  test('heavier load cannot increase quality or use lower triggers', () => {
    const config = structuredClone(DEFAULT_VIDEO_PROCESSING_CONFIG)
    config.adaptive_config.heavy.pixel_multiplier_percent = 80
    config.adaptive_config.heavy.cpu_threshold = 60
    const parsed = videoProcessingSchema.safeParse(config)
    expect(parsed.success).toBe(false)
    if (!parsed.success) {
      expect(parsed.error.issues.map((issue) => issue.path.join('.'))).toEqual(
        expect.arrayContaining([
          'adaptive_config.heavy.pixel_multiplier_percent',
          'adaptive_config.heavy.cpu_threshold',
        ])
      )
    }
  })
})
