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
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { assert, expect, test } from 'vitest'

import { GroupRatioVisualEditor } from '../group-ratio-visual-editor'

// This fork edits GroupRatio in the route-group table and TopupGroupRatio in the
// account-group table, so each case targets a row of its own table.
const ROUTE_ROW = 'route-decimals'
const ACCOUNT_ROW = 'account-decimals'

function PricingFixture() {
  const [settings, setSettings] = useState<Record<string, string>>({
    AccountGroups: `{"${ACCOUNT_ROW}":""}`,
    DefaultUserGroup: '',
    GroupRatio: `{"${ROUTE_ROW}":1}`,
    TopupGroupRatio: '{}',
    UserUsableGroups: '{}',
  })
  return (
    <>
      <GroupRatioVisualEditor
        accountGroups={settings.AccountGroups}
        defaultUserGroup={settings.DefaultUserGroup}
        groupRatio={settings.GroupRatio}
        topupGroupRatio={settings.TopupGroupRatio}
        userUsableGroups={settings.UserUsableGroups}
        groupGroupRatio='{}'
        autoGroups='[]'
        maxTokenAutoGroupsField={null}
        groupSpecialUsableGroup='{}'
        defaultUseAutoGroup={false}
        onChange={(field, value) =>
          setSettings((current) => ({ ...current, [field]: value }))
        }
      />
      <output aria-label='Saved ratios'>{JSON.stringify(settings)}</output>
    </>
  )
}

test.each([
  ['GroupRatio', 'Base ratio', ROUTE_ROW],
  ['TopupGroupRatio', 'Top-up ratio', ACCOUNT_ROW],
] as const)(
  '%s preserves typed decimals and accepts them as valid numeric ratios',
  async (key, columnHeader, rowName) => {
    const user = userEvent.setup()
    render(<PricingFixture />)
    const table = screen.getByText(columnHeader).closest('table')
    assert(table)
    const row = within(table)
      .getAllByRole('row')
      .find(
        (candidate) =>
          within(candidate).queryAllByDisplayValue(rowName).length > 0
      )
    assert(row)
    const input = within(row).getAllByRole('spinbutton')[0] as HTMLInputElement
    fireEvent.change(input, { target: { value: '0.0' } })
    expect(input.value).toBe('0.0')
    await user.clear(input)
    await user.type(input, '0.04')
    expect(input).toHaveValue(0.04)
    await user.tab()
    expect(input.checkValidity()).toBe(true)
    const saved = JSON.parse(
      screen.getByRole('status', { name: 'Saved ratios' }).textContent ?? '{}'
    )
    expect(JSON.parse(saved[key])).toEqual({ [rowName]: 0.04 })
    await user.clear(input)
    await user.type(input, '0.0001')
    expect(input).toHaveValue(0.0001)
    expect(input.checkValidity()).toBe(true)
    await user.clear(input)
    await user.type(input, '0.00001')
    expect(input.validity.stepMismatch).toBe(true)
    await user.clear(input)
    await user.type(input, '-0.04')
    expect(input.validity.rangeUnderflow).toBe(true)
  }
)
