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
import { AlertTriangleIcon, CheckCircle2Icon, UploadIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import type { SystemBackupImportPreview, SystemBackupSection } from '../types'

const SECTION_LABELS: Record<SystemBackupSection, string> = {
  options: 'System settings',
  channels: 'Channels',
  catalog: 'Vendors, models, and setup',
  oauth: 'Custom OAuth providers',
  authorization: 'Roles and authorization rules',
  users: 'Users',
  tokens: 'API tokens',
  redemptions: 'Redemption codes',
  authentication: 'Authentication credentials',
  subscriptions: 'Subscription plans and assignments',
}

const SECTION_ORDER = Object.keys(SECTION_LABELS) as SystemBackupSection[]

type SystemBackupPreviewProps = {
  preview: SystemBackupImportPreview
  applying: boolean
  onApply: () => void
}

export function SystemBackupPreview(props: SystemBackupPreviewProps) {
  const { t } = useTranslation()
  const hasConflicts = props.preview.conflicts.length > 0

  return (
    <div className='flex flex-col gap-4 border-t pt-5'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <h5 className='text-sm font-medium'>{t('Full import preview')}</h5>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('{{count}} records in the backup', {
              count: props.preview.record_count,
            })}
          </p>
        </div>
        <span className='text-muted-foreground font-mono text-xs'>
          {props.preview.hash.slice(0, 12)}
        </span>
      </div>

      <div className='grid gap-2 sm:grid-cols-2 xl:grid-cols-5'>
        {SECTION_ORDER.map((section) => {
          const counts = props.preview.sections[section]
          return (
            <div key={section} className='min-w-0 rounded-md border p-3'>
              <div className='truncate text-sm font-medium'>
                {t(SECTION_LABELS[section])}
              </div>
              <div className='mt-3 flex flex-wrap gap-1.5'>
                <Badge variant='secondary'>
                  {t('Add {{count}}', { count: counts.add })}
                </Badge>
                <Badge variant='outline'>
                  {t('Update {{count}}', { count: counts.update })}
                </Badge>
                <Badge variant='destructive'>
                  {t('Delete {{count}}', { count: counts.delete })}
                </Badge>
                <Badge variant='ghost'>
                  {t('Unchanged {{count}}', { count: counts.unchanged })}
                </Badge>
              </div>
            </div>
          )
        })}
      </div>

      {hasConflicts ? (
        <Alert variant='destructive'>
          <AlertTriangleIcon aria-hidden='true' />
          <AlertTitle>{t('Full import cannot be applied')}</AlertTitle>
          <AlertDescription>
            {t('Resolve the validation conflicts and preview the file again.')}
          </AlertDescription>
        </Alert>
      ) : (
        <Alert>
          <CheckCircle2Icon aria-hidden='true' />
          <AlertTitle>{t('Ready for exact replacement')}</AlertTitle>
          <AlertDescription>
            {t(
              'The listed records will replace the current records in one transaction.'
            )}
          </AlertDescription>
        </Alert>
      )}

      <div className='flex justify-end'>
        <Button
          type='button'
          variant='destructive'
          onClick={props.onApply}
          disabled={hasConflicts || props.applying}
        >
          <UploadIcon aria-hidden='true' />
          {t('Replace with this backup')}
        </Button>
      </div>
    </div>
  )
}
