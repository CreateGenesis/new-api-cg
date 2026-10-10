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

export const VIDEO_COMPATIBILITY_DEFAULTS = {
  enabled: false,
  fps: 8,
  max_frames: 256,
  max_side: 1024,
  pixel_percent: 100,
  jpeg_quality: 100,
}

export const videoCompatibilitySchema = z.object({
  enabled: z.boolean(),
  fps: z
    .number()
    .min(0.1)
    .max(8)
    .refine(
      (value) => Math.abs(value * 10 - Math.round(value * 10)) < 1e-8,
      'Video frame rate must use increments of 0.1 fps.'
    ),
  max_frames: z.number().int().min(1).max(512),
  max_side: z.number().int().min(256).max(2048),
  pixel_percent: z.number().int().min(1).max(100),
  jpeg_quality: z.number().int().min(1).max(100),
})

export type VideoCompatibility = z.infer<typeof videoCompatibilitySchema>

export function readVideoCompatibility(value: unknown): VideoCompatibility {
  if (!value || typeof value !== 'object') {
    return { ...VIDEO_COMPATIBILITY_DEFAULTS }
  }
  const parsed = videoCompatibilitySchema.safeParse({
    ...VIDEO_COMPATIBILITY_DEFAULTS,
    ...value,
  })
  return parsed.success ? parsed.data : { ...VIDEO_COMPATIBILITY_DEFAULTS }
}
