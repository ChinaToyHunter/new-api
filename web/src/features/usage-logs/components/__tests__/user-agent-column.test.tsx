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
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'

import { usageLogSchema } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { useCommonLogsColumns } from '../columns/common-logs-columns'

function UserAgentColumn(props: { other: LogOtherData }) {
  const table = useReactTable({
    data: [
      usageLogSchema.parse({
        id: 1,
        user_id: 1,
        created_at: 1788840000,
        type: 2,
        content: '',
        model_name: 'gpt-4o-mini',
        other: JSON.stringify(props.other),
      }),
    ],
    columns: useCommonLogsColumns(false, false),
    getCoreRowModel: getCoreRowModel(),
  })
  const header = table
    .getFlatHeaders()
    .find((item) => item.column.id === 'user_agent')
  const cell = table
    .getRowModel()
    .rows[0].getAllCells()
    .find((item) => item.column.id === 'user_agent')
  if (!header || !cell) {
    throw new Error('The log table must have a user agent column')
  }
  return (
    <table>
      <thead>
        <tr>
          <th>
            {flexRender(header.column.columnDef.header, header.getContext())}
          </th>
        </tr>
      </thead>
      <tbody>
        <tr>
          <td>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
        </tr>
      </tbody>
    </table>
  )
}

test('shows the recorded user agent on a single line in the log table', () => {
  const userAgent = 'claude-cli/2.1.0 (external, cli)'
  render(<UserAgentColumn other={{ user_agent: userAgent }} />)

  expect(screen.getByRole('columnheader', { name: 'User-Agent' })).toBeVisible()
  const cell = screen.getByRole('cell', { name: userAgent })
  expect(cell.querySelector('.truncate')).toBeInTheDocument()
})

test('leaves the user agent cell empty when the log recorded none', () => {
  render(<UserAgentColumn other={{}} />)

  expect(screen.getByRole('cell')).toBeEmptyDOMElement()
})
