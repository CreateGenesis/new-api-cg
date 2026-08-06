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
  const retryZeroOutput = useWatch({
    control: props.control,
    name: 'retry_zero_output',
  })
  const disableStream = useWatch({
    control: props.control,
    name: 'disable_stream',
  })
  const disableNonStream = useWatch({
    control: props.control,
    name: 'disable_non_stream',
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
                  {t('Retry zero-token output on another channel')}
                </FormLabel>
                <FormDescription>
                  {t(
                    'If upstream reports zero output tokens and no effective output, retry on another channel.'
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

      <FormField
        control={props.control}
        name='missing_output_token_multiplier'
        render={({ field }) => (
          <FormItem className='max-w-sm'>
            <FormLabel>{t('Missing output token multiplier')}</FormLabel>
            <FormControl>
              <Input
                type='number'
                min={0.01}
                max={100}
                step={0.01}
                disabled={!retryZeroOutput}
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
                'Scales locally estimated output tokens when valid output is present but upstream usage is zero. Values are rounded up and changes apply to new requests immediately.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
    </fieldset>
  )
}
