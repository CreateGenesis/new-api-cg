/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useWatch, type Control, type FieldPath } from 'react-hook-form'
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

type SimulatedModelCacheNumberFieldName = Extract<
  FieldPath<ChannelFormValues>,
  | 'simulated_model_cache_ttl_seconds'
  | 'simulated_model_cache_min_match_ratio'
  | 'simulated_model_cache_image_tokens_per_megapixel'
  | 'simulated_model_cache_video_tokens_per_second_megapixel'
  | 'simulated_model_cache_audio_tokens_per_second'
  | 'simulated_model_cache_file_tokens_per_mib'
  | 'simulated_model_cache_image_fallback_tokens'
  | 'simulated_model_cache_video_fallback_tokens'
  | 'simulated_model_cache_audio_fallback_tokens'
  | 'simulated_model_cache_file_fallback_tokens'
>

type SimulatedModelCacheNumberFieldProps = {
  control: Control<ChannelFormValues>
  name: SimulatedModelCacheNumberFieldName
  label: string
  description?: string
  disabled: boolean
  min: number
  max?: number
  step: number
}

function SimulatedModelCacheNumberField(
  props: SimulatedModelCacheNumberFieldProps
) {
  return (
    <FormField
      control={props.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            <Input
              type='number'
              min={props.min}
              max={props.max}
              step={props.step}
              disabled={props.disabled}
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
          {props.description && (
            <FormDescription>{props.description}</FormDescription>
          )}
          <FormMessage />
        </FormItem>
      )}
    />
  )
}

type SimulatedModelCacheFieldsProps = {
  control: Control<ChannelFormValues>
  runtimeActive: boolean
  sensitiveLocked: boolean
}

export function SimulatedModelCacheFields(
  props: SimulatedModelCacheFieldsProps
) {
  const { t } = useTranslation()
  const multimodalEnabled = useWatch({
    control: props.control,
    name: 'simulated_model_cache_multimodal_enabled',
  })
  const numericFieldsDisabled = !props.runtimeActive || !multimodalEnabled

  return (
    <fieldset
      disabled={props.sensitiveLocked}
      className='space-y-5 disabled:opacity-60'
    >
      <div className='divide-border space-y-0 divide-y border-y'>
        <FormField
          control={props.control}
          name='simulated_model_cache_enabled'
          render={({ field }) => (
            <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
              <div className='space-y-0.5'>
                <FormLabel className='text-sm'>
                  {t('Show simulated cache fields')}
                </FormLabel>
                <FormDescription>
                  {t(
                    'Controls whether simulated cache fields are shown in responses and logs. Cache-aware key routing continues in the background when disabled.'
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
          name='simulated_model_cache_estimate_missing_input_tokens'
          render={({ field }) => (
            <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
              <div className='space-y-0.5'>
                <FormLabel className='text-sm'>
                  {t('Estimate missing input tokens')}
                </FormLabel>
                <FormDescription>
                  {t(
                    'When an upstream reports zero input tokens, use a conservative local text estimate for billing and simulated cache usage.'
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
          name='simulated_model_cache_multimodal_enabled'
          render={({ field }) => (
            <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
              <div className='space-y-0.5'>
                <FormLabel className='text-sm'>
                  {t('Multimedia cache matching')}
                </FormLabel>
                <FormDescription>
                  {t(
                    'Include image, video, audio, and file blocks in simulated cache fingerprints.'
                  )}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  checked={field.value}
                  disabled={!props.runtimeActive}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />
      </div>

      <div className='grid gap-4 sm:grid-cols-2'>
        <SimulatedModelCacheNumberField
          control={props.control}
          name='simulated_model_cache_ttl_seconds'
          label={t('TTL seconds')}
          description={t(
            'How long prompt fingerprints remain available for matching'
          )}
          disabled={!props.runtimeActive}
          min={1}
          step={1}
        />
        <SimulatedModelCacheNumberField
          control={props.control}
          name='simulated_model_cache_min_match_ratio'
          label={t('Minimum match ratio')}
          description={t(
            'Approximate consecutive content fingerprint ratio required for partial cache usage'
          )}
          disabled={!props.runtimeActive}
          min={0.01}
          max={1}
          step={0.01}
        />
      </div>

      {multimodalEnabled && (
        <div className='space-y-5'>
          <div className='space-y-3'>
            <div className='space-y-1'>
              <h4 className='text-sm font-medium'>{t('Media token rates')}</h4>
              <p className='text-muted-foreground text-xs'>
                {t('Used when the request provides measurable media metadata.')}
              </p>
            </div>
            <div className='grid gap-4 sm:grid-cols-2'>
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_image_tokens_per_megapixel'
                label={t('Image tokens per megapixel')}
                disabled={numericFieldsDisabled}
                min={0.000001}
                max={1_000_000}
                step={0.000001}
              />
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_video_tokens_per_second_megapixel'
                label={t('Video tokens per second-megapixel')}
                disabled={numericFieldsDisabled}
                min={0.000001}
                max={1_000_000}
                step={0.000001}
              />
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_audio_tokens_per_second'
                label={t('Audio tokens per second')}
                disabled={numericFieldsDisabled}
                min={0.000001}
                max={1_000_000}
                step={0.000001}
              />
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_file_tokens_per_mib'
                label={t('File tokens per MiB')}
                disabled={numericFieldsDisabled}
                min={0.000001}
                max={1_000_000}
                step={0.000001}
              />
            </div>
          </div>

          <div className='space-y-3'>
            <div className='space-y-1'>
              <h4 className='text-sm font-medium'>{t('Fallback tokens')}</h4>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Used when dimensions, duration, or file size are unavailable.'
                )}
              </p>
            </div>
            <div className='grid gap-4 sm:grid-cols-2'>
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_image_fallback_tokens'
                label={t('Image fallback tokens')}
                disabled={numericFieldsDisabled}
                min={1}
                max={1_000_000}
                step={1}
              />
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_video_fallback_tokens'
                label={t('Video fallback tokens')}
                disabled={numericFieldsDisabled}
                min={1}
                max={1_000_000}
                step={1}
              />
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_audio_fallback_tokens'
                label={t('Audio fallback tokens')}
                disabled={numericFieldsDisabled}
                min={1}
                max={1_000_000}
                step={1}
              />
              <SimulatedModelCacheNumberField
                control={props.control}
                name='simulated_model_cache_file_fallback_tokens'
                label={t('File fallback tokens')}
                disabled={numericFieldsDisabled}
                min={1}
                max={1_000_000}
                step={1}
              />
            </div>
          </div>
        </div>
      )}
    </fieldset>
  )
}
