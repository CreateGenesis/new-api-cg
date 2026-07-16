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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangleIcon,
  CheckCircle2Icon,
  DownloadIcon,
  FileJsonIcon,
  Loader2Icon,
  UploadIcon,
} from 'lucide-react'
import { useRef, useState, type DragEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'

import {
  applySystemConfigImport,
  exportSystemConfig,
  previewSystemConfigImport,
} from '../api'
import { SettingsSection } from '../components/settings-section'
import type {
  SystemConfigChangeCounts,
  SystemConfigImportPreview,
  SystemConfigIssue,
} from '../types'

const MAX_IMPORT_SIZE = 32 * 1024 * 1024

const ISSUE_TRANSLATION_KEYS: Record<SystemConfigIssue['code'], string> = {
  unknown_option: 'Unknown setting skipped',
  new_channel_disabled: 'New channel will be imported disabled',
  missing_vendor: 'Referenced vendor is missing',
  ambiguous_channel: 'Multiple target channels have the same name and type',
  duplicate_vendor: 'The import file contains a duplicate vendor',
  duplicate_model: 'The import file contains a duplicate model',
  duplicate_channel: 'The import file contains a duplicate channel',
}

type PreviewCategory = {
  label: string
  counts: SystemConfigChangeCounts
}

export function ConfigMigrationSection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [fileContent, setFileContent] = useState('')
  const [preview, setPreview] = useState<SystemConfigImportPreview | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)

  const exportMutation = useMutation({
    mutationFn: exportSystemConfig,
    onSuccess: (result) => {
      const url = URL.createObjectURL(result.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = result.filename
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      URL.revokeObjectURL(url)
      toast.success(t('Configuration exported successfully.'))
    },
  })

  const previewMutation = useMutation({
    mutationFn: previewSystemConfigImport,
    onSuccess: (response) => {
      if (response.success && response.data) {
        setPreview(response.data)
      }
    },
  })

  const applyMutation = useMutation({
    mutationFn: (request: { content: string; hash: string }) =>
      applySystemConfigImport(request.content, request.hash),
    onSuccess: async (response) => {
      if (!response.success || !response.data) return

      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['system-options'] }),
        queryClient.invalidateQueries({ queryKey: ['status'] }),
      ])
      try {
        window.localStorage.removeItem('status')
      } catch {
        /* empty */
      }
      setConfirmOpen(false)
      setPreview(response.data)
      toast.success(t('Configuration imported successfully.'))
      if (response.data.reload_needed) {
        window.location.reload()
      }
    },
  })

  const acceptFile = async (nextFile: File | undefined) => {
    if (!nextFile) return
    if (!nextFile.name.toLowerCase().endsWith('.json')) {
      toast.error(t('Select a JSON configuration file.'))
      return
    }
    if (nextFile.size > MAX_IMPORT_SIZE) {
      toast.error(t('The configuration file must not exceed 32 MiB.'))
      return
    }
    try {
      const content = await nextFile.text()
      JSON.parse(content)
      setFile(nextFile)
      setFileContent(content)
      setPreview(null)
    } catch {
      toast.error(t('The selected file is not valid JSON.'))
    }
  }

  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    void acceptFile(event.dataTransfer.files[0])
  }

  const handleApply = () => {
    if (!preview || preview.conflicts.length > 0) return
    applyMutation.mutate({ content: fileContent, hash: preview.hash })
  }

  const categories: PreviewCategory[] = preview
    ? [
        { label: t('System settings'), counts: preview.options },
        { label: t('Channels'), counts: preview.channels },
        { label: t('Vendors'), counts: preview.vendors },
        { label: t('Models'), counts: preview.models },
      ]
    : []

  return (
    <SettingsSection title={t('Configuration Migration')}>
      <div className='flex flex-col gap-6'>
        <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
          <div className='min-w-0'>
            <h4 className='text-sm font-medium'>{t('Export configuration')}</h4>
            <p className='text-muted-foreground mt-1 text-sm'>
              {t(
                'Download system settings, channels, vendors, and models without credentials or runtime data.'
              )}
            </p>
          </div>
          <Button
            type='button'
            variant='outline'
            onClick={() => exportMutation.mutate()}
            disabled={exportMutation.isPending}
          >
            {exportMutation.isPending ? (
              <Loader2Icon className='animate-spin' aria-hidden='true' />
            ) : (
              <DownloadIcon aria-hidden='true' />
            )}
            {t('Export configuration')}
          </Button>
        </div>

        <Separator />

        <div className='flex flex-col gap-4'>
          <div>
            <h4 className='text-sm font-medium'>{t('Import configuration')}</h4>
            <p className='text-muted-foreground mt-1 text-sm'>
              {t(
                'Review all changes before merging them into the current system.'
              )}
            </p>
          </div>

          <div
            className='border-border hover:bg-muted/40 focus-within:border-ring flex min-h-28 w-full flex-col items-center justify-center gap-3 rounded-md border border-dashed p-5 text-center transition-colors'
            onDragOver={(event) => event.preventDefault()}
            onDrop={handleDrop}
          >
            <FileJsonIcon
              className='text-muted-foreground size-6'
              aria-hidden='true'
            />
            <div className='min-w-0'>
              <p className='truncate text-sm font-medium'>
                {file?.name ?? t('No configuration file selected')}
              </p>
              {file && (
                <p className='text-muted-foreground mt-1 text-xs'>
                  {t('{{size}} KiB', { size: Math.ceil(file.size / 1024) })}
                </p>
              )}
            </div>
            <input
              ref={fileInputRef}
              type='file'
              accept='.json,application/json'
              className='sr-only'
              onChange={(event) => void acceptFile(event.target.files?.[0])}
            />
            <Button
              type='button'
              variant='secondary'
              size='sm'
              onClick={() => fileInputRef.current?.click()}
            >
              <UploadIcon aria-hidden='true' />
              {t('Select file')}
            </Button>
          </div>

          <div className='flex justify-end'>
            <Button
              type='button'
              onClick={() => previewMutation.mutate(fileContent)}
              disabled={!fileContent || previewMutation.isPending}
            >
              {previewMutation.isPending && (
                <Loader2Icon className='animate-spin' aria-hidden='true' />
              )}
              {t('Preview import')}
            </Button>
          </div>
        </div>

        {preview && (
          <div className='flex flex-col gap-4 border-t pt-5'>
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <h4 className='text-sm font-medium'>{t('Import preview')}</h4>
              <span className='text-muted-foreground font-mono text-xs'>
                {preview.hash.slice(0, 12)}
              </span>
            </div>

            <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
              {categories.map((category) => (
                <div key={category.label} className='rounded-md border p-3'>
                  <div className='text-sm font-medium'>{category.label}</div>
                  <div className='mt-3 flex flex-wrap gap-1.5'>
                    <Badge variant='secondary'>
                      {t('Add {{count}}', { count: category.counts.add })}
                    </Badge>
                    <Badge variant='outline'>
                      {t('Update {{count}}', {
                        count: category.counts.update,
                      })}
                    </Badge>
                    <Badge variant='ghost'>
                      {t('Unchanged {{count}}', {
                        count: category.counts.unchanged,
                      })}
                    </Badge>
                    {category.counts.skipped > 0 && (
                      <Badge variant='destructive'>
                        {t('Skipped {{count}}', {
                          count: category.counts.skipped,
                        })}
                      </Badge>
                    )}
                  </div>
                </div>
              ))}
            </div>

            {preview.warnings.length > 0 && (
              <Alert>
                <AlertTriangleIcon aria-hidden='true' />
                <AlertTitle>{t('Warnings')}</AlertTitle>
                <AlertDescription>
                  <ul className='list-disc space-y-1 ps-4'>
                    {preview.warnings.map((issue) => (
                      <li key={`${issue.code}-${issue.item}`}>
                        {t(ISSUE_TRANSLATION_KEYS[issue.code])}
                        {issue.item ? `: ${issue.item}` : ''}
                      </li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            )}

            {preview.conflicts.length > 0 ? (
              <Alert variant='destructive'>
                <AlertTriangleIcon aria-hidden='true' />
                <AlertTitle>
                  {t('Resolve conflicts before importing')}
                </AlertTitle>
                <AlertDescription>
                  <ul className='list-disc space-y-1 ps-4'>
                    {preview.conflicts.map((issue) => (
                      <li key={`${issue.code}-${issue.item}`}>
                        {t(ISSUE_TRANSLATION_KEYS[issue.code])}
                        {issue.item ? `: ${issue.item}` : ''}
                      </li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            ) : (
              <Alert>
                <CheckCircle2Icon aria-hidden='true' />
                <AlertTitle>{t('Ready to import')}</AlertTitle>
                <AlertDescription>
                  {t(
                    'Existing records will be merged. New channels will be disabled until credentials are added.'
                  )}
                </AlertDescription>
              </Alert>
            )}

            <div className='flex justify-end'>
              <Button
                type='button'
                onClick={() => setConfirmOpen(true)}
                disabled={
                  preview.conflicts.length > 0 || applyMutation.isPending
                }
              >
                <UploadIcon aria-hidden='true' />
                {t('Apply import')}
              </Button>
            </div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Apply configuration import?')}
        desc={t(
          'System settings and matching records will be updated in one transaction. Existing credentials are preserved, and new channels remain disabled.'
        )}
        confirmText={t('Apply import')}
        handleConfirm={handleApply}
        isLoading={applyMutation.isPending}
      />
    </SettingsSection>
  )
}
