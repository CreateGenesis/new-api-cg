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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { api } from '@/lib/api'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  defaultGroupScheduling,
  channelFormSchema,
  transformFormDataToCreatePayload,
  type ChannelFormValues,
} from '../../../../lib/channel-form'
import { ChannelGroupSchedulingSection } from '../channel-group-scheduling-section'

function SchedulingForm(props: {
  locked?: boolean
  enabled?: boolean
  channelId?: number
  onSave?: (value: string) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      models: 'm',
      group: ['default'],
      group_scheduling: {
        ...defaultGroupScheduling,
        enabled: props.enabled ?? false,
        groups: ['default'],
        probe_models: ['m'],
      },
    },
  })
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <Form {...form}>
        <form
          onSubmit={form.handleSubmit((values) =>
            props.onSave?.(
              String(transformFormDataToCreatePayload(values).channel.settings)
            )
          )}
        >
          <ChannelGroupSchedulingSection
            form={form}
            channelId={props.channelId ?? null}
            disabled={props.locked ?? false}
          />
          <Button type='submit'>Save</Button>
        </form>
      </Form>
    </QueryClientProvider>
  )
}

describe('group scheduling settings', () => {
  test('starts disabled and saves enabled scheduling with explicit zero cost', async () => {
    const save = vi.fn()
    const user = userEvent.setup()
    render(<SchedulingForm onSave={save} />)
    expect(
      screen.getByRole('spinbutton', { name: 'Scheduling cost factor' })
    ).toBeDisabled()
    const toggle = screen.getByRole('switch', { name: 'Group scheduling' })
    expect(toggle).not.toBeChecked()
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).toBeChecked()
    await user.clear(
      screen.getByRole('spinbutton', { name: 'Scheduling cost factor' })
    )
    await user.type(
      screen.getByRole('spinbutton', { name: 'Scheduling cost factor' }),
      '0'
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(save).toHaveBeenCalledOnce())
    expect(JSON.parse(save.mock.calls[0][0]).group_scheduling).toEqual({
      ...defaultGroupScheduling,
      enabled: true,
      groups: ['default'],
      probe_models: ['m'],
      cost_factor: 0,
    })
  })
  test('sensitive permission lock prevents changes', () => {
    render(<SchedulingForm locked enabled />)
    expect(
      screen.getByRole('switch', { name: 'Group scheduling' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByRole('spinbutton', { name: 'Probe interval (seconds)' })
    ).toBeDisabled()
  })
  test('loaded model health displays failure count and group threshold', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          thresholds: { default: 2000 },
          probes: [
            {
              model: 'm',
              healthy: false,
              ttft_ms: 0,
              checked_at: 1,
              next_at: 2,
              failures: 3,
            },
          ],
        },
      },
    })
    try {
      render(<SchedulingForm enabled channelId={123} />)
      expect(await screen.findByText('Awaiting successful probe')).toBeVisible()
      expect(screen.getByText('Consecutive failures: 3')).toBeVisible()
      expect(screen.getByText(/First response tolerance.*2/)).toBeVisible()
    } finally {
      get.mockRestore()
    }
  })
  test('rejects groups and models outside the channel and invalid intervals', () => {
    const valid = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'test',
      key: 'test',
      models: 'm',
      group: ['default'],
      group_scheduling: {
        ...defaultGroupScheduling,
        enabled: true,
        groups: ['default'],
        probe_models: ['m'],
      },
    }
    expect(channelFormSchema.safeParse(valid).success).toBe(true)
    for (const change of [
      { groups: ['vip'] },
      { probe_models: [] },
      { probe_models: ['other'] },
      { interval_seconds: 0 },
      { cost_factor: -1 },
    ]) {
      expect(
        channelFormSchema.safeParse({
          ...valid,
          group_scheduling: { ...valid.group_scheduling, ...change },
        }).success
      ).toBe(false)
    }
  })
})
