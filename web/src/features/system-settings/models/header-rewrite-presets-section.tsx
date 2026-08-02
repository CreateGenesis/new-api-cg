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
import i18next from 'i18next'
import { Braces, RotateCcw } from 'lucide-react'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Textarea } from '@/components/ui/textarea'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const DEFAULT_HEADER_REWRITE_PRESETS = JSON.stringify(
  {
    'claude-code': {
      name: 'Claude Code',
      remove: ['X-App', 'X-Stainless-*'],
      set: {
        'User-Agent': 'claude-cli/2.1.201 (external, cli)',
      },
    },
    opencode: {
      name: 'OpenCode',
      remove: ['X-App', 'X-Stainless-*'],
      set: {
        'User-Agent': 'opencode/1.17.13',
      },
    },
    codex: {
      name: 'Codex CLI',
      remove: ['X-App', 'X-Stainless-*'],
      set: {
        'User-Agent': 'codex-cli/0.146.0',
        Originator: 'codex_cli_rs',
        Session_id: '{client_header:Session_id|request_id}',
        Thread_id: '{client_header:Thread_id|request_id}',
        'X-Client-Request-Id': '{client_header:X-Client-Request-Id|request_id}',
      },
    },
  },
  null,
  2
)

const presetIdSchema = z
  .string()
  .min(1)
  .max(64)
  .regex(/^[A-Za-z0-9][A-Za-z0-9._-]*$/)

const presetSchema = z.object({
  name: z.string().trim().min(1).max(128),
  remove: z.array(z.string()).max(64).optional(),
  set: z.record(z.string(), z.string()).optional(),
})

const formSchema = z.object({
  presets: z.string().superRefine((value, context) => {
    try {
      const parsed = JSON.parse(value)
      const result = z
        .record(presetIdSchema, presetSchema)
        .refine((presets) => Object.keys(presets).length <= 64)
        .safeParse(parsed)
      if (!result.success) {
        context.addIssue({
          code: z.ZodIssueCode.custom,
          message: i18next.t('Invalid header rewrite preset structure'),
        })
      }
    } catch {
      context.addIssue({
        code: z.ZodIssueCode.custom,
        message: i18next.t('Invalid JSON format'),
      })
    }
  }),
})

type HeaderRewritePresetsForm = z.infer<typeof formSchema>

function normalizeJson(value: string) {
  return JSON.stringify(JSON.parse(value))
}

type HeaderRewritePresetsSectionProps = {
  defaultValue: string
}

export function HeaderRewritePresetsSection({
  defaultValue,
}: HeaderRewritePresetsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const form = useForm<HeaderRewritePresetsForm>({
    resolver: zodResolver(formSchema),
    defaultValues: { presets: defaultValue },
  })

  useEffect(() => {
    form.reset({ presets: defaultValue })
  }, [defaultValue, form])

  const onSubmit = async (values: HeaderRewritePresetsForm) => {
    if (normalizeJson(values.presets) === normalizeJson(defaultValue)) {
      toast.info(t('No changes to save'))
      return
    }
    await updateOption.mutateAsync({
      key: 'HeaderRewritePresets',
      value: JSON.stringify(JSON.parse(values.presets), null, 2),
    })
  }

  return (
    <SettingsSection title={t('Header Rewrite')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            onReset={() => form.reset({ presets: defaultValue })}
            isSaving={updateOption.isPending}
          />
          <FormField
            control={form.control}
            name='presets'
            render={({ field }) => (
              <FormItem>
                <div className='flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between'>
                  <div className='space-y-1'>
                    <FormLabel>{t('Preset JSON')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Channels reference preset IDs live; saving here updates every linked channel.'
                      )}
                    </FormDescription>
                  </div>
                  <div className='flex flex-wrap gap-2'>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      onClick={() => {
                        try {
                          field.onChange(
                            JSON.stringify(JSON.parse(field.value), null, 2)
                          )
                        } catch {
                          toast.error(t('Invalid JSON format'))
                        }
                      }}
                    >
                      <Braces className='mr-2 h-4 w-4' />
                      {t('Format')}
                    </Button>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      onClick={() =>
                        field.onChange(DEFAULT_HEADER_REWRITE_PRESETS)
                      }
                    >
                      <RotateCcw className='mr-2 h-4 w-4' />
                      {t('Load Default Presets')}
                    </Button>
                  </div>
                </div>
                <FormControl>
                  <Textarea
                    {...field}
                    rows={22}
                    spellCheck={false}
                    className='min-h-96 resize-y font-mono text-xs'
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
