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
import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'

import { SyncPriceCell } from '../upstream-price-cells'

it('keeps repeated tier labels and their distinct prices visible when the upstream expression changes', () => {
  const { rerender } = render(
    <SyncPriceCell
      values={{
        billing_mode: 'tiered_expr',
        billing_expr:
          'len <= 100 ? tier("shared", p * 2 + c * 3) : tier("shared", p * 4 + c * 5)',
      }}
    />
  )
  expect(screen.getByText('Length <= 100')).toBeVisible()
  expect(screen.getByText('shared')).toBeVisible()
  expect(
    screen.getAllByText(/^\$\d$/).map((price) => price.textContent)
  ).toEqual(['$2', '$3', '$4', '$5'])

  rerender(
    <SyncPriceCell
      values={{
        billing_mode: 'tiered_expr',
        billing_expr:
          'len <= 100 ? tier("shared", p * 6 + c * 7) : tier("shared", p * 8 + c * 9)',
      }}
    />
  )
  expect(
    screen.getAllByText(/^\$\d$/).map((price) => price.textContent)
  ).toEqual(['$6', '$7', '$8', '$9'])
  expect(screen.queryByText('$2')).not.toBeInTheDocument()
})
