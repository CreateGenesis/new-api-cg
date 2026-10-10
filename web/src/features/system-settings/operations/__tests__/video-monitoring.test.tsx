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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, within } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import viLocale from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { PerformanceSection } from '../../maintenance/performance-section'
import { VideoPoolStats } from '../video-pool-stats'
import type { VideoPoolStats as Stats } from '../video-pool-types'
import { DEFAULT_VIDEO_PROCESSING_CONFIG } from '../video-processing-config'

const sample: Stats = {
  current_children: 16,
  peak_children: 32,
  running: 32,
  queued: 12,
  peak_running: 32,
  queued_bytes: 1048576,
  buffer_bytes: 2097152,
  disk_bytes: 4194304,
  accepted: 100,
  rejected: 2,
  completed: 80,
  failed: 3,
  timeouts: 1,
  cancelled: 1,
  cache_hits: 5,
  decode_cache_hits: 3,
  decode_cache_misses: 3,
  frame_cache_hits: 4,
  frame_cache_misses: 0,
  queue_wait_milliseconds: 10,
  processing_milliseconds: 100,
  child_cpu_seconds: 7,
  current_child_rss_bytes: 524288,
  peak_combined_child_rss_bytes: 2097152,
  peak_child_rss_bytes: 1048576,
  generated_jpeg_bytes: 2097152,
  output_frames: 240,
  adaptive_tier: 1,
  adaptive_light: 8,
  adaptive_heavy: 2,
  completed_per_minute: 10,
  config_version: 901,
  timing_5m: {
    queue: { p50_ms: 10, p95_ms: 20 },
    decode: { p50_ms: 30, p95_ms: 40 },
    extract: { p50_ms: 50, p95_ms: 60 },
    total: { p50_ms: 90, p95_ms: 120 },
  },
  system: {
    sampled_at: '2026-10-10T10:00:00Z',
    supported: true,
    cpu_available: false,
    memory_available: true,
    disk_available: true,
    cpu_percent: 0,
    memory_percent: 25,
    effective_cpus: 8,
    memory_used_bytes: 1,
    memory_limit_bytes: 1048576,
    disk_free_bytes: 1,
    ffmpeg_available: true,
    ffprobe_available: true,
    prlimit_available: true,
  },
  config: DEFAULT_VIDEO_PROCESSING_CONFIG,
}
const defaults = {
  'performance_setting.simulated_model_cache_memory_budget_mb': 1024,
  'performance_setting.simulated_model_cache_max_entries_per_scope': 100,
  'performance_setting.simulated_model_cache_min_input_tokens': 128,
  'performance_setting.disk_cache_enabled': false,
  'performance_setting.disk_cache_threshold_mb': 10,
  'performance_setting.disk_cache_max_size_mb': 1024,
  'performance_setting.disk_cache_path': '',
  'performance_setting.monitor_enabled': false,
  'performance_setting.monitor_cpu_threshold': 90,
  'performance_setting.monitor_memory_threshold': 90,
  'performance_setting.monitor_disk_threshold': 95,
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

test('media monitoring exposes capacity, timings and sample availability without inventing zero usage', () => {
  render(<VideoPoolStats stats={sample} />)
  const region = screen.getByRole('region', { name: 'Video processing pool' })
  expect(within(region).getByText('901')).toBeVisible()
  expect(within(region).getByText('Light load reduction')).toBeVisible()
  expect(
    within(region).getByText('Peak media concurrency').parentElement
  ).toHaveTextContent('32')
  expect(within(region).getByText('CPU').parentElement).toHaveTextContent(
    'Unavailable'
  )
  expect(
    within(region).getByText(
      'P50/P95 approximate the bucket upper bounds for the last 5 minutes.'
    )
  ).toBeVisible()
  expect(
    within(region).getByRole('row', { name: 'Total media processing 90 120' })
  ).toBeVisible()
  expect(
    within(region).getByText('Decode cache hit rate').parentElement
  ).toHaveTextContent('50%')
  expect(
    within(region).getByText('Current media process RSS').parentElement
  ).toHaveTextContent('0.5 MiB')
})

test('media process counts distinguish live subprocesses from running media tasks', () => {
  render(<VideoPoolStats stats={sample} />)
  expect(
    screen.getByText('Active FFmpeg/FFprobe processes').parentElement
  ).toHaveTextContent('16')
  expect(
    screen.getByText('Peak FFmpeg/FFprobe processes').parentElement
  ).toHaveTextContent('32')
})

test('performance polling runs every five seconds while visible and pauses in a hidden tab', async () => {
  vi.useFakeTimers()
  let visibility = 'visible'
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(
    () => visibility as DocumentVisibilityState
  )
  const getSpy = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { video_pool: sample } },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  await act(async () => {
    render(
      <QueryClientProvider client={client}>
        <PerformanceSection defaultValues={defaults} />
      </QueryClientProvider>
    )
  })
  expect(api.get).toHaveBeenCalledOnce()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000)
  })
  expect(api.get).toHaveBeenCalledTimes(2)
  await act(async () => {
    visibility = 'hidden'
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000)
  })
  expect(api.get).toHaveBeenCalledTimes(2)
  await act(async () => {
    visibility = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000)
  })
  expect(getSpy.mock.calls.length).toBeGreaterThan(2)
  client.clear()
})

const localizedResources = { en, zhCN: zh, zhTW, fr, ja, ru, vi: viLocale }
test.each([
  ['en', '8.5'],
  ['zhCN', '8.5'],
  ['zhTW', '8.5'],
  ['fr', '8,5'],
  ['ja', '8.5'],
  ['ru', '8,5'],
  ['vi', '8,5'],
])(
  'video monitoring renders resource limits in interface language %s',
  async (language, expected) => {
    const instance = createInstance()
    await instance
      .use(initReactI18next)
      .init({ lng: language, fallbackLng: 'en', resources: localizedResources })
    render(
      <I18nextProvider i18n={instance}>
        <VideoPoolStats
          stats={{
            ...sample,
            system: { ...sample.system, effective_cpus: 8.5 },
          }}
        />
      </I18nextProvider>
    )
    const label = screen.getByText(instance.t('Effective CPU limit'))
    expect(label.parentElement).toHaveTextContent(expected)
    expect(
      screen.getByRole('region', { name: instance.t('Video processing pool') })
    ).toBeVisible()
  }
)

test('video monitoring refreshes translated labels when the interface language changes', async () => {
  const instance = createInstance()
  await instance
    .use(initReactI18next)
    .init({ lng: 'en', fallbackLng: 'en', resources: localizedResources })
  render(
    <I18nextProvider i18n={instance}>
      <VideoPoolStats stats={sample} />
    </I18nextProvider>
  )
  expect(
    screen.getByRole('region', { name: 'Video processing pool' })
  ).toBeVisible()
  await act(async () => {
    await instance.changeLanguage('zhTW')
  })
  expect(
    screen.getByRole('region', {
      name: zhTW.translation['Video processing pool'],
    })
  ).toBeVisible()
})
