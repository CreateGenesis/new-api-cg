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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { DEFAULT_VIDEO_PROCESSING_CONFIG } from '../video-processing-config'
import { VideoProcessingSettingsSection } from '../video-processing-settings-section'

function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <VideoProcessingSettingsSection defaultValue='' />
      </SettingsPageProvider>
    </>
  )
}
function mount() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <Fixture />
    </QueryClientProvider>
  )
}

test('saving concurrency and adaptive controls sends one complete JSON option', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  mount()
  const concurrency = screen.getByRole('spinbutton', {
    name: 'Maximum video processing concurrency',
  })
  await user.clear(concurrency)
  await user.type(concurrency, '64')
  expect(concurrency).toHaveValue(64)
  await user.click(
    screen.getByRole('switch', {
      name: 'Automatically reduce video quality under load',
    })
  )
  expect(
    screen.getAllByRole('spinbutton', {
      name: 'Pixel retention multiplier (%)',
    })
  ).toHaveLength(2)
  await user.click(
    screen.getByRole('button', { name: 'Save video processing settings' })
  )
  await waitFor(() => expect(api.put).toHaveBeenCalledOnce())
  expect(api.put).toHaveBeenCalledWith('/api/option/', {
    key: 'video_processing_setting.config',
    value: JSON.stringify({
      ...DEFAULT_VIDEO_PROCESSING_CONFIG,
      max_concurrency: 64,
      adaptive_enabled: true,
    }),
  })
})

test('failed saves preserve manually entered limits and allow retry', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'test save failure' },
  })
  const user = userEvent.setup()
  mount()
  const input = screen.getByRole('spinbutton', {
    name: 'Maximum video processing concurrency',
  })
  fireEvent.change(input, { target: { value: '48' } })
  await user.click(
    screen.getByRole('button', { name: 'Save video processing settings' })
  )
  await waitFor(() => expect(api.put).toHaveBeenCalledOnce())
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Save video processing settings' })
    ).toBeEnabled()
  )
  expect(input).toHaveValue(48)
})
