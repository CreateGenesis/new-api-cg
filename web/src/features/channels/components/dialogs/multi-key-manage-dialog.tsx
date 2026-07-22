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
import {
  Copy01Icon,
  GaugeIcon,
  Loading03Icon,
  Tick02Icon,
  ViewIcon,
  ViewOffSlashIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQueryClient } from '@tanstack/react-query'
import { Loader2, RefreshCw, Trash2, Power, PowerOff } from 'lucide-react'
import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { copyToClipboard } from '@/lib/copy-to-clipboard'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  getMultiKeyStatus,
  enableMultiKey,
  disableMultiKey,
  deleteMultiKey,
  enableAllMultiKeys,
  disableAllMultiKeys,
  deleteDisabledMultiKeys,
  getMultiKeyChannelKey,
} from '../../api'
import { MULTI_KEY_FILTER_OPTIONS, MULTI_KEY_MODES } from '../../constants'
import {
  channelsQueryKeys,
  formatTimestamp,
  getMultiKeyStatusConfig,
  getMultiKeyConfirmMessage,
  handleTestChannel,
  isDestructiveAction,
} from '../../lib'
import type { KeyStatus, MultiKeyConfirmAction } from '../../types'
import { useChannels } from '../channels-provider'
import { StatisticsCard } from './multi-key-statistics-card'
import { MultiKeyTableRowActions } from './multi-key-table-row-actions'

type MultiKeyManageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function MultiKeyManageDialog({
  open,
  onOpenChange,
}: MultiKeyManageDialogProps) {
  const { t } = useTranslation()
  const { currentRow } = useChannels()
  const queryClient = useQueryClient()
  const currentUser = useAuthStore((s) => s.auth.user)
  const canEditSensitive = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const canRevealKeys = currentUser?.role === ROLE.SUPER_ADMIN

  // Data state
  const [isLoading, setIsLoading] = useState(false)
  const [keys, setKeys] = useState<KeyStatus[]>([])
  const [keysChannelId, setKeysChannelId] = useState<number | null>(null)
  const [currentPage, setCurrentPage] = useState(1)
  const [pageSize, setPageSize] = useState(10)
  const [total, setTotal] = useState(0)
  const [totalPages, setTotalPages] = useState(0)
  const [enabledCount, setEnabledCount] = useState(0)
  const [manualDisabledCount, setManualDisabledCount] = useState(0)
  const [autoDisabledCount, setAutoDisabledCount] = useState(0)

  // UI state
  const [statusFilter, setStatusFilter] = useState<number | null>(null)
  const [confirmAction, setConfirmAction] =
    useState<MultiKeyConfirmAction | null>(null)
  const [isPerformingAction, setIsPerformingAction] = useState(false)
  const [testingKeyIndexes, setTestingKeyIndexes] = useState<Set<number>>(
    new Set()
  )

  // Complete keys are scoped to the current dialog/channel and fetched on demand.
  const [loadedKeys, setLoadedKeys] = useState<Record<number, string>>({})
  const [loadedKeysChannelId, setLoadedKeysChannelId] = useState<number | null>(
    null
  )
  const [revealedKeyIndexes, setRevealedKeyIndexes] = useState<Set<number>>(
    new Set()
  )
  const [loadingKeyIndexes, setLoadingKeyIndexes] = useState<Set<number>>(
    new Set()
  )
  const [copyingKeyIndexes, setCopyingKeyIndexes] = useState<Set<number>>(
    new Set()
  )
  const [copiedKeyIndexes, setCopiedKeyIndexes] = useState<Set<number>>(
    new Set()
  )
  const loadedKeysRef = useRef<Record<number, string>>({})
  const loadedKeysChannelIdRef = useRef<number | null>(null)
  const keyRequestsRef = useRef<Map<number, Promise<string | null>>>(new Map())
  const copiedTimersRef = useRef<Map<number, ReturnType<typeof setTimeout>>>(
    new Map()
  )
  const testingKeyIndexesRef = useRef<Set<number>>(new Set())
  const testGenerationRef = useRef(0)
  const secretGenerationRef = useRef(0)
  const statusGenerationRef = useRef(0)
  const secretScopeRef = useRef<number | null>(null)
  secretScopeRef.current = open && currentRow ? currentRow.id : null

  const clearLoadedSecrets = () => {
    secretGenerationRef.current += 1
    loadedKeysRef.current = {}
    loadedKeysChannelIdRef.current = null
    keyRequestsRef.current.clear()
    for (const timer of copiedTimersRef.current.values()) {
      clearTimeout(timer)
    }
    copiedTimersRef.current.clear()
    setLoadedKeys({})
    setLoadedKeysChannelId(null)
    setRevealedKeyIndexes(new Set())
    setLoadingKeyIndexes(new Set())
    setCopyingKeyIndexes(new Set())
    setCopiedKeyIndexes(new Set())
  }

  // Reset and load data when dialog opens
  useEffect(() => {
    statusGenerationRef.current += 1
    testGenerationRef.current += 1
    testingKeyIndexesRef.current.clear()
    setTestingKeyIndexes(new Set())
    clearLoadedSecrets()
    setKeys([])
    setKeysChannelId(null)
    setIsLoading(false)
    if (open && currentRow) {
      setCurrentPage(1)
      setStatusFilter(null)
      loadKeyStatus(1, pageSize, null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, currentRow?.id])

  const loadKeyStatus = async (
    page: number = currentPage,
    size: number = pageSize,
    status: number | null = statusFilter
  ) => {
    if (!currentRow) return

    const channelId = currentRow.id
    const generation = statusGenerationRef.current + 1
    statusGenerationRef.current = generation
    clearLoadedSecrets()
    setIsLoading(true)
    try {
      const response = await getMultiKeyStatus(
        channelId,
        page,
        size,
        status === null ? undefined : status
      )

      if (
        generation !== statusGenerationRef.current ||
        secretScopeRef.current !== channelId
      ) {
        return
      }

      if (response.success && response.data) {
        setKeys(response.data.keys || [])
        setKeysChannelId(channelId)
        setTotal(response.data.total || 0)
        setCurrentPage(response.data.page || 1)
        setPageSize(response.data.page_size || 10)
        setTotalPages(response.data.total_pages || 0)
        setEnabledCount(response.data.enabled_count || 0)
        setManualDisabledCount(response.data.manual_disabled_count || 0)
        setAutoDisabledCount(response.data.auto_disabled_count || 0)
      } else {
        toast.error(response.message || t('Failed to load key status'))
      }
    } catch (error: unknown) {
      if (
        generation === statusGenerationRef.current &&
        secretScopeRef.current === channelId
      ) {
        toast.error(
          error instanceof Error
            ? error.message
            : t('Failed to load key status')
        )
      }
    } finally {
      if (generation === statusGenerationRef.current) {
        setIsLoading(false)
      }
    }
  }

  const loadFullKey = async (
    keyIndex: number,
    failureMessage: string
  ): Promise<string | null> => {
    if (!currentRow || !canRevealKeys) return null
    if (secretScopeRef.current !== currentRow.id) return null
    if (
      loadedKeysChannelIdRef.current === currentRow.id &&
      Object.hasOwn(loadedKeysRef.current, keyIndex)
    ) {
      return loadedKeysRef.current[keyIndex]
    }

    const pendingRequest = keyRequestsRef.current.get(keyIndex)
    if (pendingRequest) return pendingRequest

    const channelId = currentRow.id
    const generation = secretGenerationRef.current
    setLoadingKeyIndexes((previous) => new Set(previous).add(keyIndex))

    const request = (async () => {
      try {
        const response = await getMultiKeyChannelKey(channelId, keyIndex)
        if (
          generation !== secretGenerationRef.current ||
          secretScopeRef.current !== channelId
        ) {
          return null
        }
        if (!response.success || typeof response.data?.key !== 'string') {
          toast.error(t(failureMessage))
          return null
        }

        const fullKey = response.data.key
        loadedKeysRef.current = {
          ...loadedKeysRef.current,
          [keyIndex]: fullKey,
        }
        loadedKeysChannelIdRef.current = channelId
        setLoadedKeys((previous) => ({ ...previous, [keyIndex]: fullKey }))
        setLoadedKeysChannelId(channelId)
        return fullKey
      } catch {
        if (
          generation === secretGenerationRef.current &&
          secretScopeRef.current === channelId
        ) {
          toast.error(t(failureMessage))
        }
        return null
      } finally {
        if (generation === secretGenerationRef.current) {
          setLoadingKeyIndexes((previous) => {
            const next = new Set(previous)
            next.delete(keyIndex)
            return next
          })
          keyRequestsRef.current.delete(keyIndex)
        }
      }
    })()

    keyRequestsRef.current.set(keyIndex, request)
    return request
  }

  const handleToggleKeyVisibility = async (keyIndex: number) => {
    if (revealedKeyIndexes.has(keyIndex)) {
      setRevealedKeyIndexes((previous) => {
        const next = new Set(previous)
        next.delete(keyIndex)
        return next
      })
      return
    }

    const fullKey = await loadFullKey(keyIndex, 'Failed to fetch channel key')
    if (fullKey === null) return
    setRevealedKeyIndexes((previous) => new Set(previous).add(keyIndex))
  }

  const handleCopyKey = async (keyIndex: number) => {
    setCopyingKeyIndexes((previous) => new Set(previous).add(keyIndex))
    try {
      const fullKey = await loadFullKey(keyIndex, 'Failed to copy')
      if (fullKey === null) return
      if (!(await copyToClipboard(fullKey))) {
        toast.error(t('Failed to copy'))
        return
      }

      toast.success(t('Copied!'))
      setCopiedKeyIndexes((previous) => new Set(previous).add(keyIndex))
      const previousTimer = copiedTimersRef.current.get(keyIndex)
      if (previousTimer) clearTimeout(previousTimer)
      copiedTimersRef.current.set(
        keyIndex,
        setTimeout(() => {
          setCopiedKeyIndexes((previous) => {
            const next = new Set(previous)
            next.delete(keyIndex)
            return next
          })
          copiedTimersRef.current.delete(keyIndex)
        }, 2000)
      )
    } finally {
      setCopyingKeyIndexes((previous) => {
        const next = new Set(previous)
        next.delete(keyIndex)
        return next
      })
    }
  }

  const handleTestKey = async (keyIndex: number) => {
    if (!currentRow || testingKeyIndexesRef.current.has(keyIndex)) return

    const channelId = currentRow.id
    const generation = testGenerationRef.current
    testingKeyIndexesRef.current.add(keyIndex)
    setTestingKeyIndexes((previous) => new Set(previous).add(keyIndex))
    try {
      await handleTestChannel(
        channelId,
        { channelName: currentRow.name, keyIndex },
        () => {
          queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })
        }
      )
    } finally {
      if (
        generation === testGenerationRef.current &&
        secretScopeRef.current === channelId
      ) {
        testingKeyIndexesRef.current.delete(keyIndex)
        setTestingKeyIndexes((previous) => {
          const next = new Set(previous)
          next.delete(keyIndex)
          return next
        })
      }
    }
  }

  const handleStatusFilterChange = (value: string) => {
    const newFilter = value === 'all' ? null : Number.parseInt(value)
    setStatusFilter(newFilter)
    setCurrentPage(1)
    loadKeyStatus(1, pageSize, newFilter)
  }

  const handlePageChange = (newPage: number) => {
    setCurrentPage(newPage)
    loadKeyStatus(newPage, pageSize)
  }

  const performAction = async () => {
    if (!confirmAction || !currentRow) return
    if (
      !canEditSensitive &&
      (confirmAction.type === 'delete' ||
        confirmAction.type === 'delete-disabled')
    ) {
      setConfirmAction(null)
      return
    }

    setIsPerformingAction(true)
    try {
      const { type, keyIndex } = confirmAction
      let response

      // Execute the appropriate action
      if (type === 'enable' && keyIndex !== undefined) {
        response = await enableMultiKey(currentRow.id, keyIndex)
      } else if (type === 'disable' && keyIndex !== undefined) {
        response = await disableMultiKey(currentRow.id, keyIndex)
      } else if (type === 'delete' && keyIndex !== undefined) {
        response = await deleteMultiKey(currentRow.id, keyIndex)
      } else if (type === 'enable-all') {
        response = await enableAllMultiKeys(currentRow.id)
      } else if (type === 'disable-all') {
        response = await disableAllMultiKeys(currentRow.id)
      } else if (type === 'delete-disabled') {
        response = await deleteDisabledMultiKeys(currentRow.id)
      }

      if (response?.success) {
        toast.success(response.message || t('Operation successful'))
        queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })

        // Reload data - reset to page 1 for bulk actions
        const isBulkAction = type.includes('all') || type === 'delete-disabled'
        if (isBulkAction) {
          setCurrentPage(1)
          loadKeyStatus(1, pageSize)
        } else {
          loadKeyStatus(currentPage, pageSize)
        }
      } else {
        toast.error(response?.message || t('Operation failed'))
      }
    } catch (error: unknown) {
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      setIsPerformingAction(false)
      setConfirmAction(null)
    }
  }

  const renderStatusBadge = (status: number) => {
    const config = getMultiKeyStatusConfig(status)
    return (
      <StatusBadge
        label={t(config.label)}
        variant={config.variant}
        showDot
        copyable={false}
      />
    )
  }

  const formatKeyTimestamp = (timestamp?: number) => {
    if (!timestamp) return '-'
    return formatTimestamp(timestamp)
  }

  if (!currentRow) return null

  const visibleKeys = keysChannelId === currentRow.id ? keys : []
  const multiKeyMode = currentRow.channel_info?.multi_key_mode
  const multiKeyModeLabel = MULTI_KEY_MODES.find(
    (mode) => mode.value === multiKeyMode
  )?.label

  return (
    <>
      <Dialog
        open={open}
        onOpenChange={onOpenChange}
        title={
          <>
            {t('Multi-Key Management')}
            <StatusBadge
              label={currentRow.name}
              variant='neutral'
              copyable={false}
            />
            {multiKeyMode && (
              <StatusBadge
                label={t(multiKeyModeLabel || 'Random')}
                variant='neutral'
                copyable={false}
              />
            )}
          </>
        }
        description={t(
          'Manage multi-key status and configuration for this channel'
        )}
        contentClassName='flex max-h-[90vh] max-w-5xl flex-col'
        titleClassName='flex items-center gap-2'
        contentHeight='min(72vh, 720px)'
        bodyClassName='space-y-4'
      >
        <div className='flex min-h-0 flex-1 flex-col space-y-4 overflow-hidden'>
          {/* Statistics */}
          <div className='grid shrink-0 grid-cols-3 gap-3'>
            <StatisticsCard
              label={t('Enabled')}
              count={enabledCount}
              total={total}
            />
            <StatisticsCard
              label={t('Manual Disabled')}
              count={manualDisabledCount}
              total={total}
            />
            <StatisticsCard
              label={t('Auto Disabled')}
              count={autoDisabledCount}
              total={total}
            />
          </div>

          <Separator className='shrink-0' />

          {/* Toolbar */}
          <div className='flex shrink-0 items-center justify-between'>
            <Select
              items={MULTI_KEY_FILTER_OPTIONS.map((option) => ({
                value: option.value,
                label: t(option.label),
              }))}
              value={statusFilter === null ? 'all' : statusFilter.toString()}
              onValueChange={(v) => v !== null && handleStatusFilterChange(v)}
            >
              <SelectTrigger className='w-40'>
                <SelectValue placeholder={t('All Status')} />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {MULTI_KEY_FILTER_OPTIONS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {t(option.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>

            <div className='flex items-center gap-2'>
              <Button
                variant='outline'
                size='sm'
                onClick={() => loadKeyStatus()}
                disabled={isLoading}
              >
                <RefreshCw className='h-4 w-4' />
              </Button>

              {manualDisabledCount + autoDisabledCount > 0 && (
                <Button
                  variant='default'
                  size='sm'
                  onClick={() => setConfirmAction({ type: 'enable-all' })}
                >
                  <Power className='mr-2 h-4 w-4' />
                  {t('Enable All')}
                </Button>
              )}

              {enabledCount > 0 && (
                <Button
                  variant='destructive'
                  size='sm'
                  onClick={() => setConfirmAction({ type: 'disable-all' })}
                >
                  <PowerOff className='mr-2 h-4 w-4' />
                  {t('Disable All')}
                </Button>
              )}

              {autoDisabledCount > 0 && (
                <Button
                  variant='destructive'
                  size='sm'
                  onClick={() => {
                    if (!canEditSensitive) return
                    setConfirmAction({ type: 'delete-disabled' })
                  }}
                  disabled={!canEditSensitive}
                  title={
                    canEditSensitive
                      ? undefined
                      : t('No permission to perform this action')
                  }
                >
                  <Trash2 className='mr-2 h-4 w-4' />
                  {t('Delete Auto-Disabled')}
                </Button>
              )}
            </div>
          </div>
          {!canEditSensitive && (
            <p className='text-muted-foreground text-xs'>
              {t('No permission to perform this action')}
            </p>
          )}

          {/* Table */}
          <div className='min-h-0 flex-1 overflow-auto rounded-md border'>
            {isLoading && (
              <div className='flex items-center justify-center py-12'>
                <Loader2 className='text-muted-foreground h-8 w-8 animate-spin' />
              </div>
            )}
            {!isLoading && visibleKeys.length === 0 && (
              <div className='text-muted-foreground py-12 text-center'>
                {t('No keys found')}
              </div>
            )}
            {!isLoading && visibleKeys.length > 0 && (
              <StaticDataTable
                className='rounded-none border-0'
                tableClassName='min-w-[1110px]'
                data={visibleKeys}
                getRowKey={(key) => key.index}
                columns={[
                  {
                    id: 'index',
                    header: t('Index'),
                    className: 'w-20',
                    cellClassName: 'font-mono text-sm',
                    cell: (key) => `#${key.index + 1}`,
                  },
                  {
                    id: 'test',
                    header: t('Test'),
                    className: 'w-16 text-center',
                    cellClassName: 'text-center',
                    cell: (key) => {
                      const isTesting = testingKeyIndexes.has(key.index)
                      const testLabel = t('Test channel key #{{index}}', {
                        index: key.index + 1,
                      })
                      return (
                        <Tooltip>
                          <TooltipTrigger
                            render={
                              <Button
                                variant='ghost'
                                size='icon-xs'
                                onClick={() => void handleTestKey(key.index)}
                                disabled={isTesting}
                                aria-label={testLabel}
                              />
                            }
                          >
                            <HugeiconsIcon
                              icon={isTesting ? Loading03Icon : GaugeIcon}
                              className={isTesting ? 'animate-spin' : undefined}
                              strokeWidth={2}
                            />
                          </TooltipTrigger>
                          <TooltipContent>{testLabel}</TooltipContent>
                        </Tooltip>
                      )
                    },
                  },
                  {
                    id: 'key',
                    header: t('Channel key'),
                    className: 'min-w-[280px]',
                    cell: (key) => {
                      const isRevealed = revealedKeyIndexes.has(key.index)
                      const isLoadingKey = loadingKeyIndexes.has(key.index)
                      const isCopyingKey = copyingKeyIndexes.has(key.index)
                      const isCopied = copiedKeyIndexes.has(key.index)
                      let displayedKey = key.masked_key || key.key_preview || ''
                      if (
                        open &&
                        loadedKeysChannelId === currentRow.id &&
                        isRevealed &&
                        Object.hasOwn(loadedKeys, key.index)
                      ) {
                        displayedKey = loadedKeys[key.index]
                      }

                      let visibilityIcon = ViewIcon
                      if (isLoadingKey) {
                        visibilityIcon = Loading03Icon
                      } else if (isRevealed) {
                        visibilityIcon = ViewOffSlashIcon
                      }

                      let copyIcon = Copy01Icon
                      let copyLabel = 'Copy secret key'
                      let copyIconClassName: string | undefined
                      if (isCopyingKey) {
                        copyIcon = Loading03Icon
                        copyLabel = 'Loading...'
                        copyIconClassName = 'animate-spin'
                      } else if (isCopied) {
                        copyIcon = Tick02Icon
                        copyLabel = 'Copied!'
                        copyIconClassName = 'text-success'
                      }

                      return (
                        <div className='flex min-w-0 items-center gap-1'>
                          <code className='min-w-0 flex-1 truncate font-mono text-sm'>
                            {displayedKey}
                          </code>
                          {canRevealKeys && (
                            <>
                              <Tooltip>
                                <TooltipTrigger
                                  render={
                                    <Button
                                      variant='ghost'
                                      size='icon-xs'
                                      onClick={() =>
                                        void handleToggleKeyVisibility(
                                          key.index
                                        )
                                      }
                                      disabled={isLoadingKey || isCopyingKey}
                                      aria-label={t(
                                        isRevealed ? 'Hide' : 'Show'
                                      )}
                                    />
                                  }
                                >
                                  <HugeiconsIcon
                                    icon={visibilityIcon}
                                    className={
                                      isLoadingKey ? 'animate-spin' : undefined
                                    }
                                    strokeWidth={2}
                                  />
                                </TooltipTrigger>
                                <TooltipContent>
                                  {t(isRevealed ? 'Hide' : 'Show')}
                                </TooltipContent>
                              </Tooltip>
                              <Tooltip>
                                <TooltipTrigger
                                  render={
                                    <Button
                                      variant='ghost'
                                      size='icon-xs'
                                      onClick={() =>
                                        void handleCopyKey(key.index)
                                      }
                                      disabled={isLoadingKey || isCopyingKey}
                                      aria-label={t(copyLabel)}
                                    />
                                  }
                                >
                                  <HugeiconsIcon
                                    icon={copyIcon}
                                    className={copyIconClassName}
                                    strokeWidth={2}
                                  />
                                </TooltipTrigger>
                                <TooltipContent>{t(copyLabel)}</TooltipContent>
                              </Tooltip>
                            </>
                          )}
                        </div>
                      )
                    },
                  },
                  {
                    id: 'status',
                    header: t('Status'),
                    className: 'w-32',
                    cell: (key) => renderStatusBadge(key.status),
                  },
                  {
                    id: 'reason',
                    header: t('Disabled Reason'),
                    className: 'min-w-[200px]',
                    cellClassName: 'max-w-xs truncate text-sm',
                    cell: (key) => key.reason || '-',
                  },
                  {
                    id: 'disabled-time',
                    header: t('Disabled Time'),
                    className: 'w-44',
                    cellClassName: 'text-muted-foreground text-sm',
                    cell: (key) => formatKeyTimestamp(key.disabled_time),
                  },
                  {
                    id: 'actions',
                    header: t('Actions'),
                    className: 'text-right',
                    cell: (key) => (
                      <MultiKeyTableRowActions
                        keyIndex={key.index}
                        status={key.status}
                        canDelete={canEditSensitive}
                        onAction={setConfirmAction}
                      />
                    ),
                  },
                ]}
              />
            )}
          </div>

          {/* Pagination */}
          {totalPages > 1 && (
            <div className='flex shrink-0 items-center justify-between'>
              <div className='text-muted-foreground text-sm'>
                {t('Page {{current}} of {{total}}', {
                  current: currentPage,
                  total: totalPages,
                })}
              </div>
              <div className='flex gap-2'>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => handlePageChange(currentPage - 1)}
                  disabled={currentPage === 1 || isLoading}
                >
                  {t('Previous')}
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => handlePageChange(currentPage + 1)}
                  disabled={currentPage >= totalPages || isLoading}
                >
                  {t('Next')}
                </Button>
              </div>
            </div>
          )}
        </div>
      </Dialog>

      {/* Confirmation Dialog */}
      <ConfirmDialog
        open={confirmAction !== null}
        onOpenChange={(open) => !open && setConfirmAction(null)}
        title={t('Confirm Action')}
        desc={t(getMultiKeyConfirmMessage(confirmAction))}
        destructive={isDestructiveAction(confirmAction)}
        isLoading={isPerformingAction}
        handleConfirm={performAction}
      />
    </>
  )
}
