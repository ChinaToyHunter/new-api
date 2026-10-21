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
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { JsonCodeEditor } from '@/components/json-code-editor'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'

import { MODEL_ROUTED_CHANNEL_TYPES } from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'

export function ModelProtocolRulesSetting(props: {
  channelType: number
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()
  if (!MODEL_ROUTED_CHANNEL_TYPES.has(props.channelType)) return null

  return (
    <FormField
      control={form.control}
      name='model_protocol_rules'
      render={({ field }) => (
        <FormItem className='space-y-3 border-t pt-4'>
          <FormLabel>{t('Model protocol rules')}</FormLabel>
          <FormDescription>
            {t(
              'Leave empty to use built-in model routing. Rules are checked in order; unmatched models use the upstream catalog or Chat Completions. Use protocol for the preferred endpoint and protocols for other supported endpoints.'
            )}
          </FormDescription>
          <FormControl>
            <JsonCodeEditor
              value={field.value || ''}
              onChange={field.onChange}
              name={field.name}
              onBlur={field.onBlur}
              textareaRef={field.ref}
              disabled={props.disabled}
              ariaLabel={t('Model protocol rules')}
              placeholder='[{"pattern":"claude-*","protocol":"anthropic"}]'
              heightClassName='h-40 min-h-40 max-h-40'
            />
          </FormControl>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
