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
  FormMessage,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../lib/channel-form'
import { ModelMappingEditor } from './model-mapping-editor'

type ResponseModelMappingFieldsProps = {
  sourceModelOptions: string[]
  disabled?: boolean
}

export function ResponseModelMappingFields(
  props: ResponseModelMappingFieldsProps
) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()
  const currentResponseModelMappingEnabled = useWatch({
    control: form.control,
    name: 'response_model_mapping_enabled',
  })
  return (
    <div className='border-border/60 space-y-4 rounded-lg border p-4'>
      <FormField
        control={form.control}
        name='response_model_mapping_enabled'
        render={({ field }) => (
          <FormItem className='flex items-center justify-between gap-3'>
            <div className='space-y-1'>
              <FormLabel>{t('Rewrite response model')}</FormLabel>
              <FormDescription>
                {t(
                  'Return the client request model name unless a response mapping is configured. Only the response model name is changed.'
                )}
              </FormDescription>
            </div>
            <FormControl>
              <Switch
                checked={field.value === true}
                onCheckedChange={field.onChange}
                disabled={props.disabled}
              />
            </FormControl>
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name='response_model_mapping'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Response model mapping')}</FormLabel>
            <FormDescription>
              {t(
                'Client request model → response model. Unlisted models use the client request name. Mappings are applied once.'
              )}
            </FormDescription>
            <FormControl>
              <ModelMappingEditor
                sourceLabel={t('Client request model')}
                targetLabel={t('Response model')}
                ariaLabel={t('Response model mapping')}
                value={field.value || ''}
                onChange={field.onChange}
                sourceModelOptions={props.sourceModelOptions}
                disabled={!currentResponseModelMappingEnabled || props.disabled}
              />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
    </div>
  )
}
