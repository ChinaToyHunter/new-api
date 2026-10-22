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
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsControlGroup,
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { safeNumberFieldProps } from '../utils/numeric-field'

const webToolsSchema = z.object({
  web_tools_setting: z.object({
    enabled: z.boolean(),
    default_mode: z.enum(['native', 'gateway']),
    native_affinity_rules: z.string(),
    tool_group: z.string(),
    max_calls_per_request: z.number().int().min(1).max(32),
    search_result_count: z.number().int().min(1).max(20),
    fetch_max_chars: z.number().int().min(1000).max(200000),
  }),
})

type WebToolsFormInput = z.input<typeof webToolsSchema>
type WebToolsFormValues = z.output<typeof webToolsSchema>

export type WebToolsSettingsValues = {
  'web_tools_setting.enabled': boolean
  'web_tools_setting.default_mode': string
  'web_tools_setting.native_affinity_rules': string[]
  'web_tools_setting.tool_group': string
  'web_tools_setting.max_calls_per_request': number
  'web_tools_setting.search_result_count': number
  'web_tools_setting.fetch_max_chars': number
}

const buildFormDefaults = (
  defaults: WebToolsSettingsValues
): WebToolsFormInput => ({
  web_tools_setting: {
    enabled: defaults['web_tools_setting.enabled'],
    default_mode:
      defaults['web_tools_setting.default_mode'] === 'gateway'
        ? 'gateway'
        : 'native',
    native_affinity_rules:
      defaults['web_tools_setting.native_affinity_rules'].join('\n'),
    tool_group: defaults['web_tools_setting.tool_group'],
    max_calls_per_request: defaults['web_tools_setting.max_calls_per_request'],
    search_result_count: defaults['web_tools_setting.search_result_count'],
    fetch_max_chars: defaults['web_tools_setting.fetch_max_chars'],
  },
})

const normalizeFormValues = (
  values: WebToolsFormValues
): WebToolsSettingsValues => ({
  'web_tools_setting.enabled': values.web_tools_setting.enabled,
  'web_tools_setting.default_mode': values.web_tools_setting.default_mode,
  'web_tools_setting.native_affinity_rules':
    values.web_tools_setting.native_affinity_rules
      .split('\n')
      .map((rule) => rule.trim())
      .filter(Boolean),
  'web_tools_setting.tool_group': values.web_tools_setting.tool_group.trim(),
  'web_tools_setting.max_calls_per_request':
    values.web_tools_setting.max_calls_per_request,
  'web_tools_setting.search_result_count':
    values.web_tools_setting.search_result_count,
  'web_tools_setting.fetch_max_chars': values.web_tools_setting.fetch_max_chars,
})

type WebToolsSettingsCardProps = {
  defaultValues: WebToolsSettingsValues
}

export function WebToolsSettingsCard(props: WebToolsSettingsCardProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const baselineRef = useRef<WebToolsSettingsValues>(props.defaultValues)

  const formDefaults = useMemo(
    () => buildFormDefaults(props.defaultValues),
    [props.defaultValues]
  )

  const form = useForm<WebToolsFormInput, unknown, WebToolsFormValues>({
    resolver: zodResolver(webToolsSchema),
    defaultValues: formDefaults,
  })

  useEffect(() => {
    baselineRef.current = props.defaultValues
    form.reset(buildFormDefaults(props.defaultValues))
  }, [props.defaultValues, form])

  const onSubmit = async (values: WebToolsFormValues) => {
    const normalized = normalizeFormValues(values)
    const changedKeys = (
      Object.keys(normalized) as Array<keyof WebToolsSettingsValues>
    ).filter(
      (key) =>
        JSON.stringify(normalized[key]) !==
        JSON.stringify(baselineRef.current[key])
    )

    if (changedKeys.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const key of changedKeys) {
      const value = normalized[key]
      await updateOption.mutateAsync({
        key,
        value: Array.isArray(value) ? JSON.stringify(value) : value,
      })
    }
    baselineRef.current = normalized
  }

  return (
    <SettingsSection title={t('Web Tools')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />
          <FormField
            control={form.control}
            name='web_tools_setting.enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>
                    {t('Run hosted web tools in the gateway')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Runs hosted web search and page fetch with the DeepSeek Search and Fetch APIs through the channels serving deepseek-search and deepseek-fetch. Calls are billed at the web_search and web_fetch tool prices.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <FormField
            control={form.control}
            name='web_tools_setting.default_mode'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Default web tools mode')}</FormLabel>
                <Select
                  items={[
                    {
                      value: 'native',
                      label: t('Native: upstream runs the tools'),
                    },
                    {
                      value: 'gateway',
                      label: t('Gateway: New API runs the tools'),
                    },
                  ]}
                  value={field.value}
                  onValueChange={(value) =>
                    field.onChange(value === 'gateway' ? 'gateway' : 'native')
                  }
                >
                  <FormControl>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      <SelectItem value='native'>
                        {t('Native: upstream runs the tools')}
                      </SelectItem>
                      <SelectItem value='gateway'>
                        {t('Gateway: New API runs the tools')}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t('Applies to channels without their own web tools mode.')}
                </FormDescription>
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='web_tools_setting.native_affinity_rules'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Native affinity rules')}</FormLabel>
                <FormControl>
                  <Textarea rows={3} {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Requests matched by these channel affinity rules keep the upstream tools on channels without their own mode, such as the Claude Code and Codex CLI traces. One rule name per line.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='web_tools_setting.tool_group'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Tool channel group')}</FormLabel>
                <FormControl>
                  <Input {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Group whose deepseek-search and deepseek-fetch channels run gateway calls. Empty uses the group of the request.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <SettingsControlGroup>
            <FormField
              control={form.control}
              name='web_tools_setting.max_calls_per_request'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Max tool calls per request')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      max={32}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Up to 32 gateway tool calls in one request.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='web_tools_setting.search_result_count'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Search results per call')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      max={20}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Up to 20 results for each gateway search.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='web_tools_setting.fetch_max_chars'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Fetch character limit')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1000}
                      max={200000}
                      step={1000}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Page text passed to the model, 1,000 to 200,000 characters.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </SettingsControlGroup>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
