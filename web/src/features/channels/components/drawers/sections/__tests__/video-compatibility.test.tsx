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
import { zodResolver } from '@hookform/resolvers/zod'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformFormDataToCreatePayload,
  type ChannelFormValues,
} from '../../../../lib/channel-form'
import { ChannelFallbackPolicyFields } from '../channel-fallback-policy-fields'

function Fixture(props: {
  locked?: boolean
  disabled?: boolean
  type?: number
  onSave: (settings: string) => void
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'video',
      type: props.type ?? 1,
      models: 'k3',
      key: 'test-key',
      disable_video_understanding: props.disabled ?? false,
    },
  })
  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit((values) =>
          props.onSave(
            String(transformFormDataToCreatePayload(values).channel.settings)
          )
        )}
      >
        <ChannelFallbackPolicyFields
          control={form.control}
          sensitiveLocked={props.locked ?? false}
        />
        <Button type='submit'>Save</Button>
      </form>
    </Form>
  )
}

describe('channel video compatibility controls', () => {
  test('enabling extraction reveals quality controls and saves the manually selected pixel percentage', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    render(<Fixture onSave={onSave} />)
    expect(
      screen.queryByRole('spinbutton', { name: 'Total pixels retained (%)' })
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole('switch', {
        name: 'Video compatibility (frame extraction)',
      })
    )
    const input = screen.getByRole('spinbutton', {
      name: 'Total pixels retained (%)',
    })
    await user.clear(input)
    await user.type(input, '25')
    expect(input).toHaveValue(25)
    expect(
      screen.getByRole('switch', {
        name: 'Disable video understanding requests',
      })
    ).toHaveAttribute('aria-disabled', 'true')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(JSON.parse(onSave.mock.calls[0][0])).toHaveProperty(
      'video_compatibility.pixel_percent',
      25
    )
  })
  test('disabled video understanding prevents enabling compatibility', () => {
    render(<Fixture disabled onSave={vi.fn()} />)
    expect(
      screen.getByRole('switch', {
        name: 'Video compatibility (frame extraction)',
      })
    ).toHaveAttribute('aria-disabled', 'true')
  })
  test('sensitive lock prevents changing extraction with the keyboard', async () => {
    const user = userEvent.setup()
    render(<Fixture locked onSave={vi.fn()} />)
    const toggle = screen.getByRole('switch', {
      name: 'Video compatibility (frame extraction)',
    })
    expect(toggle).toHaveAttribute('aria-disabled', 'true')
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).not.toBeChecked()
  })
  test('task plugin channels expose why extraction is unavailable and prevent enablement', () => {
    render(<Fixture type={61} onSave={vi.fn()} />)
    expect(
      screen.getByRole('switch', {
        name: 'Video compatibility (frame extraction)',
      })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByText(
        'video_compatibility is unavailable for task plugin channels'
      )
    ).toBeVisible()
  })
})
