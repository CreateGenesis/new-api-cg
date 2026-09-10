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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToCreatePayload,
  type ChannelFormValues,
} from '../../../../lib/channel-form'
import { ChannelFallbackPolicyFields } from '../channel-fallback-policy-fields'

function SettingsForm(props: {
  locked?: boolean
  enabled?: boolean
  disableStream?: boolean
  onSave: (settings: string) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      disable_video_understanding: props.enabled ?? false,
      disable_stream: props.disableStream ?? false,
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

describe('video understanding switch', () => {
  test('starts off and saves an enabled switch', async () => {
    const onSave = vi.fn()
    const user = userEvent.setup()
    render(<SettingsForm onSave={onSave} />)
    const toggle = screen.getByRole('switch', {
      name: 'Disable video understanding requests',
    })
    expect(toggle).not.toBeChecked()
    await user.click(toggle)
    expect(toggle).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(JSON.parse(onSave.mock.calls[0][0])).toHaveProperty(
      'disable_video_understanding',
      true
    )
  })

  test('keyboard can disable the saved setting independently of the stream switch', async () => {
    const onSave = vi.fn()
    const user = userEvent.setup()
    render(<SettingsForm onSave={onSave} enabled disableStream />)
    const toggle = screen.getByRole('switch', {
      name: 'Disable video understanding requests',
    })
    expect(toggle).toBeChecked()
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).not.toBeChecked()
    expect(
      screen.getByRole('switch', { name: 'Disable stream requests' })
    ).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(JSON.parse(onSave.mock.calls[0][0])).not.toHaveProperty(
      'disable_video_understanding'
    )
  })

  test('locked settings prevent changing the switch', async () => {
    const user = userEvent.setup()
    render(<SettingsForm onSave={vi.fn()} enabled locked />)
    const toggle = screen.getByRole('switch', {
      name: 'Disable video understanding requests',
    })
    expect(toggle).toHaveAttribute('aria-disabled', 'true')
    await user.click(toggle)
    expect(toggle).toBeChecked()
  })
})
