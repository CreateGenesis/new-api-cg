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
import { useNavigate } from '@tanstack/react-router'
import {
  AlertTriangleIcon,
  DownloadIcon,
  FileJsonIcon,
  Loader2Icon,
  ShieldAlertIcon,
  UploadIcon,
} from 'lucide-react'
import { useRef, useState, type DragEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { clearAuthenticatedClientState } from '@/lib/api'

import {
  applySystemBackupImport,
  exportSystemBackup,
  previewSystemBackupImport,
} from '../api'
import type {
  SystemBackupImportPreview,
  SystemBackupProof,
  SystemBackupProofScope,
} from '../types'
import { SystemBackupCredentialsDialog } from './system-backup-credentials-dialog'
import { SystemBackupPreview } from './system-backup-preview'
import {
  getSystemBackupErrorMessage,
  isSystemBackupProofError,
  isSystemBackupProofUsable,
  MAX_SYSTEM_BACKUP_SIZE,
  validateSystemBackupText,
} from './system-backup-utils'

type ProtectedAction = 'export' | 'preview' | 'apply'

const EXPORT_SCOPE: SystemBackupProofScope = 'system.config.full.export'
const IMPORT_SCOPE: SystemBackupProofScope = 'system.config.full.import'

export function FullSystemBackupSection() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [fileContent, setFileContent] = useState('')
  const [preview, setPreview] = useState<SystemBackupImportPreview | null>(null)
  const [importProof, setImportProof] = useState<SystemBackupProof>()
  const [protectedAction, setProtectedAction] =
    useState<ProtectedAction>('export')
  const [credentialsOpen, setCredentialsOpen] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)

  const exportMutation = useMutation({
    mutationFn: exportSystemBackup,
    onSuccess: (result) => {
      const url = URL.createObjectURL(result.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = result.filename
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      URL.revokeObjectURL(url)
      toast.success(t('Full system backup exported.'))
    },
    onError: (error) => {
      toast.error(
        getSystemBackupErrorMessage(error) ||
          t('Failed to export the full system backup.')
      )
    },
  })

  const previewMutation = useMutation({
    mutationFn: async (proof: SystemBackupProof) => {
      const response = await previewSystemBackupImport(
        fileContent,
        proof.proof_token
      )
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Failed to preview the backup.'))
      }
      return response.data
    },
    onSuccess: setPreview,
    onError: (error) => {
      if (isSystemBackupProofError(error)) {
        setImportProof(undefined)
        setProtectedAction('preview')
        setCredentialsOpen(true)
        return
      }
      toast.error(
        getSystemBackupErrorMessage(error) || t('Failed to preview the backup.')
      )
    },
  })

  const applyMutation = useMutation({
    mutationFn: async (proof: SystemBackupProof) => {
      if (!preview) throw new Error(t('Preview the backup before importing.'))
      const response = await applySystemBackupImport(
        fileContent,
        preview.hash,
        proof.proof_token
      )
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Failed to import the backup.'))
      }
      return response.data
    },
    onSuccess: () => {
      setConfirmOpen(false)
      setImportProof(undefined)
      clearAuthenticatedClientState(queryClient)
      void navigate({ to: '/sign-in', replace: true })
    },
    onError: (error) => {
      setConfirmOpen(false)
      if (isSystemBackupProofError(error)) {
        setImportProof(undefined)
        setProtectedAction('apply')
        setCredentialsOpen(true)
        return
      }
      toast.error(
        getSystemBackupErrorMessage(error) || t('Failed to import the backup.')
      )
    },
  })

  const acceptFile = async (nextFile: File | undefined) => {
    if (!nextFile) return
    setFile(null)
    setFileContent('')
    setPreview(null)
    if (!nextFile.name.toLowerCase().endsWith('.json')) {
      toast.error(t('Select a JSON backup file.'))
      return
    }
    if (nextFile.size > MAX_SYSTEM_BACKUP_SIZE) {
      toast.error(t('The backup file must not exceed 128 MiB.'))
      return
    }
    try {
      const content = await nextFile.text()
      const validationError = validateSystemBackupText(content)
      if (validationError === 'invalid_json') {
        toast.error(t('The selected file is not valid JSON.'))
        return
      }
      if (validationError === 'invalid_schema') {
        toast.error(t('The selected file is not a full system backup.'))
        return
      }
      setFile(nextFile)
      setFileContent(content)
    } catch {
      toast.error(t('Failed to read the selected backup file.'))
    }
  }

  const requestProtectedAction = (action: ProtectedAction) => {
    if (
      action !== 'export' &&
      isSystemBackupProofUsable(importProof, IMPORT_SCOPE)
    ) {
      if (action === 'preview') previewMutation.mutate(importProof)
      if (action === 'apply') setConfirmOpen(true)
      return
    }
    setProtectedAction(action)
    setCredentialsOpen(true)
  }

  const handleVerified = (proof: SystemBackupProof) => {
    if (protectedAction === 'export') {
      exportMutation.mutate(proof.proof_token)
      return
    }
    setImportProof(proof)
    if (protectedAction === 'preview') previewMutation.mutate(proof)
    if (protectedAction === 'apply') setConfirmOpen(true)
  }

  const handleApply = () => {
    if (!preview || preview.conflicts.length > 0) return
    if (!isSystemBackupProofUsable(importProof, IMPORT_SCOPE)) {
      setConfirmOpen(false)
      setProtectedAction('apply')
      setCredentialsOpen(true)
      return
    }
    applyMutation.mutate(importProof)
  }

  const credentialScope =
    protectedAction === 'export' ? EXPORT_SCOPE : IMPORT_SCOPE

  return (
    <section
      className='flex flex-col gap-5'
      aria-labelledby='full-backup-title'
    >
      <div>
        <h4 id='full-backup-title' className='text-sm font-medium'>
          {t('Full system backup')}
        </h4>
        <p className='text-muted-foreground mt-1 text-sm'>
          {t(
            'Move all persisted configuration, users, credentials, keys, and authentication data between installations.'
          )}
        </p>
      </div>

      <Alert variant='destructive'>
        <ShieldAlertIcon aria-hidden='true' />
        <AlertTitle>{t('Plaintext backup contains secrets')}</AlertTitle>
        <AlertDescription>
          {t(
            'The downloaded JSON contains channel keys, system secrets, password hashes, API tokens, and authentication material. Store it securely and delete it when it is no longer needed.'
          )}
        </AlertDescription>
      </Alert>

      <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
        <div className='min-w-0'>
          <h5 className='text-sm font-medium'>{t('Export full backup')}</h5>
          <p className='text-muted-foreground mt-1 text-sm'>
            {t('Root administrator password verification is required.')}
          </p>
        </div>
        <Button
          type='button'
          variant='outline'
          onClick={() => requestProtectedAction('export')}
          disabled={exportMutation.isPending}
        >
          {exportMutation.isPending ? (
            <Loader2Icon className='animate-spin' aria-hidden='true' />
          ) : (
            <DownloadIcon aria-hidden='true' />
          )}
          {t('Export full backup')}
        </Button>
      </div>

      <Separator />

      <div className='flex flex-col gap-4'>
        <div>
          <h5 className='text-sm font-medium'>{t('Import full backup')}</h5>
          <p className='text-muted-foreground mt-1 text-sm'>
            {t(
              'This exact replacement includes secrets and accounts. Runtime history and deployment environment variables remain unchanged.'
            )}
          </p>
        </div>

        <div
          className='border-border hover:bg-muted/40 focus-within:border-ring flex min-h-28 w-full flex-col items-center justify-center gap-3 rounded-md border border-dashed p-5 text-center transition-colors'
          onDragOver={(event) => event.preventDefault()}
          onDrop={(event: DragEvent<HTMLDivElement>) => {
            event.preventDefault()
            void acceptFile(event.dataTransfer.files[0])
          }}
        >
          <FileJsonIcon
            className='text-muted-foreground size-6'
            aria-hidden='true'
          />
          <div className='max-w-full min-w-0'>
            <p className='truncate text-sm font-medium'>
              {file?.name ?? t('No full backup file selected')}
            </p>
            {file && (
              <p className='text-muted-foreground mt-1 text-xs'>
                {t('{{size}} MiB', {
                  size: Math.max(0.01, file.size / 1024 / 1024).toFixed(2),
                })}
              </p>
            )}
          </div>
          <input
            ref={fileInputRef}
            type='file'
            accept='.json,application/json'
            className='sr-only'
            onChange={(event) => {
              void acceptFile(event.target.files?.[0])
              event.target.value = ''
            }}
          />
          <Button
            type='button'
            variant='secondary'
            size='sm'
            onClick={() => fileInputRef.current?.click()}
          >
            <UploadIcon aria-hidden='true' />
            {t('Select backup file')}
          </Button>
        </div>

        <div className='flex justify-end'>
          <Button
            type='button'
            onClick={() => requestProtectedAction('preview')}
            disabled={!fileContent || previewMutation.isPending}
          >
            {previewMutation.isPending && (
              <Loader2Icon className='animate-spin' aria-hidden='true' />
            )}
            {t('Preview full import')}
          </Button>
        </div>
      </div>

      {preview && (
        <SystemBackupPreview
          preview={preview}
          applying={applyMutation.isPending}
          onApply={() => requestProtectedAction('apply')}
        />
      )}

      <SystemBackupCredentialsDialog
        open={credentialsOpen}
        scope={credentialScope}
        onOpenChange={setCredentialsOpen}
        onVerified={handleVerified}
      />

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Replace current system data?')}
        desc={t(
          'All records in the full backup scope will be replaced, including keys, secrets, users, tokens, redemptions, and authentication data. Current login sessions will end after a successful import. This action cannot be undone.'
        )}
        confirmText={t('Replace and sign out')}
        destructive
        handleConfirm={handleApply}
        isLoading={applyMutation.isPending}
      >
        {preview && (
          <Alert variant='destructive'>
            <AlertTriangleIcon aria-hidden='true' />
            <AlertTitle>{t('Exact replacement')}</AlertTitle>
            <AlertDescription>
              {t('{{count}} records will be loaded from this backup.', {
                count: preview.record_count,
              })}
            </AlertDescription>
          </Alert>
        )}
      </ConfirmDialog>
    </section>
  )
}
