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
import { useQuery } from '@tanstack/react-query'
import {
  AlertCircle,
  Check,
  Copy,
  FileJson2,
  RefreshCw,
  Send,
  Server,
} from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { formatTimestampToDate } from '@/lib/format'

import {
  relayActionLabel,
  relayReasonLabel,
  relayStageLabel,
} from '../../lib/relay-debug-labels'
import { getRelayDebugTrace } from '../../relay-debug-api'
import type {
  RelayDebugAttempt,
  RelayDebugBody,
  RelayDebugHTTPMessage,
  RelayDebugTrace,
} from '../../types'

function formatDebugBodyText(body: RelayDebugBody): string {
  if (!body.text) return ''
  if (body.kind !== 'json' || body.text.length > 256 * 1024) return body.text
  try {
    return JSON.stringify(JSON.parse(body.text), null, 2)
  } catch {
    return body.text
  }
}

function bodyOmissionLabel(
  reason: string | undefined,
  t: (key: string) => string
) {
  if (reason === 'binary_body') return t('Binary body omitted')
  if (reason === 'body_exceeds_debug_limit') {
    return t('Body exceeds debug limit')
  }
  if (reason === 'trace_text_budget_exceeded') {
    return t('Trace text budget exceeded')
  }
  if (reason === 'invalid_multipart_boundary') {
    return t('Invalid multipart boundary')
  }
  return reason ? t(reason) : t('Body omitted')
}

function DebugBodyPanel(props: { body?: RelayDebugBody; label: string }) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const formattedText = useMemo(
    () => (props.body ? formatDebugBodyText(props.body) : ''),
    [props.body]
  )
  const hasOmittedMedia =
    props.body?.kind === 'omitted' ||
    formattedText.includes('"omitted": "media"') ||
    formattedText.includes('"omitted":"media"') ||
    formattedText.includes('"omitted": "base64"') ||
    formattedText.includes('"omitted":"base64"')

  return (
    <div className='min-w-0 space-y-1.5'>
      <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
        <Label className='text-xs'>{props.label}</Label>
        {props.body && (
          <>
            <StatusBadge
              label={props.body.kind}
              variant='grey'
              size='sm'
              copyable={false}
            />
            <span className='text-muted-foreground text-xs tabular-nums'>
              {t('{{count}} bytes', { count: props.body.size })}
            </span>
            {props.body.truncated && (
              <StatusBadge
                label={t('Truncated')}
                variant='orange'
                size='sm'
                copyable={false}
              />
            )}
          </>
        )}
      </div>

      {!props.body && (
        <div className='text-muted-foreground rounded-md border border-dashed px-2.5 py-2 text-xs'>
          {t('No body captured')}
        </div>
      )}
      {props.body?.omitted_reason && (
        <Alert>
          <FileJson2 aria-hidden='true' />
          <AlertTitle>
            {bodyOmissionLabel(props.body.omitted_reason, t)}
          </AlertTitle>
          <AlertDescription>
            {t('{{count}} bytes were not persisted.', {
              count: props.body.size,
            })}
          </AlertDescription>
        </Alert>
      )}
      {props.body && !props.body.omitted_reason && (
        <div className='relative min-w-0'>
          {formattedText && (
            <Button
              type='button'
              variant='ghost'
              size='icon-xs'
              className='absolute top-1.5 right-1.5 z-10'
              onClick={() => copyToClipboard(formattedText)}
              title={t('Copy to clipboard')}
              aria-label={t('Copy to clipboard')}
            >
              {copiedText === formattedText ? (
                <Check className='text-success size-3' aria-hidden='true' />
              ) : (
                <Copy className='size-3' aria-hidden='true' />
              )}
            </Button>
          )}
          <pre className='bg-background max-h-80 min-h-12 min-w-0 overflow-auto rounded-md border p-2 pr-9 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap'>
            {formattedText || t('Empty body')}
          </pre>
        </div>
      )}

      {hasOmittedMedia && props.body?.kind !== 'omitted' && (
        <p className='text-muted-foreground text-xs'>
          {t('Media and Base64 payloads were omitted.')}
        </p>
      )}
    </div>
  )
}

function DebugHeaders(props: { headers?: Record<string, string[]> }) {
  const { t } = useTranslation()
  const entries = Object.entries(props.headers ?? {}).sort(([left], [right]) =>
    left.localeCompare(right)
  )
  if (entries.length === 0) return null

  return (
    <details className='min-w-0 text-xs'>
      <summary className='text-muted-foreground cursor-pointer py-1 font-medium select-none'>
        {t('Headers ({{count}})', { count: entries.length })}
      </summary>
      <div className='bg-muted/20 min-w-0 space-y-1 rounded-md border p-2 font-mono'>
        {entries.map(([name, values]) => (
          <div
            key={name}
            className='grid min-w-0 grid-cols-[minmax(5rem,9rem)_minmax(0,1fr)] gap-2'
          >
            <span className='text-muted-foreground break-all'>{name}</span>
            <span className='min-w-0 break-all'>{values.join(', ')}</span>
          </div>
        ))}
      </div>
    </details>
  )
}

