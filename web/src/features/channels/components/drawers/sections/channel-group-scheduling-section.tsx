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
import { useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { MultiSelect } from '@/components/multi-select'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'

import {
  defaultGroupScheduling,
  type ChannelFormValues,
} from '../../../lib/channel-form'

type ProbeState = {
  model: string
  healthy: boolean
  ttft_ms: number
  checked_at: number
  next_at: number
  failures: number
}
type ProbeStatus = { probes: ProbeState[]; thresholds: Record<string, number> }

type Props = {
  form: UseFormReturn<ChannelFormValues>
  channelId: number | null
  disabled: boolean
}

export function ChannelGroupSchedulingSection(props: Props) {
  const { t } = useTranslation()
  const scheduling =
    useWatch({ control: props.form.control, name: 'group_scheduling' }) ??
    defaultGroupScheduling
  const groups = useWatch({ control: props.form.control, name: 'group' }) ?? []
  const models = useWatch({ control: props.form.control, name: 'models' }) ?? ''
  const status = useQuery({
    queryKey: ['channel-group-scheduling', props.channelId],
    enabled: props.channelId !== null && scheduling.enabled,
    refetchInterval: 5000,
    queryFn: async () => {
      const response = await api.get<{
        success: boolean
        message?: string
        data: ProbeStatus
      }>(`/api/channel/${props.channelId}/group_scheduling/probes`)
      if (!response.data.success) throw new Error(response.data.message)
      return response.data.data
    },
  })
  const numericFields = [
    {
      name: 'cost_factor',
      label: t('Scheduling cost factor'),
      min: 0,
      max: undefined,
      step: 'any',
    },
    {
      name: 'interval_seconds',
      label: t('Probe interval (seconds)'),
      min: 1,
      max: 86400,
      step: 1,
    },
    {
      name: 'timeout_seconds',
      label: t('Probe timeout (seconds)'),
      min: 1,
      max: 600,
      step: 1,
    },
  ] as const
  return (
    <section
      className='space-y-4 border-t pt-4'
      aria-label={t('Group scheduling')}
    >
      <FormField
        control={props.form.control}
        name='group_scheduling.enabled'
        render={({ field }) => (
          <FormItem className='flex items-center justify-between gap-3'>
            <FormLabel>{t('Group scheduling')}</FormLabel>
            <FormControl>
              <Switch
                checked={field.value ?? false}
                onCheckedChange={field.onChange}
                disabled={props.disabled}
              />
            </FormControl>
          </FormItem>
        )}
      />
      <p className='text-muted-foreground text-sm'>
        {t(
          'Prefer lower first response time, then lower cost within the group threshold. Overrides priority and channel affinity only for selected groups and models.'
        )}
      </p>
      <fieldset
        disabled={props.disabled || !scheduling.enabled}
        className='space-y-4 disabled:opacity-60'
      >
        <FormField
          control={props.form.control}
          name='group_scheduling.groups'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Scheduling groups')}</FormLabel>
              <FormControl>
                <MultiSelect
                  options={groups.map((group) => ({
                    label: group,
                    value: group,
                  }))}
                  selected={field.value ?? []}
                  onChange={field.onChange}
                  disabled={props.disabled || !scheduling.enabled}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='group_scheduling.probe_models'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Probe models')}</FormLabel>
              <FormControl>
                <MultiSelect
                  options={models
                    .split(',')
                    .map((model) => model.trim())
                    .filter(Boolean)
                    .map((model) => ({ label: model, value: model }))}
                  selected={field.value ?? []}
                  onChange={field.onChange}
                  disabled={props.disabled || !scheduling.enabled}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <div className='grid gap-3 sm:grid-cols-3'>
          {numericFields.map((config) => (
            <FormField
              key={config.name}
              control={props.form.control}
              name={`group_scheduling.${config.name}`}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{config.label}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={config.min}
                      max={config.max}
                      step={config.step}
                      value={field.value ?? defaultGroupScheduling[config.name]}
                      onChange={(event) =>
                        field.onChange(Number(event.target.value))
                      }
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          ))}
        </div>
      </fieldset>
      {scheduling.enabled && (
        <div className='space-y-2 text-sm'>
          <p>
            {t(
              'Active probes use upstream tokens. Failed models stop receiving scheduling traffic until a probe succeeds.'
            )}
          </p>
          {scheduling.groups.map((group) => (
            <p key={group}>
              {group}: {t('First response tolerance')}:{' '}
              {(status.data?.thresholds[group] ?? 5000) / 1000} {t('Seconds')}
            </p>
          ))}
          {status.isPending && props.channelId !== null && (
            <LoadingState inline size='sm' message={t('Loading...')} />
          )}
          {status.isError && (
            <ErrorState
              className='min-h-0'
              title={t('Failed to load probe status')}
              onRetry={() => void status.refetch()}
            />
          )}
          {status.data?.probes.map((probe) => (
            <div
              key={probe.model}
              className='flex flex-wrap gap-x-3 gap-y-1 rounded border p-2'
            >
              <strong>{probe.model}</strong>
              <span>
                {probe.healthy ? t('Healthy') : t('Awaiting successful probe')}
              </span>
              {probe.healthy && <span>{probe.ttft_ms} ms</span>}
              <span>
                {t('Consecutive failures')}: {probe.failures}
              </span>
              <span>
                {t('Last probe')}:{' '}
                {probe.checked_at
                  ? new Date(probe.checked_at).toLocaleString()
                  : '—'}
              </span>
              <span>
                {t('Next probe')}:{' '}
                {probe.next_at ? new Date(probe.next_at).toLocaleString() : '—'}
              </span>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
