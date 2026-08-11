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
import { Add01Icon, Delete02Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useFieldArray, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import {
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { RESPONSE_CONTENT_RETRY_MAX_RULES } from './response-content-retry-policy'
import type {
  RoutingReliabilityFormInput,
  RoutingReliabilityFormValues,
} from './routing-reliability-schema'

type ResponseContentRetryEditorProps = {
  form: UseFormReturn<
    RoutingReliabilityFormInput,
    unknown,
    RoutingReliabilityFormValues
  >
  disabled: boolean
}

export function ResponseContentRetryEditor(
  props: ResponseContentRetryEditorProps
) {
  const { t } = useTranslation()
  const rules = useFieldArray({
    control: props.form.control,
    name: 'response_content_retry_policy.rules',
  })

  return (
    <div className='flex min-w-0 flex-col gap-4'>
      <div className='flex flex-col gap-1'>
        <h4 className='text-sm font-medium'>
          {t('Response content fallback')}
        </h4>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Retry another channel when a successful response matches a rule.'
          )}
        </p>
      </div>

      <FormField
        control={props.form.control}
        name='response_content_retry_policy.enabled'
        render={({ field }) => (
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <FormLabel>{t('Enable response content fallback')}</FormLabel>
            </SettingsSwitchContent>
            <FormControl>
              <Switch
                checked={field.value}
                disabled={props.disabled}
                onCheckedChange={field.onChange}
              />
            </FormControl>
          </SettingsSwitchItem>
        )}
      />

      <div className='flex min-w-0 flex-col'>
        {rules.fields.map((rule, index) => (
          <div
            key={rule.id}
            className='grid min-w-0 gap-3 border-b py-3 first:pt-0 sm:grid-cols-[11rem_minmax(0,1fr)_2rem] sm:items-start'
          >
            <FormField
              control={props.form.control}
              name={`response_content_retry_policy.rules.${index}.mode`}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Match mode')}</FormLabel>
                  <FormControl>
                    <ToggleGroup
                      value={[field.value]}
                      disabled={props.disabled}
                      onValueChange={(values) => {
                        const nextValue = values.find(
                          (value) => value !== field.value
                        )
                        if (nextValue) field.onChange(nextValue)
                      }}
                      aria-label={t('Match mode')}
                      variant='outline'
                      size='sm'
                      className='grid w-full grid-cols-2'
                    >
                      <ToggleGroupItem value='prefix' className='w-full'>
                        {t('Prefix')}
                      </ToggleGroupItem>
                      <ToggleGroupItem value='exact' className='w-full'>
                        {t('Exact')}
                      </ToggleGroupItem>
                    </ToggleGroup>
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={props.form.control}
              name={`response_content_retry_policy.rules.${index}.content`}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Rule content')}</FormLabel>
                  <FormControl>
                    <Input
                      value={field.value}
                      disabled={props.disabled}
                      placeholder={t('Response text to match')}
                      onChange={(event) => field.onChange(event.target.value)}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon'
                    disabled={props.disabled}
                    onClick={() => rules.remove(index)}
                    aria-label={t('Delete rule')}
                    className='text-muted-foreground hover:text-destructive justify-self-end sm:mt-6'
                  />
                }
              >
                <HugeiconsIcon icon={Delete02Icon} strokeWidth={2} />
              </TooltipTrigger>
              <TooltipContent>{t('Delete rule')}</TooltipContent>
            </Tooltip>
          </div>
        ))}
      </div>

      <FormField
        control={props.form.control}
        name='response_content_retry_policy.rules'
        render={() => (
          <FormItem>
            <FormDescription>
              {t('Rules are case-sensitive and ignore leading whitespace.')}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />

      <div>
        <Button
          type='button'
          variant='outline'
          disabled={
            props.disabled ||
            rules.fields.length >= RESPONSE_CONTENT_RETRY_MAX_RULES
          }
          onClick={() => rules.append({ mode: 'prefix', content: '' })}
        >
          <HugeiconsIcon
            icon={Add01Icon}
            strokeWidth={2}
            data-icon='inline-start'
          />
          {t('Add rule')}
        </Button>
      </div>
    </div>
  )
}
