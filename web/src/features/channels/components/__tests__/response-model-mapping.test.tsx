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
} from '../../lib/channel-form'
import { ResponseModelMappingFields } from '../response-model-mapping-fields'

function SettingsForm(props: {
  locked?: boolean
  enabled?: boolean
  onSave: (settings: string) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      response_model_mapping_enabled: props.enabled ?? false,
      response_model_mapping: '{"foo":"public-foo"}',
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
        <ResponseModelMappingFields
          sourceModelOptions={['foo', 'bar']}
          disabled={props.locked}
        />
        <Button type='submit'>Save</Button>
      </form>
    </Form>
  )
}

describe('response model mapping controls', () => {
  test('enables editing and saves a model-specific replacement', async () => {
    const onSave = vi.fn()
    const user = userEvent.setup()
    render(<SettingsForm onSave={onSave} />)
    const toggle = screen.getByRole('switch', {
      name: 'Rewrite response model',
    })
    const target = screen.getByRole('combobox', { name: 'Response model' })
    expect(toggle).not.toBeChecked()
    expect(target).toBeDisabled()
    await user.click(toggle)
    await user.clear(target)
    await user.type(target, 'published-foo')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(JSON.parse(onSave.mock.calls[0][0]).response_model_mapping).toEqual({
      enabled: true,
      mapping: { foo: 'published-foo' },
    })
  })

  test('disables with the keyboard and retains mappings when re-enabled', async () => {
    const onSave = vi.fn()
    const user = userEvent.setup()
    render(<SettingsForm onSave={onSave} enabled />)
    const toggle = screen.getByRole('switch', {
      name: 'Rewrite response model',
    })
    toggle.focus()
    await user.keyboard(' ')
    expect(
      screen.getByRole('combobox', { name: 'Response model' })
    ).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(JSON.parse(onSave.mock.calls[0][0]).response_model_mapping).toEqual({
      enabled: false,
      mapping: { foo: 'public-foo' },
    })
    await user.click(toggle)
    expect(
      screen.getByRole('combobox', { name: 'Response model' })
    ).toHaveValue('public-foo')
    expect(
      screen.getByRole('combobox', { name: 'Response model' })
    ).toBeEnabled()
  })

  test('allows an empty mapping after deleting the final row', async () => {
    const onSave = vi.fn()
    const user = userEvent.setup()
    render(<SettingsForm onSave={onSave} enabled />)
    await user.click(screen.getByRole('button', { name: 'Delete mapping' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(JSON.parse(onSave.mock.calls[0][0]).response_model_mapping).toEqual({
      enabled: true,
      mapping: {},
    })
  })

  test('locks both the switch and mapping controls when editing is restricted', () => {
    render(<SettingsForm onSave={vi.fn()} enabled locked />)
    expect(
      screen.getByRole('switch', { name: 'Rewrite response model' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByRole('combobox', { name: 'Client request model' })
    ).toBeDisabled()
    expect(
      screen.getByRole('combobox', { name: 'Response model' })
    ).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Add Mapping' })).toBeDisabled()
  })
})
