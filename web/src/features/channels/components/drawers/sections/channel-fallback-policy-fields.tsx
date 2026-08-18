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
import { useWatch, type Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
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
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../../../lib'
import { isRequestModeSwitchDisabled } from '../../../lib/channel-request-mode-policy'

type ChannelFallbackPolicyFieldsProps = {
  control: Control<ChannelFormValues>
  sensitiveLocked: boolean
}

export function ChannelFallbackPolicyFields(
  props: ChannelFallbackPolicyFieldsProps
) {
  const { t } = useTranslation()
  const disableStream = useWatch({
    control: props.control,
    name: 'disable_stream',
  })
  const disableNonStream = useWatch({
    control: props.control,
    name: 'disable_non_stream',
  })
  const usageEstimationEnabled = useWatch({
    control: props.control,
    name: 'usage_estimation_enabled',
  })

  return (
    <fieldset
      disabled={props.sensitiveLocked}
      className='space-y-4 disabled:opacity-60'
    >
      <div className='divide-border space-y-0 divide-y border-y'>
        <FormField
          control={props.control}
          name='retry_zero_output'
          render={({ field }) => (
            <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
              <div className='space-y-0.5'>
                <FormLabel className='text-sm'>
                  {t('Retry empty output on another channel')}
                </FormLabel>
                <FormDescription>
                  {t(
                    'If the response has no effective visible output, retry on another channel.'
                  )}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  checked={field.value}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />

        <FormField
          control={props.control}
          name='disable_stream'
          render={({ field }) => (
            <FormItem className='px-4 py-3'>
              <div className='flex items-center justify-between gap-3'>
                <div className='space-y-0.5'>
                  <FormLabel className='text-sm'>
                    {t('Disable stream requests')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Skip this channel for stream generation requests and route them to another eligible channel.'
                    )}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    checked={field.value}
                    disabled={isRequestModeSwitchDisabled(
                      field.value,
                      disableNonStream
                    )}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </div>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={props.control}
          name='disable_non_stream'
          render={({ field }) => (
            <FormItem className='px-4 py-3'>
              <div className='flex items-center justify-between gap-3'>
                <div className='space-y-0.5'>
                  <FormLabel className='text-sm'>
                    {t('Disable non-stream requests')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Skip this channel for non-stream generation requests and route them to another eligible channel.'
                    )}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    checked={field.value}
                    disabled={isRequestModeSwitchDisabled(
                      field.value,
                      disableStream
                    )}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </div>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>

      <div className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={props.control}
          name='usage_token_limit_input_tokens'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Input Usage token limit')}</FormLabel>
              <FormControl>
                <Input
                  type='number'
                  min={0}
                  max={2147483647}
                  step={1}
                  value={field.value ?? ''}
                  onBlur={field.onBlur}
                  name={field.name}
                  ref={field.ref}
                  onChange={(event) => {
                    const value = event.target.value
                    field.onChange(value === '' ? undefined : Number(value))
                  }}
                />
              </FormControl>
              <FormDescription>
                {t(
                  'Set to 0 to disable. Values above the limit are replaced with a random 30%-95% of the limit.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={props.control}
          name='usage_token_limit_output_tokens'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Output Usage token limit')}</FormLabel>
              <FormControl>
                <Input
                  type='number'
                  min={0}
                  max={2147483647}
                  step={1}
                  value={field.value ?? ''}
                  onBlur={field.onBlur}
                  name={field.name}
                  ref={field.ref}
                  onChange={(event) => {
                    const value = event.target.value
                    field.onChange(value === '' ? undefined : Number(value))
                  }}
                />
              </FormControl>
              <FormDescription>
                {t(
                  'Set to 0 to disable. Values above the limit are replaced with a random 30%-95% of the limit.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>

      <div className='divide-border space-y-0 divide-y border-y'>
        <FormField
          control={props.control}
          name='usage_estimation_enabled'
          render={({ field }) => (
            <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
              <div className='space-y-0.5'>
                <FormLabel className='text-sm'>
                  {t('Estimate missing usage tokens')}
                </FormLabel>
                <FormDescription>
                  {t(
                    'Estimate zero or missing input and output usage from the selected model family.'
                  )}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  checked={field.value === true}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />

        {usageEstimationEnabled && (
          <div className='grid gap-4 px-4 py-3 sm:grid-cols-3'>
            <FormField
              control={props.control}
              name='usage_estimation_model_family'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Usage estimation model family')}</FormLabel>
                  <Select
                    value={field.value ?? 'glm'}
                    onValueChange={field.onChange}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectItem value='glm'>GLM</SelectItem>
                      <SelectItem value='kimi'>Kimi</SelectItem>
                      <SelectItem value='deepseek'>DeepSeek</SelectItem>
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={props.control}
              name='usage_estimation_input_multiplier'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Input estimation multiplier')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={0.01}
                      max={100}
                      step={0.01}
                      value={field.value ?? 1}
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                      onChange={(event) => field.onChange(Number(event.target.value))}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Allowed range: 0.01 to 100.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={props.control}
              name='usage_estimation_output_multiplier'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Output estimation multiplier')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={0.01}
                      max={100}
                      step={0.01}
                      value={field.value ?? 1}
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                      onChange={(event) => field.onChange(Number(event.target.value))}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Allowed range: 0.01 to 100.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
        )}
      </div>
    </fieldset>
  )
}