function HTTPMessagePanel(props: {
  message: RelayDebugHTTPMessage
  label: string
  response?: boolean
}) {
  const { t } = useTranslation()

  return (
    <div className='min-w-0 space-y-2'>
      <div className='flex min-w-0 flex-wrap items-center gap-1.5 text-xs'>
        {props.response ? (
          <Server
            className='text-muted-foreground size-3.5'
            aria-hidden='true'
          />
        ) : (
          <Send className='text-muted-foreground size-3.5' aria-hidden='true' />
        )}
        <span className='font-semibold'>{props.label}</span>
        {props.message.method && (
          <StatusBadge
            label={props.message.method}
            variant='grey'
            size='sm'
            copyable={false}
          />
        )}
        {props.message.status != null && props.message.status > 0 && (
          <StatusBadge
            label={`HTTP ${props.message.status}`}
            variant={props.message.status >= 400 ? 'red' : 'green'}
            size='sm'
            copyable={false}
          />
        )}
      </div>
      {props.message.url && (
        <div className='grid min-w-0 grid-cols-[3.5rem_minmax(0,1fr)] gap-2 text-xs'>
          <span className='text-muted-foreground'>{t('URL')}</span>
          <span className='min-w-0 font-mono break-all'>
            {props.message.url}
          </span>
        </div>
      )}
      <DebugHeaders headers={props.message.headers} />
      <DebugBodyPanel body={props.message.body} label={t('Body')} />
    </div>
  )
}

function AttemptPanel(props: { attempt: RelayDebugAttempt }) {
  const { t } = useTranslation()
  const attempt = props.attempt

  return (
    <div className='min-w-0 space-y-3'>
      <div className='grid min-w-0 gap-1.5 text-xs sm:grid-cols-2'>
        <div>
          <span className='text-muted-foreground'>{t('Stage')}: </span>
          <span className='font-medium'>
            {relayStageLabel(attempt.stage, t)}
          </span>
        </div>
        <div>
          <span className='text-muted-foreground'>{t('Duration')}: </span>
          <span className='font-mono tabular-nums'>
            {attempt.duration_ms} ms
          </span>
        </div>
        <div className='min-w-0'>
          <span className='text-muted-foreground'>{t('Channel')}: </span>
          <span className='font-mono break-all'>
            {attempt.channel_id == null
              ? '-'
              : `#${attempt.channel_id}${attempt.channel_name ? ` (${attempt.channel_name})` : ''}`}
          </span>
        </div>
        <div>
          <span className='text-muted-foreground'>{t('Decision')}: </span>
          <span className='font-medium'>
            {relayActionLabel(attempt.decision.action, t)}
          </span>
          <span className='text-muted-foreground'>
            {' · '}
            {relayReasonLabel(attempt.decision.reason, t)}
          </span>
        </div>
      </div>

      {attempt.exchanges?.map((exchange, index) => (
        <div
          key={`${exchange.index ?? 'exchange'}-${exchange.request.method ?? ''}-${exchange.request.url ?? ''}-${exchange.response.status ?? 0}-${exchange.request.body?.sha256 ?? ''}`}
          className='grid min-w-0 gap-3 border-t pt-3 lg:grid-cols-2'
        >
          <HTTPMessagePanel
            message={exchange.request}
            label={t('Upstream request {{index}}', { index: index + 1 })}
          />
          <HTTPMessagePanel
            message={exchange.response}
            label={t('Upstream response {{index}}', { index: index + 1 })}
            response
          />
        </div>
      ))}

      {attempt.error && (
        <div className='border-destructive/30 min-w-0 space-y-2 border-t pt-3'>
          <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
            <AlertCircle
              className='text-destructive size-3.5'
              aria-hidden='true'
            />
            <span className='text-destructive text-xs font-semibold'>
              {t('Attempt error')}
            </span>
            {(attempt.error.upstream_status_code ?? attempt.error.status_code) >
              0 && (
              <StatusBadge
                label={`HTTP ${attempt.error.upstream_status_code ?? attempt.error.status_code}`}
                variant='red'
                size='sm'
                copyable={false}
              />
            )}
          </div>
          <p className='text-destructive font-mono text-xs leading-relaxed break-words'>
            {attempt.error.message}
          </p>
          {attempt.error.response && (
            <DebugBodyPanel
              body={attempt.error.response}
              label={t('Error response')}
            />
          )}
        </div>
      )}
    </div>
  )
}

