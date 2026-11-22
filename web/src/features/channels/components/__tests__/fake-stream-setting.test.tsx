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
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { useForm } from 'react-hook-form'
import { I18nextProvider } from 'react-i18next'
import { describe, expect, test } from 'vitest'

import { Form } from '@/components/ui/form'
import { resources } from '@/i18n/config'

import { CHANNEL_TYPE_NEW_API, CHANNEL_TYPE_SUB2API } from '../../constants'
import {
  buildSettingJSON,
  CHANNEL_FORM_DEFAULT_VALUES,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { channelSchema } from '../../types'
import { FakeStreamSetting } from '../fake-stream-setting'

const LABEL = 'Enable Fake Streaming'
const DESCRIPTION =
  'For OpenAI Chat Completions stream requests, call the upstream without streaming and return the complete response as SSE. Only for Sub2API and New API channels; requires Pass Through Body off and no Responses API conversion.'
const SUPPORTED = [CHANNEL_TYPE_SUB2API, CHANNEL_TYPE_NEW_API]
const UNSUPPORTED = [1, 3, 14, 57, 61, 62, 64]

function SettingsForm(props: {
  channelType: number
  disabled?: boolean
  enabled?: boolean
  passThrough?: boolean
  onSave: (value: string) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: props.channelType,
      fake_stream_enabled: props.enabled,
      pass_through_body_enabled: props.passThrough,
    },
  })
  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit((values) =>
          props.onSave(buildSettingJSON(values))
        )}
      >
        <FakeStreamSetting
          channelType={props.channelType}
          disabled={props.disabled}
        />
        <button type='submit'>Save</button>
      </form>
    </Form>
  )
}

function channelWithSetting(channelType: number, setting: unknown) {
  return channelSchema.parse({
    id: 1,
    name: 'Test channel',
    key: '',
    type: channelType,
    status: 1,
    created_time: 0,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    setting: JSON.stringify(setting),
  })
}

describe('Fake Streaming channel setting', () => {
  describe.each(SUPPORTED)('channel type %s', (channelType) => {
    test.each([undefined, false, true])(
      'preserves setting %s through create, update and reload',
      (fakeStream) => {
        const channel = channelWithSetting(channelType, {
          fake_stream: fakeStream,
        })
        const values = transformChannelToFormDefaults(channel)
        expect(values.fake_stream_enabled).toBe(fakeStream === true)
        for (const payload of [
          transformFormDataToCreatePayload(values).channel,
          transformFormDataToUpdatePayload(values, channel.id),
        ]) {
          expect(typeof payload.setting).toBe('string')
          const setting = JSON.parse(payload.setting ?? '{}')
          expect(setting.fake_stream).toBe(fakeStream === true)
          expect(
            transformChannelToFormDefaults({
              ...channel,
              setting: payload.setting,
            }).fake_stream_enabled
          ).toBe(fakeStream === true)
        }
      }
    )

    test('exposes an accessible switch and saves on and off', async () => {
      const user = userEvent.setup()
      const saved: string[] = []
      render(
        <SettingsForm
          channelType={channelType}
          onSave={(value) => saved.push(value)}
        />
      )
      const toggle = screen.getByRole('switch', { name: LABEL })
      expect(toggle).not.toBeChecked()
      await user.click(toggle)
      await user.click(screen.getByRole('button', { name: 'Save' }))
      expect(JSON.parse(saved[0]).fake_stream).toBe(true)
      toggle.focus()
      await user.keyboard(' ')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      expect(JSON.parse(saved[1]).fake_stream).toBe(false)
    })

    test('pass-through body disables the switch and keeps the saved key false', async () => {
      const user = userEvent.setup()
      const saved: string[] = []
      render(
        <SettingsForm
          channelType={channelType}
          enabled
          passThrough
          onSave={(value) => saved.push(value)}
        />
      )
      const toggle = screen.getByRole('switch', { name: LABEL })
      expect(toggle).toHaveAttribute('aria-disabled', 'true')
      expect(toggle).toBeChecked()
      await user.click(screen.getByRole('button', { name: 'Save' }))
      expect(JSON.parse(saved[0])).toMatchObject({
        pass_through_body_enabled: true,
        fake_stream: false,
      })
    })

    test('disabled settings lock the switch', async () => {
      const user = userEvent.setup()
      render(
        <SettingsForm
          channelType={channelType}
          disabled
          onSave={() => undefined}
        />
      )
      const toggle = screen.getByRole('switch', { name: LABEL })
      expect(toggle).toHaveAttribute('aria-disabled', 'true')
      await user.click(toggle)
      expect(toggle).not.toBeChecked()
    })
  })

  test.each(UNSUPPORTED)(
    'channel type %s hides the switch and never emits fake_stream true',
    async (channelType) => {
      const user = userEvent.setup()
      const saved: string[] = []
      render(
        <SettingsForm
          channelType={channelType}
          enabled
          onSave={(value) => saved.push(value)}
        />
      )
      expect(
        screen.queryByRole('switch', { name: LABEL })
      ).not.toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: 'Save' }))
      expect(JSON.parse(saved[0]).fake_stream).toBe(false)

      const defaults = transformChannelToFormDefaults(
        channelWithSetting(channelType, { fake_stream: true })
      )
      expect(defaults.fake_stream_enabled).toBe(false)
      expect(
        JSON.parse(buildSettingJSON({ ...defaults, fake_stream_enabled: true }))
          .fake_stream
      ).toBe(false)
    }
  )

  test.each(['en', 'zhCN', 'zhTW', 'fr', 'ru', 'ja', 'vi'])(
    'locale %s translates both keys',
    (language) => {
      const bundle = resources[language as keyof typeof resources]
      expect(bundle.translation[LABEL]).toBeTruthy()
      expect(bundle.translation[LABEL]).not.toBe(
        language === 'en' ? undefined : LABEL
      )
      expect(bundle.translation[DESCRIPTION]).toBeTruthy()
      expect(bundle.translation[DESCRIPTION]).not.toBe(
        language === 'en' ? undefined : DESCRIPTION
      )
    }
  )

  test('renders the translated label and description for non-English locales', async () => {
    const i18n = createInstance()
    await i18n.init({
      lng: 'zhCN',
      resources,
      keySeparator: false,
      nsSeparator: false,
    })
    render(
      <I18nextProvider i18n={i18n}>
        <SettingsForm
          channelType={CHANNEL_TYPE_SUB2API}
          onSave={() => undefined}
        />
      </I18nextProvider>
    )
    expect(
      screen.getByRole('switch', { name: resources.zhCN.translation[LABEL] })
    ).toBeInTheDocument()
    expect(
      screen.getByText(resources.zhCN.translation[DESCRIPTION])
    ).toBeInTheDocument()
  })
})
