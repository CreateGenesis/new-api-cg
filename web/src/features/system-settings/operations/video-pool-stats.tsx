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
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Progress } from '@/components/ui/progress'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestampToDate } from '@/lib/format'

import type { VideoPoolStats as Stats } from './video-pool-types'

export function VideoPoolStats(props: { stats: Stats }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const stats = props.stats
  const number = (value?: number) => formatNumber(value, locale)
  const bytes = (value?: number) =>
    value === undefined ? '-' : `${number(value / 1048576)} MiB`
  const tierLabels = [
    t('Normal video quality'),
    t('Light load reduction'),
    t('Heavy load reduction'),
  ]
  const limits = [
    {
      label: t('Running media tasks'),
      used: stats.running,
      limit: stats.config.max_concurrency,
      format: number,
    },
    {
      label: t('Waiting media tasks'),
      used: stats.queued,
      limit: stats.config.queue_capacity,
      format: number,
    },
    {
      label: t('Queued request bodies'),
      used: stats.queued_bytes,
      limit: stats.config.queue_bytes_mb * 1048576,
      format: bytes,
    },
    {
      label: t('Media buffer memory'),
      used: stats.buffer_bytes,
      limit: stats.config.buffer_memory_mb * 1048576,
      format: bytes,
    },
    {
      label: t('Media temporary disk'),
      used: stats.disk_bytes,
      limit: stats.config.temp_disk_mb * 1048576,
      format: bytes,
    },
  ]
  const metrics = [
    {
      label: t('Active FFmpeg/FFprobe processes'),
      value: number(stats.current_children),
    },
    {
      label: t('Peak FFmpeg/FFprobe processes'),
      value: number(stats.peak_children),
    },
    { label: t('Peak media concurrency'), value: number(stats.peak_running) },
    { label: t('Completed media tasks'), value: number(stats.completed) },
    {
      label: t('Media completions per minute'),
      value: number(stats.completed_per_minute),
    },
    { label: t('Failed media tasks'), value: number(stats.failed) },
    { label: t('Rejected media tasks'), value: number(stats.rejected) },
    { label: t('Media timeouts'), value: number(stats.timeouts) },
    { label: t('Cancelled media tasks'), value: number(stats.cancelled) },
    { label: t('Generated video frames'), value: number(stats.output_frames) },
    {
      label: t('Generated JPEG bytes'),
      value: bytes(stats.generated_jpeg_bytes),
    },
    {
      label: t('Current media process RSS'),
      value: bytes(stats.current_child_rss_bytes),
    },
    {
      label: t('Peak combined media process RSS'),
      value: bytes(stats.peak_combined_child_rss_bytes),
    },
    {
      label: t('Peak media process RSS'),
      value: bytes(stats.peak_child_rss_bytes),
    },
    {
      label: t('Media process CPU seconds'),
      value: number(stats.child_cpu_seconds),
    },
    { label: t('Decode cache hits'), value: number(stats.decode_cache_hits) },
    { label: t('Frame cache hits'), value: number(stats.frame_cache_hits) },
    {
      label: t('Decode cache hit rate'),
      value: cacheRate(
        stats.decode_cache_hits,
        stats.decode_cache_misses,
        number
      ),
    },
    {
      label: t('Frame cache hit rate'),
      value: cacheRate(
        stats.frame_cache_hits,
        stats.frame_cache_misses,
        number
      ),
    },
    {
      label: t('Light reduction requests'),
      value: number(stats.adaptive_light),
    },
    {
      label: t('Heavy reduction requests'),
      value: number(stats.adaptive_heavy),
    },
    {
      label: t('CPU'),
      value: stats.system.cpu_available
        ? `${number(stats.system.cpu_percent)}%`
        : t('Unavailable'),
    },
    {
      label: t('Memory'),
      value: stats.system.memory_available
        ? `${number(stats.system.memory_percent)}%`
        : t('Unavailable'),
    },
    {
      label: t('Effective CPU limit'),
      value: number(stats.system.effective_cpus),
    },
    {
      label: t('Effective memory limit'),
      value: stats.system.memory_available
        ? bytes(stats.system.memory_limit_bytes)
        : t('Unavailable'),
    },
  ]
  const phases = [
    { name: 'queue', label: t('Media queue wait') },
    { name: 'decode', label: t('Source video decoding') },
    { name: 'extract', label: t('Frame extraction and encoding') },
    { name: 'total', label: t('Total media processing') },
  ] as const
  return (
    <section
      className='space-y-4 rounded-lg border p-4'
      aria-label={t('Video processing pool')}
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h3 className='text-sm font-medium'>{t('Video processing pool')}</h3>
        <StatusBadge variant='neutral' copyable={false}>
          {tierLabels[stats.adaptive_tier] ?? t('Unavailable')}
        </StatusBadge>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t('Auto-refreshing every {{seconds}}s', { seconds: 5 })} ·{' '}
        {t('Resource sample')}:{' '}
        {formatTimestampToDate(Date.parse(stats.system.sampled_at) / 1000)} ·{' '}
        {t('Configuration version')}:{' '}
        <span className='font-mono break-all'>
          {stats.config_version ?? '-'}
        </span>
      </p>
      <div className='grid gap-4 sm:grid-cols-2 lg:grid-cols-3'>
        {limits.map((limit) => (
          <div key={limit.label} className='space-y-2'>
            <div className='flex items-center justify-between gap-2 text-xs'>
              <span>{limit.label}</span>
              <span className='font-mono'>
                {limit.format(limit.used)} / {limit.format(limit.limit)}
              </span>
            </div>
            <Progress
              aria-label={limit.label}
              value={
                limit.limit > 0
                  ? Math.min(100, (100 * limit.used) / limit.limit)
                  : 0
              }
            />
          </div>
        ))}
      </div>
      <dl className='grid gap-x-4 gap-y-2 text-xs sm:grid-cols-2 lg:grid-cols-3'>
        {metrics.map((metric) => (
          <div key={metric.label} className='flex justify-between gap-2'>
            <dt className='text-muted-foreground'>{metric.label}</dt>
            <dd className='font-mono tabular-nums'>{metric.value}</dd>
          </div>
        ))}
      </dl>
      <div className='overflow-x-auto'>
        <p className='text-muted-foreground mb-2 text-xs'>
          {t(
            'P50/P95 approximate the bucket upper bounds for the last 5 minutes.'
          )}
        </p>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('Media stage')}</TableHead>
              <TableHead>{t('Last 5 minutes P50 (ms)')}</TableHead>
              <TableHead>{t('Last 5 minutes P95 (ms)')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {phases.map((phase) => (
              <TableRow key={phase.name}>
                <TableCell>{phase.label}</TableCell>
                <TableCell>
                  {number(stats.timing_5m?.[phase.name]?.p50_ms)}
                </TableCell>
                <TableCell>
                  {number(stats.timing_5m?.[phase.name]?.p95_ms)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      {(!stats.system.supported ||
        !stats.system.ffmpeg_available ||
        !stats.system.ffprobe_available ||
        !stats.system.prlimit_available) && (
        <p className='text-destructive text-xs'>
          {t(
            'Video processing requires Linux, FFmpeg, FFprobe and prlimit on this instance.'
          )}
        </p>
      )}
    </section>
  )
}

function cacheRate(
  hits: number | undefined,
  misses: number | undefined,
  format: (value?: number) => string
): string {
  if (hits === undefined || misses === undefined) return '-'
  if (hits + misses === 0) return '-'
  return `${format((100 * hits) / (hits + misses))}%`
}
