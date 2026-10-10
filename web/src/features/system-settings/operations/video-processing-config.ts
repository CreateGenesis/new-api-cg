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
import { z } from 'zod'

const percent = z.number().int().min(1).max(100)
const fps = z
  .number()
  .min(0.1)
  .max(8)
  .refine(
    (value) => Math.abs(value * 10 - Math.round(value * 10)) < 1e-8,
    'Video frame rate must use increments of 0.1 fps.'
  )
const adaptiveLevelSchema = z.object({
  pixel_multiplier_percent: percent,
  fps_multiplier_percent: percent,
  frames_multiplier_percent: percent,
  jpeg_quality_cap: percent,
  cpu_threshold: percent,
  memory_threshold: percent,
  running_threshold: percent,
  queue_threshold: percent,
})

export const videoProcessingSchema = z
  .object({
    max_concurrency: z.number().int().min(1).max(256),
    queue_capacity: z.number().int().min(0).max(4096),
    queue_bytes_mb: z.number().int().min(1).max(65536),
    queue_wait_seconds: z.number().int().min(1).max(120),
    request_timeout_seconds: z.number().int().min(1).max(300),
    ffmpeg_threads: z.number().int().min(1).max(16),
    worker_memory_mb: z.number().int().min(256).max(8192),
    buffer_memory_mb: z.number().int().min(64).max(65536),
    temp_disk_mb: z.number().int().min(16).max(1048576),
    adaptive_enabled: z.boolean(),
    adaptive_config: z.object({
      light: adaptiveLevelSchema,
      heavy: adaptiveLevelSchema,
      minimum_fps: fps,
      minimum_frames: z.number().int().min(1).max(512),
      minimum_pixel_percent: percent,
      minimum_jpeg_quality: percent,
      recovery_seconds: z.number().int().min(1).max(300),
    }),
  })
  .superRefine((value, ctx) => {
    const light = value.adaptive_config.light
    const heavy = value.adaptive_config.heavy
    const thresholdKeys = [
      'cpu_threshold',
      'memory_threshold',
      'running_threshold',
      'queue_threshold',
    ] as const
    const qualityKeys = [
      'pixel_multiplier_percent',
      'fps_multiplier_percent',
      'frames_multiplier_percent',
      'jpeg_quality_cap',
    ] as const
    for (const key of thresholdKeys) {
      if (heavy[key] < light[key]) {
        ctx.addIssue({
          code: 'custom',
          path: ['adaptive_config', 'heavy', key],
          message:
            'Heavy load thresholds must be at least the light load thresholds.',
        })
      }
    }
    for (const key of qualityKeys) {
      if (heavy[key] > light[key]) {
        ctx.addIssue({
          code: 'custom',
          path: ['adaptive_config', 'heavy', key],
          message:
            'Heavy load quality settings must not exceed light load settings.',
        })
      }
    }
  })

export type VideoProcessingConfig = z.infer<typeof videoProcessingSchema>
export const DEFAULT_VIDEO_PROCESSING_CONFIG: VideoProcessingConfig = {
  max_concurrency: 32,
  queue_capacity: 128,
  queue_bytes_mb: 512,
  queue_wait_seconds: 15,
  request_timeout_seconds: 60,
  ffmpeg_threads: 1,
  worker_memory_mb: 1024,
  buffer_memory_mb: 2048,
  temp_disk_mb: 4096,
  adaptive_enabled: false,
  adaptive_config: {
    light: {
      pixel_multiplier_percent: 50,
      fps_multiplier_percent: 75,
      frames_multiplier_percent: 75,
      jpeg_quality_cap: 75,
      cpu_threshold: 70,
      memory_threshold: 75,
      running_threshold: 75,
      queue_threshold: 25,
    },
    heavy: {
      pixel_multiplier_percent: 25,
      fps_multiplier_percent: 50,
      frames_multiplier_percent: 50,
      jpeg_quality_cap: 50,
      cpu_threshold: 85,
      memory_threshold: 85,
      running_threshold: 90,
      queue_threshold: 50,
    },
    minimum_fps: 0.5,
    minimum_frames: 16,
    minimum_pixel_percent: 25,
    minimum_jpeg_quality: 40,
    recovery_seconds: 30,
  },
}

export function parseVideoProcessingConfig(raw: string): {
  config: VideoProcessingConfig
  invalid: boolean
} {
  if (!raw) {
    return {
      config: structuredClone(DEFAULT_VIDEO_PROCESSING_CONFIG),
      invalid: false,
    }
  }
  try {
    const result = videoProcessingSchema.safeParse(JSON.parse(raw))
    if (result.success) return { config: result.data, invalid: false }
  } catch {
    /* Saved configuration is validated before displaying it. */
  }
  return {
    config: structuredClone(DEFAULT_VIDEO_PROCESSING_CONFIG),
    invalid: true,
  }
}
