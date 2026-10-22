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
import { expect, test } from 'vitest'

import { channelSchema } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from '../channel-form'

const storedChannel = {
  other: '',
  remark: '',
  id: 7,
  status: 1,
  created_time: 1,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
}

function channelForm(overrides: Record<string, unknown>) {
  return channelFormSchema.parse({
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'OpenAI',
    type: 1,
    key: 'test-key',
    models: 'gpt-5.5',
    ...overrides,
  })
}

test.each(['native', 'gateway'] as const)(
  'a %s web tools mode round-trips without dropping other settings',
  (mode) => {
    const payload = transformFormDataToCreatePayload(
      channelForm({ web_tools_mode: mode, settings: '{"custom_option":true}' })
    ).channel
    expect(JSON.parse(payload.settings ?? '{}')).toMatchObject({
      web_tools_mode: mode,
      custom_option: true,
    })

    const channel = channelSchema.parse({ ...payload, ...storedChannel })
    expect(transformChannelToFormDefaults(channel).web_tools_mode).toBe(mode)
  }
)

test('inheriting the global default removes the channel web tools mode', () => {
  const settings = JSON.parse(
    transformFormDataToCreatePayload(
      channelForm({
        web_tools_mode: '',
        settings: '{"web_tools_mode":"gateway","custom_option":true}',
      })
    ).channel.settings ?? '{}'
  )
  expect(settings).not.toHaveProperty('web_tools_mode')
  expect(settings).toMatchObject({ custom_option: true })
})

test('an unknown stored web tools mode loads as inheriting the global default', () => {
  const payload = transformFormDataToCreatePayload(channelForm({})).channel
  const channel = channelSchema.parse({
    ...payload,
    ...storedChannel,
    settings: '{"web_tools_mode":"always"}',
  })
  expect(transformChannelToFormDefaults(channel).web_tools_mode).toBe('')
})
