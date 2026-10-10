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

import { CHANNEL_TYPE_TASK_PLUGIN } from '../../../constants'
import type { ChannelFormValues } from '../../../lib'

const NUMERIC_FIELDS = [
  {
    name: 'fps',
    label: 'Video sampling rate (fps)',
    min: 0.1,
    max: 8,
    step: 0.1,
  },
  {
    name: 'max_frames',
    label: 'Maximum video frames per request',
    min: 1,
    max: 512,
    step: 1,
  },
  {
    name: 'max_side',
    label: 'Frame longest side (px)',
    min: 256,
    max: 2048,
    step: 1,
  },
  {
    name: 'pixel_percent',
    label: 'Total pixels retained (%)',
    min: 1,
    max: 100,
    step: 1,
  },
  { name: 'jpeg_quality', label: 'JPEG quality', min: 1, max: 100, step: 1 },
] as const

export function VideoCompatibilityFields(props: {
  control: Control<ChannelFormValues>
  sensitiveLocked: boolean
}) {
  const { t } = useTranslation()
  const enabled = useWatch({
    control: props.control,
    name: 'video_compatibility.enabled',
  })
  const channelType = useWatch({ control: props.control, name: 'type' })
  const taskPlugin = channelType === CHANNEL_TYPE_TASK_PLUGIN
  const videoDisabled = useWatch({
    control: props.control,
    name: 'disable_video_understanding',
  })
  return (
    <div className='space-y-4 rounded-lg border p-4'>
      <FormField
        control={props.control}
        name='video_compatibility.enabled'
        render={({ field }) => (
          <FormItem className='flex items-center justify-between gap-3'>
            <div className='space-y-0.5'>
              <FormLabel>
                {t('Video compatibility (frame extraction)')}
              </FormLabel>
              <FormDescription>
                {t(
                  'Convert inline base64 videos into timestamped images. Only visual content is processed; audio is ignored.'
                )}
              </FormDescription>
              <FormMessage />
            </div>
            <FormControl>
              <Switch
                checked={field.value ?? false}
                disabled={
                  props.sensitiveLocked ||
                  Boolean((videoDisabled || taskPlugin) && !field.value)
                }
                onCheckedChange={field.onChange}
              />
            </FormControl>
          </FormItem>
        )}
      />
      {taskPlugin && (
        <p className='text-muted-foreground text-xs'>
          {t('video_compatibility is unavailable for task plugin channels')}
        </p>
      )}
      {enabled && (
        <>
          <div className='grid gap-4 sm:grid-cols-2'>
            {NUMERIC_FIELDS.map((setting) => (
              <FormField
                key={setting.name}
                control={props.control}
                name={`video_compatibility.${setting.name}`}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t(setting.label)}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={setting.min}
                        max={setting.max}
                        step={setting.step}
                        disabled={props.sensitiveLocked}
                        name={field.name}
                        ref={field.ref}
                        onBlur={field.onBlur}
                        value={field.value ?? ''}
                        onChange={(event) =>
                          field.onChange(
                            event.target.value === ''
                              ? ''
                              : Number(event.target.value)
                          )
                        }
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            ))}
          </div>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Pixel percentage applies after the longest-side limit: 25% retains half the width and height. JPEG quality mainly controls size and detail; tokens depend on the final image dimensions.'
            )}
          </p>
        </>
      )}
    </div>
  )
}
