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
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../lib/channel-form'
import { supportsFakeStream } from '../lib/fake-stream'

export function FakeStreamSetting(props: {
  channelType: number
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()
  const passThrough = useWatch({
    control: form.control,
    name: 'pass_through_body_enabled',
  })
  if (!supportsFakeStream(props.channelType)) return null
  const inactive = props.disabled || passThrough === true

  return (
    <FormField
      control={form.control}
      name='fake_stream_enabled'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between gap-4 px-4 py-3'>
          <div className='space-y-0.5'>
            <FormLabel>{t('Enable Fake Streaming')}</FormLabel>
            <FormDescription>
              {t(
                'For OpenAI Chat Completions stream requests, call the upstream without streaming and return the complete response as SSE. Only for Sub2API and New API channels; requires Pass Through Body off and no Responses API conversion.'
              )}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              checked={field.value === true}
              onCheckedChange={field.onChange}
              disabled={inactive}
              onBlur={field.onBlur}
              name={field.name}
              ref={field.ref}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )
}
