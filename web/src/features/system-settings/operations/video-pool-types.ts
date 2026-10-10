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
import type { VideoProcessingConfig } from './video-processing-config'

export type VideoPoolStats = {
  current_children?: number
  peak_children?: number
  running: number
  queued: number
  peak_running?: number
  queued_bytes: number
  buffer_bytes: number
  disk_bytes: number
  accepted: number
  rejected: number
  completed: number
  failed: number
  timeouts?: number
  cancelled?: number
  cache_hits: number
  decode_cache_hits?: number
  decode_cache_misses?: number
  frame_cache_hits?: number
  frame_cache_misses?: number
  queue_wait_milliseconds: number
  processing_milliseconds: number
  child_cpu_seconds: number
  current_child_rss_bytes?: number
  peak_combined_child_rss_bytes?: number
  peak_child_rss_bytes: number
  generated_jpeg_bytes: number
  output_frames?: number
  adaptive_tier: number
  adaptive_light: number
  adaptive_heavy: number
  completed_per_minute?: number
  config_version?: number
  timing_5m?: Record<
    'queue' | 'decode' | 'extract' | 'total',
    { p50_ms: number; p95_ms: number }
  >
  system: {
    sampled_at: string
    supported: boolean
    cpu_available: boolean
    memory_available: boolean
    disk_available: boolean
    cpu_percent: number
    memory_percent: number
    effective_cpus: number
    memory_used_bytes: number
    memory_limit_bytes: number
    disk_free_bytes: number
    ffmpeg_available: boolean
    ffprobe_available: boolean
    prlimit_available: boolean
  }
  config: VideoProcessingConfig
}
