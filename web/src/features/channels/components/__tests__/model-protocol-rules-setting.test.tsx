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
import { zodResolver } from '@hookform/resolvers/zod'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, test, vi } from 'vitest'

import { Form } from '@/components/ui/form'

import { CHANNEL_TYPE_OPENCODE_GO } from '../../constants'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { ModelProtocolRulesSetting } from '../model-protocol-rules-setting'

function SettingsForm(props: {
  channelType: number
  disabled?: boolean
  onSave: (values: ChannelFormValues) => void
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Gateway',
      models: 'glm-5.3',
      type: props.channelType,
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(props.onSave)}>
        <ModelProtocolRulesSetting
          channelType={props.channelType}
          disabled={props.disabled}
        />
        <button type='submit'>Save</button>
      </form>
    </Form>
  )
}

describe('Model protocol rules editor', () => {
  test('saves an edited JSON rule through the form', async () => {
    const user = userEvent.setup()
    const saved = vi.fn()
    render(
      <SettingsForm channelType={CHANNEL_TYPE_OPENCODE_GO} onSave={saved} />
    )
    const rules = '[{"pattern":"glm-*","protocol":"anthropic"}]'
    fireEvent.input(
      screen.getByRole('textbox', { name: 'Model protocol rules' }),
      {
        target: { value: rules },
      }
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(saved).toHaveBeenCalledWith(
      expect.objectContaining({ model_protocol_rules: rules }),
      expect.anything()
    )
  })

  test('invalid protocol shows a field error and prevents saving', async () => {
    const user = userEvent.setup()
    const saved = vi.fn()
    render(
      <SettingsForm channelType={CHANNEL_TYPE_OPENCODE_GO} onSave={saved} />
    )
    fireEvent.input(
      screen.getByRole('textbox', { name: 'Model protocol rules' }),
      {
        target: { value: '[{"pattern":"*","protocol":"unknown"}]' },
      }
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(saved).not.toHaveBeenCalled()
    expect(
      screen.getByRole('textbox', { name: 'Model protocol rules' })
    ).toHaveAttribute('aria-invalid', 'true')
  })

  test('locked settings disable the real JSON editor', () => {
    render(
      <SettingsForm
        channelType={CHANNEL_TYPE_OPENCODE_GO}
        disabled
        onSave={() => undefined}
      />
    )
    expect(
      screen.getByRole('textbox', { name: 'Model protocol rules' })
    ).toBeDisabled()
  })

  test('unrelated channel types do not expose model routing rules', () => {
    render(<SettingsForm channelType={1} onSave={() => undefined} />)
    expect(
      screen.queryByRole('textbox', { name: 'Model protocol rules' })
    ).not.toBeInTheDocument()
  })
})
