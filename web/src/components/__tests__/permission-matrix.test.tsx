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
import type { FormEvent } from 'react'
import { expect, it, vi } from 'vitest'

import { PermissionMatrix } from '@/components/permission-matrix'
import { Button } from '@/components/ui/button'
import type { PermissionResourceDef } from '@/lib/admin-permissions'

const resources: PermissionResourceDef[] = [
  {
    resource: 'audit',
    label_key: 'Audit Logs',
    actions: [
      {
        action: 'read',
        label_key: 'Read audit logs',
        description_key: 'View audit records without changing them',
      },
    ],
  },
]

it('keyboard focus reveals permission details without submitting or changing grants', async () => {
  const user = userEvent.setup()
  const onChange = vi.fn()
  const onSubmit = vi.fn((event: FormEvent<HTMLFormElement>) =>
    event.preventDefault()
  )
  render(
    <form onSubmit={onSubmit}>
      <PermissionMatrix resources={resources} value={{}} onChange={onChange} />
      <Button type='submit'>Save</Button>
    </form>
  )

  await user.tab()
  expect(screen.getByRole('button', { name: 'Details' })).toHaveFocus()
  expect(
    await screen.findByText('View audit records without changing them', {
      selector: 'dd',
    })
  ).toBeVisible()
  await user.keyboard('{Enter}')
  expect(onSubmit).not.toHaveBeenCalled()
  expect(onChange).not.toHaveBeenCalled()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(
      screen.queryByText('View audit records without changing them', {
        selector: 'dd',
      })
    ).not.toBeInTheDocument()
  )
  await user.tab()
  const toggle = screen.getByRole('button', { name: 'Read audit logs' })
  expect(toggle).toHaveFocus()
  expect(toggle).toHaveAttribute('aria-pressed', 'false')
  expect(toggle).toHaveAccessibleDescription(
    'View audit records without changing them'
  )
  await user.keyboard(' ')
  expect(onChange).toHaveBeenCalledWith({ audit: { read: true } })
  expect(onSubmit).not.toHaveBeenCalled()
})

it('disabled permissions cannot change grants while their details remain available', async () => {
  const user = userEvent.setup()
  const onChange = vi.fn()
  render(
    <PermissionMatrix
      resources={resources}
      value={{ audit: { read: true } }}
      onChange={onChange}
      disabled
    />
  )

  const toggle = screen.getByRole('button', { name: 'Read audit logs' })
  expect(toggle).toBeDisabled()
  expect(toggle).toHaveAttribute('aria-pressed', 'true')
  await user.click(toggle)
  expect(onChange).not.toHaveBeenCalled()
  await user.tab()
  expect(screen.getByRole('button', { name: 'Details' })).toHaveFocus()
  expect(
    await screen.findByText('View audit records without changing them', {
      selector: 'dd',
    })
  ).toBeVisible()
})