function ClientRequestPanel(props: { trace: RelayDebugTrace }) {
  const { t } = useTranslation()
  const trace = props.trace

  return (
    <div className='min-w-0 space-y-3'>
      <div className='grid min-w-0 gap-1.5 text-xs sm:grid-cols-2'>
        <div>
          <span className='text-muted-foreground'>{t('Request')}: </span>
          <span className='font-mono'>
            {trace.client.method} {trace.client.path}
          </span>
        </div>
        <div>
          <span className='text-muted-foreground'>{t('Captured at')}: </span>
          <span>{formatTimestampToDate(trace.created_at, 'milliseconds')}</span>
        </div>
      </div>
      <DebugHeaders headers={trace.client.headers} />
      <DebugBodyPanel
        body={trace.client.body}
        label={t('Original request body')}
      />
    </div>
  )
}

function relayTraceErrorCode(error: unknown): string {
  const response = (error as { response?: { data?: { code?: unknown } } })
    ?.response
  return typeof response?.data?.code === 'string' ? response.data.code : ''
}

export function RelayDebugViewer(props: { requestId: string }) {
  const { t } = useTranslation()
  const traceQuery = useQuery({
    queryKey: ['relay-debug-trace', props.requestId],
    queryFn: async () => {
      const response = await getRelayDebugTrace(props.requestId)
      if (!response.success || !response.data) {
        throw new Error(response.message || 'relay debug trace unavailable')
      }
      return response.data
    },
    retry: false,
    refetchOnWindowFocus: false,
    staleTime: Number.POSITIVE_INFINITY,
  })

  if (traceQuery.isPending) {
    return (
      <div
        className='text-muted-foreground flex min-h-24 items-center justify-center gap-2 text-xs'
        role='status'
      >
        <Spinner aria-label={t('Loading relay trace')} />
        {t('Loading relay trace')}
      </div>
    )
  }

  if (traceQuery.isError || !traceQuery.data) {
    const notFound =
      relayTraceErrorCode(traceQuery.error) === 'RELAY_DEBUG_TRACE_NOT_FOUND'
    return (
      <Alert variant='destructive'>
        <AlertCircle aria-hidden='true' />
        <AlertTitle>{t('Relay trace unavailable')}</AlertTitle>
        <AlertDescription>
          {notFound
            ? t('The relay trace has expired or is no longer available.')
            : t('The relay trace could not be loaded.')}
        </AlertDescription>
        <Button
          type='button'
          variant='outline'
          size='sm'
          className='mt-2 w-fit'
          onClick={() => traceQuery.refetch()}
          disabled={traceQuery.isFetching}
        >
          {traceQuery.isFetching ? (
            <Spinner aria-hidden='true' />
          ) : (
            <RefreshCw data-icon='inline-start' aria-hidden='true' />
          )}
          {t('Retry')}
        </Button>
      </Alert>
    )
  }

  const trace = traceQuery.data
  return (
    <section className='min-w-0 space-y-2' aria-label={t('Full relay trace')}>
      <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
        <Label className='text-xs font-semibold'>{t('Full relay trace')}</Label>
        <StatusBadge
          label={trace.outcome === 'recovered' ? t('Recovered') : t('Failed')}
          variant={trace.outcome === 'recovered' ? 'orange' : 'red'}
          size='sm'
          copyable={false}
        />
        <span className='text-muted-foreground min-w-0 font-mono text-xs break-all'>
          {trace.request_id}
        </span>
      </div>

      <Tabs defaultValue='client' className='min-w-0 gap-2'>
        <div className='min-w-0 overflow-x-auto pb-1'>
          <TabsList className='min-w-max'>
            <TabsTrigger value='client'>{t('Original request')}</TabsTrigger>
            {trace.attempts.map((attempt) => (
              <TabsTrigger
                key={attempt.index}
                value={`attempt-${attempt.index}`}
              >
                {t('Attempt {{index}}', { index: attempt.index })}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
        <TabsContent
          value='client'
          className='min-w-0 rounded-md border p-3 max-sm:p-2'
        >
          <ClientRequestPanel trace={trace} />
        </TabsContent>
        {trace.attempts.map((attempt) => (
          <TabsContent
            key={attempt.index}
            value={`attempt-${attempt.index}`}
            className='min-w-0 rounded-md border p-3 max-sm:p-2'
          >
            <AttemptPanel attempt={attempt} />
          </TabsContent>
        ))}
      </Tabs>
    </section>
  )
}
