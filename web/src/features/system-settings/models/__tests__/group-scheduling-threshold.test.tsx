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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState, type ComponentProps } from 'react'
import { useForm } from 'react-hook-form'
import { expect, test, vi } from 'vitest'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { GroupRatioForm } from '../group-ratio-form'

function ThresholdForm(props: {
  onSave: ComponentProps<typeof GroupRatioForm>['onSave']
}) {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  const form = useForm({
    defaultValues: {
      GroupSchedulingTolerance: '{}',
      GroupRatio: '{"default":1}',
      TopupGroupRatio: '{}',
      UserUsableGroups: '{}',
      GroupGroupRatio: '{}',
      AutoGroups: '[]',
      MaxTokenAutoGroups: 3,
      DefaultUseAutoGroup: false,
      GroupSpecialUsableGroup: '{}',
    },
  })
  return (
    <SettingsPageProvider actionsContainer={actions}>
      <div ref={setActions} />
      <GroupRatioForm form={form} onSave={props.onSave} isSaving={false} />
    </SettingsPageProvider>
  )
}

test('editing the group tolerance preserves edit mode and saves milliseconds', async () => {
  const onSave = vi.fn().mockResolvedValue(undefined)
  render(<ThresholdForm onSave={onSave} />)
  const input = screen.getByRole('spinbutton', { name: /default.*Seconds/ })
  expect(input).toHaveValue(5)
  fireEvent.click(input)
  fireEvent.change(input, { target: { value: '2.5' } })
  expect(
    screen.getByRole('button', { name: 'Switch to JSON' })
  ).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Save group ratios' }))
  await waitFor(() => expect(onSave).toHaveBeenCalled())
  expect(onSave.mock.calls[0][0].GroupSchedulingTolerance).toBe(
    '{"default":2500}'
  )
})
