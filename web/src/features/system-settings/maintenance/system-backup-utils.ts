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
import axios from 'axios'

import type { SystemBackupProof, SystemBackupProofScope } from '../types'

export const MAX_SYSTEM_BACKUP_SIZE = 128 * 1024 * 1024

export type SystemBackupFileError = 'invalid_json' | 'invalid_schema'

export function validateSystemBackupText(
  content: string
): SystemBackupFileError | null {
  try {
    const value: unknown = JSON.parse(content)
    if (!value || typeof value !== 'object') return 'invalid_schema'
    const metadata = value as Record<string, unknown>
    if (
      metadata.schema !== 'new-api.system-backup' ||
      metadata.version !== 1 ||
      metadata.sensitive !== true ||
      metadata.restore_mode !== 'replace'
    ) {
      return 'invalid_schema'
    }
    return null
  } catch {
    return 'invalid_json'
  }
}

export function isSystemBackupProofUsable(
  proof: SystemBackupProof | undefined,
  scope: SystemBackupProofScope,
  nowSeconds = Date.now() / 1000
): proof is SystemBackupProof {
  return Boolean(
    proof?.proof_token &&
    proof.scope === scope &&
    proof.method === 'password' &&
    proof.expires_at > nowSeconds + 5
  )
}

export function isSystemBackupProofError(error: unknown): boolean {
  if (!axios.isAxiosError(error)) return false
  const data: unknown = error.response?.data
  if (!data || typeof data !== 'object') return false
  const code = (data as Record<string, unknown>).code
  return (
    code === 'SECURITY_PROOF_REQUIRED' ||
    code === 'SECURITY_PROOF_EXPIRED' ||
    code === 'SECURITY_PROOF_INVALID' ||
    code === 'SECURITY_PROOF_SCOPE_MISMATCH' ||
    code === 'SECURITY_PROOF_METHOD_MISMATCH'
  )
}

export function getSystemBackupErrorMessage(error: unknown): string | null {
  if (!axios.isAxiosError(error)) {
    return error instanceof Error ? error.message : null
  }
  const data: unknown = error.response?.data
  if (data && typeof data === 'object') {
    const message = (data as Record<string, unknown>).message
    if (typeof message === 'string' && message) return message
  }
  return typeof error.message === 'string' ? error.message : null
}
