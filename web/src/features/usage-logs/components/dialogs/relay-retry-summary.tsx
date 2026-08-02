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
import { AlertTriangle, KeyRound, Route } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { IconBadge } from '@/components/ui/icon-badge'
import { Label } from '@/components/ui/label'

import {
  relayActionLabel,
  relayReasonLabel,
  relayStageLabel,
} from '../../lib/relay-debug-labels'
import type {
  RelayRetryErrorSummary,
  RelayRetryOccurrence,
  RelayRetrySummary,
} from '../../types'

function occurrenceChannel(occurrence: RelayRetryOccurrence): string {
  if (occurrence.channel_id == null) return '-'
  if (!occurrence.channel_name) return `#${occurrence.channel_id}`
  return `#${occurrence.channel_id} (${occurrence.channel_name})`
}

function RetryOccurrenceRow(props: { occurrence: RelayRetryOccurrence }) {
  const { t } = useTranslation()
  const occurrence = props.occurrence

  return (
    <div
      role='listitem'
      className='grid min-w-0 gap-1.5 border-t py-2 first:border-t-0 first:pt-0 last:pb-0 sm:grid-cols-[5rem_minmax(0,1fr)]'
    >
      <span className='text-muted-foreground text-xs font-medium tabular-nums'>
        {t('Attempt {{index}}', { index: occurrence.attempt_index })}
      </span>
      <div className='min-w-0 space-y-1 text-xs'>
        <div className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1'>
          <span className='font-medium'>
            {relayStageLabel(occurrence.stage, t)}
          </span>
          <span className='text-muted-foreground min-w-0 font-mono break-all'>
            {occurrenceChannel(occurrence)}
          </span>
          {occurrence.channel_type != null && (
            <span className='text-muted-foreground'>
              {t('Channel type {{type}}', { type: occurrence.channel_type })}
            </span>
          )}
          {occurrence.multi_key_index != null && (
            <span className='inline-flex items-center gap-1 font-mono'>
              <KeyRound className='size-3' aria-hidden='true' />
              {t('Key {{index}}', {
                index: occurrence.multi_key_index + 1,
              })}
            </span>
          )}
        </div>
        <div className='flex min-w-0 items-start gap-1.5'>
          <Route
            className='text-muted-foreground mt-0.5 size-3 shrink-0'
            aria-hidden='true'
          />
          <span className='min-w-0 break-words'>
            <span className='font-medium'>
              {relayActionLabel(occurrence.action, t)}
            </span>
            <span className='text-muted-foreground'>
              {' · '}
              {relayReasonLabel(occurrence.reason, t)}
            </span>
          </span>
        </div>
      </div>
    </div>
  )
}

function RetryErrorBlock(props: {
  error: RelayRetryErrorSummary
  index: number
}) {
  const { t } = useTranslation()
  const error = props.error
  const status = error.upstream_status_code ?? error.status_code

  return (
    <div className='min-w-0 border-t py-3 first:border-t-0 first:pt-0 last:pb-0'>
      <div className='mb-2 flex min-w-0 flex-wrap items-center gap-1.5'>
        <span className='text-xs font-semibold'>
          {t('Unique error {{index}}', { index: props.index + 1 })}
        </span>
        {status > 0 && (
          <StatusBadge
            label={`HTTP ${status}`}
            variant='red'
            size='sm'
            copyable={false}
          />
        )}
        {error.type && (
          <StatusBadge
            label={error.type}
            variant='grey'
            size='sm'
            copyable={false}
          />
        )}
        {error.code && (
          <StatusBadge
            label={error.code}
            variant='grey'
            size='sm'
            copyable={false}
          />
        )}
        <span className='text-muted-foreground text-xs'>
          {t('{{count}} occurrences', { count: error.occurrences.length })}
        </span>
      </div>

      <p className='text-destructive min-w-0 font-mono text-xs leading-relaxed break-words'>
        {error.message}
      </p>

      {error.response_preview && (
        <div className='mt-2 min-w-0 space-y-1'>
          <Label className='text-muted-foreground text-xs'>
            {t('Error response preview')}
          </Label>
          <pre className='bg-background max-h-40 min-w-0 overflow-auto rounded-md border p-2 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap'>
            {error.response_preview}
          </pre>
        </div>
      )}

      <div role='list' className='mt-2 min-w-0 border-t pt-2'>
        {error.occurrences.map((occurrence) => (
          <RetryOccurrenceRow
            key={`${occurrence.attempt_index}-${occurrence.stage}`}
            occurrence={occurrence}
          />
        ))}
      </div>
    </div>
  )
}

export function RelayRetrySummarySection(props: {
  summary: RelayRetrySummary
}) {
  const { t } = useTranslation()
  const recovered = props.summary.outcome === 'recovered'

  return (
    <section className='min-w-0 space-y-1.5' aria-label={t('Relay failures')}>
      <Label className='flex items-center gap-1.5 text-xs font-semibold text-orange-700 dark:text-orange-300'>
        <IconBadge tone='warning' size='xs'>
          <AlertTriangle className='size-3.5' aria-hidden='true' />
        </IconBadge>
        {t('Relay failures')}
      </Label>
      <div className='min-w-0 overflow-hidden rounded-md border border-orange-200 bg-orange-50/60 p-2.5 max-sm:p-2 dark:border-orange-900 dark:bg-orange-950/20'>
        <div className='mb-3 flex min-w-0 flex-wrap items-center gap-1.5'>
          <StatusBadge
            label={recovered ? t('Recovered after retry') : t('Request failed')}
            variant={recovered ? 'orange' : 'red'}
            size='sm'
            copyable={false}
          />
          <span className='text-muted-foreground text-xs'>
            {t('{{failures}} failed attempts · {{unique}} unique errors', {
              failures: props.summary.failure_count,
              unique: props.summary.unique_error_count,
            })}
          </span>
          <span className='text-muted-foreground font-mono text-xs'>
            {props.summary.method} {props.summary.path}
          </span>
        </div>
        {props.summary.errors.map((error, index) => (
          <RetryErrorBlock
            key={`${error.status_code}-${error.upstream_status_code ?? 0}-${error.type ?? ''}-${error.code ?? ''}-${error.message}-${error.response_preview ?? ''}`}
            error={error}
            index={index}
          />
        ))}
      </div>
    </section>
  )
}
