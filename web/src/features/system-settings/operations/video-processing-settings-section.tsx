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
import { useMemo } from 'react'
import { useForm, type FieldPath } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
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
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { safeNumberFieldProps } from '../utils/numeric-field'
import {
  parseVideoProcessingConfig,
  videoProcessingSchema,
  type VideoProcessingConfig,
} from './video-processing-config'

const RESOURCE_FIELDS = [
  {
    name: 'max_concurrency',
    label: 'Maximum video processing concurrency',
    min: 1,
    max: 256,
  },
  {
    name: 'queue_capacity',
    label: 'Video waiting queue capacity',
    min: 0,
    max: 4096,
  },
  {
    name: 'queue_bytes_mb',
    label: 'Queued request body budget (MiB)',
    min: 1,
    max: 65536,
  },
  {
    name: 'queue_wait_seconds',
    label: 'Maximum queue wait (seconds)',
    min: 1,
    max: 120,
  },
  {
    name: 'request_timeout_seconds',
    label: 'Media processing time limit (seconds)',
    min: 1,
    max: 300,
  },
  {
    name: 'ffmpeg_threads',
    label: 'FFmpeg threads per process',
    min: 1,
    max: 16,
  },
  {
    name: 'worker_memory_mb',
    label: 'Media process address space limit (MiB)',
    min: 256,
    max: 8192,
  },
  {
    name: 'buffer_memory_mb',
    label: 'Media buffer memory budget (MiB)',
    min: 64,
    max: 65536,
  },
  {
    name: 'temp_disk_mb',
    label: 'Media temporary disk budget (MiB)',
    min: 16,
    max: 1048576,
  },
] as const
const LEVEL_FIELDS = [
  { name: 'pixel_multiplier_percent', label: 'Pixel retention multiplier (%)' },
  { name: 'fps_multiplier_percent', label: 'Sampling rate multiplier (%)' },
  { name: 'frames_multiplier_percent', label: 'Frame limit multiplier (%)' },
  { name: 'jpeg_quality_cap', label: 'JPEG quality cap' },
  { name: 'cpu_threshold', label: 'CPU trigger (%)' },
  { name: 'memory_threshold', label: 'Memory trigger (%)' },
  { name: 'running_threshold', label: 'Running slot trigger (%)' },
  { name: 'queue_threshold', label: 'Queue occupancy trigger (%)' },
] as const
const MINIMUM_FIELDS = [
  {
    name: 'minimum_fps',
    label: 'Minimum sampling rate (fps)',
    min: 0.1,
    max: 8,
    step: 0.1,
  },
  {
    name: 'minimum_frames',
    label: 'Minimum frame limit',
    min: 1,
    max: 512,
    step: 1,
  },
  {
    name: 'minimum_pixel_percent',
    label: 'Minimum pixel retention (%)',
    min: 1,
    max: 100,
    step: 1,
  },
  {
    name: 'minimum_jpeg_quality',
    label: 'Minimum JPEG quality',
    min: 1,
    max: 100,
    step: 1,
  },
  {
    name: 'recovery_seconds',
    label: 'Load recovery cooldown (seconds)',
    min: 1,
    max: 300,
    step: 1,
  },
] as const

export function VideoProcessingSettingsSection(props: {
  defaultValue: string
}) {
  const { t } = useTranslation()
  const defaults = useMemo(
    () => parseVideoProcessingConfig(props.defaultValue),
    [props.defaultValue]
  )
  const form = useForm<VideoProcessingConfig>({
    resolver: zodResolver(videoProcessingSchema),
    defaultValues: defaults.config,
  })
  useResetForm(form, defaults.config)
  const updateOption = useUpdateOption()
  const adaptive = form.watch('adaptive_enabled')

  const numericField = (setting: {
    name: FieldPath<VideoProcessingConfig>
    label: string
    min: number
    max: number
    step?: number
  }) => (
    <FormField
      key={setting.name}
      control={form.control}
      name={setting.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t(setting.label)}</FormLabel>
          <FormControl>
            <Input
              type='number'
              min={setting.min}
              max={setting.max}
              step={setting.step ?? 1}
              {...safeNumberFieldProps(field, { allowEmpty: true })}
            />
          </FormControl>
          <FormMessage />
        </FormItem>
      )}
    />
  )
  const onSubmit = async (values: VideoProcessingConfig) => {
    try {
      await updateOption.mutateAsync({
        key: 'video_processing_setting.config',
        value: JSON.stringify(values),
      })
      form.reset(values)
    } catch {
      /* The shared mutation displays the error; preserve unsaved values. */
    }
  }

  return (
    <SettingsSection title={t('Video processing')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save video processing settings'
          />
          <Alert>
            <AlertDescription>
              {t(
                'Limits apply to each instance. Queue capacity 0 disables waiting. Reducing limits lets running tasks finish and stops new admission until usage falls below the limit.'
              )}
            </AlertDescription>
          </Alert>
          {defaults.invalid && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t(
                  'The saved video processing configuration is invalid. Review and save valid settings.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {RESOURCE_FIELDS.map(numericField)}
          <FormField
            control={form.control}
            name='adaptive_enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>
                    {t('Automatically reduce video quality under load')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Reduce image size, sampling rate and JPEG quality before system hard limits are reached. Each request keeps its selected tier during retries.'
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
          {adaptive && (
            <>
              {(['light', 'heavy'] as const).map((level) => (
                <section
                  key={level}
                  className='space-y-4 rounded-lg border p-4'
                >
                  <h3 className='text-sm font-medium'>
                    {level === 'light'
                      ? t('Light load reduction')
                      : t('Heavy load reduction')}
                  </h3>
                  <div className='grid gap-4 lg:grid-cols-2'>
                    {LEVEL_FIELDS.map((setting) =>
                      numericField({
                        ...setting,
                        name: `adaptive_config.${level}.${setting.name}`,
                        min: 1,
                        max: 100,
                      })
                    )}
                  </div>
                </section>
              ))}
              <p className='text-muted-foreground text-xs'>
                {t(
                  'These minimums never increase a channel’s existing quality. Recovery requires lower pressure and the configured cooldown. Missing resource samples use pool and queue occupancy.'
                )}
              </p>
              {MINIMUM_FIELDS.map((setting) =>
                numericField({
                  ...setting,
                  name: `adaptive_config.${setting.name}`,
                })
              )}
            </>
          )}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
