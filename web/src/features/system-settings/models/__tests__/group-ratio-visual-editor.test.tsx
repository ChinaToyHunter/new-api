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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { beforeEach, describe, expect, test } from 'vitest'

import { GroupRatioVisualEditor } from '../group-ratio-visual-editor'

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type Change = { field: string; value: string }

const baseProps = {
  accountGroups: '{"default":"Default account","vip":"VIP account"}',
  defaultUserGroup: 'default',
  groupRatio: '{"default":1,"vip":2}',
  topupGroupRatio: '{"default":1.5,"orphan-topup":3}',
  userUsableGroups:
    '{"default":"Default route","vip":"VIP route","orphan-route":"Legacy route","auto":"Automatic"}',
  groupGroupRatio: '{"default":{"default":1.1},"vip":{"vip":2.2}}',
  autoGroups: '["vip","auto"]',
  maxTokenAutoGroupsField: (
    <label>
      Max auto groups
      <input />
    </label>
  ),
  groupSpecialUsableGroup: '{}',
  defaultUseAutoGroup: false,
}

function renderEditor(changes: Change[]) {
  return render(
    <I18nextProvider i18n={i18n}>
      <GroupRatioVisualEditor
        {...baseProps}
        onChange={(field, value) => changes.push({ field, value })}
      />
    </I18nextProvider>
  )
}

function latestChange(changes: Change[], field: string): string {
  const change = [...changes].reverse().find((item) => item.field === field)
  if (!change) throw new Error(`No ${field} change was emitted`)
  return change.value
}

function inputByValue(value: string): HTMLInputElement {
  const input = screen
    .getAllByDisplayValue(value)
    .find(
      (element): element is HTMLInputElement =>
        element instanceof HTMLInputElement
    )
  if (!input) throw new Error(`Expected input with value ${value}`)
  return input
}

beforeEach(() => {
  i18n.changeLanguage('en')
})

describe('group ratio visual editor preservation', () => {
  test('preserves top-up-only entries when editing an account description', async () => {
    const changes: Change[] = []
    const user = userEvent.setup()
    renderEditor(changes)

    const description = inputByValue('Default account')
    await user.clear(description)
    await user.type(description, 'Default account updated')

    const topup = JSON.parse(
      latestChange(changes, 'TopupGroupRatio')
    ) as Record<string, number>
    expect(topup).toEqual({ default: 1.5, 'orphan-topup': 3 })
  })

  test('preserves route-only entries and auto without promoting either into GroupRatio', async () => {
    const changes: Change[] = []
    const user = userEvent.setup()
    renderEditor(changes)

    const description = inputByValue('Default route')
    await user.clear(description)
    await user.type(description, 'Default route updated')

    const usable = JSON.parse(
      latestChange(changes, 'UserUsableGroups')
    ) as Record<string, string>
    const ratios = JSON.parse(latestChange(changes, 'GroupRatio')) as Record<
      string,
      number
    >
    expect(usable).toMatchObject({
      default: 'Default route updated',
      vip: 'VIP route',
      'orphan-route': 'Legacy route',
      auto: 'Automatic',
    })
    expect(ratios).toEqual({ default: 1, vip: 2 })
  })

  test('does not render route-only entries as editable route rows', () => {
    const changes: Change[] = []
    renderEditor(changes)

    const routeCard = screen
      .getByText('Route and billing groups')
      .closest('[class]')
    expect(routeCard).toBeTruthy()
    expect(
      within(routeCard as HTMLElement).queryByDisplayValue('Legacy route')
    ).toBeNull()
    expect(screen.queryAllByDisplayValue('Legacy route')).toHaveLength(0)
    expect(screen.queryAllByDisplayValue('Automatic')).toHaveLength(0)
  })

  test('removes a main account row without deleting unrelated top-up orphan entries', async () => {
    const changes: Change[] = []
    const user = userEvent.setup()
    renderEditor(changes)

    const accountCard = screen.getByText('Account groups').closest('[class]')
    expect(accountCard).toBeTruthy()
    const defaultDescription = inputByValue('Default account')
    const row = defaultDescription.closest('tr')
    expect(row).toBeTruthy()
    await user.click(
      within(row as HTMLElement).getByRole('button', {
        name: 'Delete account group',
      })
    )

    const accountGroups = JSON.parse(
      latestChange(changes, 'AccountGroups')
    ) as Record<string, string>
    const topup = JSON.parse(
      latestChange(changes, 'TopupGroupRatio')
    ) as Record<string, number>
    expect(accountGroups).not.toHaveProperty('default')
    expect(topup).toEqual({ 'orphan-topup': 3 })
  })

  test('does not serialize a negative top-up ratio entered in visual mode', async () => {
    const changes: Change[] = []
    const user = userEvent.setup()
    renderEditor(changes)

    const topup = inputByValue('1.5')
    await user.clear(topup)
    await user.type(topup, '-0.5')

    expect(JSON.parse(latestChange(changes, 'TopupGroupRatio'))).toEqual({
      'orphan-topup': 3,
    })
  })
})
