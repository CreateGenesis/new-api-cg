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
import { Loader2Icon } from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { verifySystemBackupPassword } from '../api'
import type { SystemBackupProof, SystemBackupProofScope } from '../types'
import { getSystemBackupErrorMessage } from './system-backup-utils'

const credentialsSchema = z.object({
  username: z.string().trim().min(1),
  password: z.string().min(1),
})

type CredentialsForm = z.infer<typeof credentialsSchema>

type SystemBackupCredentialsDialogProps = {
  open: boolean
  scope: SystemBackupProofScope
  onOpenChange: (open: boolean) => void
  onVerified: (proof: SystemBackupProof) => void
}

export function SystemBackupCredentialsDialog(
  props: SystemBackupCredentialsDialogProps
) {
  const { t } = useTranslation()
  const [serverError, setServerError] = useState('')
  const form = useForm<CredentialsForm>({
    resolver: zodResolver(credentialsSchema),
    defaultValues: { username: '', password: '' },
  })

  const resetSensitiveState = () => {
    form.reset({ username: '', password: '' })
    setServerError('')
  }

  const handleOpenChange = (open: boolean) => {
    if (!open) resetSensitiveState()
    props.onOpenChange(open)
  }

  const handleSubmit = form.handleSubmit(async (values) => {
    setServerError('')
    try {
      const response = await verifySystemBackupPassword({
        username: values.username.trim(),
        password: values.password,
        scope: props.scope,
      })
      if (!response.success || !response.data) {
        setServerError(
          response.message || t('Administrator verification failed.')
        )
        return
      }
      const proof = response.data
      resetSensitiveState()
      props.onOpenChange(false)
      props.onVerified(proof)
    } catch (error) {
      setServerError(
        getSystemBackupErrorMessage(error) ||
          t('Administrator verification failed.')
      )
    }
  })

  const isExport = props.scope === 'system.config.full.export'

  return (
    <Dialog open={props.open} onOpenChange={handleOpenChange}>
      <DialogContent className='sm:max-w-md'>
        <DialogHeader>
          <DialogTitle>
            {isExport
              ? t('Verify full backup export')
              : t('Verify full backup import')}
          </DialogTitle>
          <DialogDescription>
            {t(
              'Enter the username and password of the Root administrator currently signed in.'
            )}
          </DialogDescription>
        </DialogHeader>

        <form id='system-backup-credentials' onSubmit={handleSubmit}>
          <FieldGroup>
            <Field data-invalid={Boolean(form.formState.errors.username)}>
              <FieldLabel htmlFor='system-backup-username'>
                {t('Administrator username')}
              </FieldLabel>
              <Input
                id='system-backup-username'
                autoComplete='username'
                autoFocus
                aria-invalid={Boolean(form.formState.errors.username)}
                disabled={form.formState.isSubmitting}
                {...form.register('username')}
              />
              <FieldError>
                {form.formState.errors.username
                  ? t('Administrator username is required.')
                  : null}
              </FieldError>
            </Field>

            <Field data-invalid={Boolean(form.formState.errors.password)}>
              <FieldLabel htmlFor='system-backup-password'>
                {t('Current Password')}
              </FieldLabel>
              <Input
                id='system-backup-password'
                type='password'
                autoComplete='current-password'
                aria-invalid={Boolean(form.formState.errors.password)}
                disabled={form.formState.isSubmitting}
                {...form.register('password')}
              />
              <FieldError>
                {form.formState.errors.password
                  ? t('Current password is required.')
                  : null}
              </FieldError>
            </Field>

            <FieldError>{serverError}</FieldError>
          </FieldGroup>
        </form>

        <DialogFooter>
          <Button
            type='button'
            variant='outline'
            onClick={() => handleOpenChange(false)}
            disabled={form.formState.isSubmitting}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='system-backup-credentials'
            disabled={form.formState.isSubmitting}
          >
            {form.formState.isSubmitting && (
              <Loader2Icon className='animate-spin' aria-hidden='true' />
            )}
            {t('Verify and continue')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
