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
import { describe, expect, test } from 'vitest'

import { createGroupSchema } from '../ratio-settings-card'

const t = (key: string) => key

const validValues = {
  AccountGroups: '{"default":"Default"}',
  DefaultUserGroup: 'default',
  GroupRatio: '{"default":1}',
  TopupGroupRatio: '{"default":1}',
  UserUsableGroups: '{"default":"Default"}',
  GroupGroupRatio: '{"default":{"default":1}}',
  AutoGroups: '["default"]',
  MaxTokenAutoGroups: 1,
  DefaultUseAutoGroup: false,
  GroupSpecialUsableGroup: '{}',
}

describe('group ratio JSON validation', () => {
  test.each([
    ['GroupRatio', '{"default":-0.1}'],
    ['TopupGroupRatio', '{"default":-0.1}'],
    ['GroupGroupRatio', '{"default":{"default":-0.1}}'],
  ] as const)('rejects negative values in %s', (field, value) => {
    const result = createGroupSchema(t).safeParse({
      ...validValues,
      [field]: value,
    })

    expect(result.success).toBe(false)
    if (result.success) return
    expect(result.error.issues).toEqual(
      expect.arrayContaining([expect.objectContaining({ path: [field] })])
    )
  })

  test('accepts zero ratios for existing free and fallback semantics', () => {
    const result = createGroupSchema(t).safeParse({
      ...validValues,
      GroupRatio: '{"free":0}',
      TopupGroupRatio: '{"free":0}',
      GroupGroupRatio: '{"default":{"free":0}}',
    })

    expect(result.success).toBe(true)
  })
})
