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
import { afterEach, describe, expect, test, vi } from 'vitest'

import * as settingsApi from '../../api'
import {
  WebToolsSettingsCard,
  type WebToolsSettingsValues,
} from '../web-tools-settings-card'

vi.mock('../../api', () => ({ updateSystemOption: vi.fn() }))

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn() },
}))

const defaults: WebToolsSettingsValues = {
  'web_tools_setting.enabled': false,
  'web_tools_setting.default_mode': 'native',
  'web_tools_setting.native_affinity_rules': [
    'codex cli trace',
    'claude cli trace',
  ],
  'web_tools_setting.tool_group': '',
  'web_tools_setting.max_calls_per_request': 8,
  'web_tools_setting.search_result_count': 5,
  'web_tools_setting.fetch_max_chars': 40000,
}

function renderCard() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  const view = render(
    <QueryClientProvider client={queryClient}>
      <WebToolsSettingsCard defaultValues={defaults} />
    </QueryClientProvider>
  )
  return { ...view, queryClient }
}

describe('web tools settings', () => {
  afterEach(() => {
    vi.mocked(settingsApi.updateSystemOption).mockReset()
  })

  test('saving sends only the changed options and stores the rules as a JSON list', async () => {
    vi.mocked(settingsApi.updateSystemOption).mockResolvedValue({
      success: true,
      message: '',
    })
    const { container, queryClient } = renderCard()

    fireEvent.click(screen.getByRole('switch'))
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Native affinity rules' }),
      { target: { value: 'claude cli trace\n\n  team rule  ' } }
    )
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)

    await waitFor(() =>
      expect(settingsApi.updateSystemOption).toHaveBeenCalledTimes(2)
    )
    expect(settingsApi.updateSystemOption).toHaveBeenNthCalledWith(1, {
      key: 'web_tools_setting.enabled',
      value: true,
    })
    expect(settingsApi.updateSystemOption).toHaveBeenNthCalledWith(2, {
      key: 'web_tools_setting.native_affinity_rules',
      value: '["claude cli trace","team rule"]',
    })
    queryClient.clear()
  })

  test('a call limit above the backend bound is not saved', async () => {
    const { container, queryClient } = renderCard()

    const limit = screen.getByRole('spinbutton', {
      name: 'Max tool calls per request',
    })
    fireEvent.change(limit, { target: { value: '64' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)

    await waitFor(() => expect(limit).toHaveAttribute('aria-invalid', 'true'))
    expect(settingsApi.updateSystemOption).not.toHaveBeenCalled()
    queryClient.clear()
  })
})
