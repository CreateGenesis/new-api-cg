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
import assert from 'node:assert/strict'
import { constants, generateKeyPairSync, privateDecrypt } from 'node:crypto'

import { afterEach, describe, expect, test, vi } from 'vitest'

import { clearPasswordEncryptionCache } from '@/features/auth/lib/password-encryption'
import { api } from '@/lib/api'

import { verifySystemBackupPassword } from '../../api'
import type { SystemBackupProof } from '../../types'
import {
  getSystemBackupErrorMessage,
  isSystemBackupProofError,
  isSystemBackupProofUsable,
  validateSystemBackupText,
} from '../system-backup-utils'

const importProof: SystemBackupProof = {
  proof_token: 'proof-token',
  expires_at: 1_300,
  method: 'password',
  scope: 'system.config.full.import',
}

describe('full system backup file validation', () => {
  test('accepts the version 1 sensitive replacement format', () => {
    const content = JSON.stringify({
      schema: 'new-api.system-backup',
      version: 1,
      sensitive: true,
      restore_mode: 'replace',
    })

    assert.equal(validateSystemBackupText(content), null)
  })

  test('distinguishes malformed JSON from an incompatible backup format', () => {
    assert.equal(validateSystemBackupText('{'), 'invalid_json')
    assert.equal(
      validateSystemBackupText(
        JSON.stringify({
          schema: 'new-api.system-config',
          version: 1,
          sensitive: false,
          restore_mode: 'merge',
        })
      ),
      'invalid_schema'
    )
  })
})

describe('full system backup password proof lifetime', () => {
  test('accepts a matching password proof with more than five seconds remaining', () => {
    assert.equal(
      isSystemBackupProofUsable(
        importProof,
        'system.config.full.import',
        1_000
      ),
      true
    )
  })

  test('rejects an expired, nearly expired, or wrong-scope proof', () => {
    assert.equal(
      isSystemBackupProofUsable(
        importProof,
        'system.config.full.import',
        1_295
      ),
      false
    )
    assert.equal(
      isSystemBackupProofUsable(
        importProof,
        'system.config.full.export',
        1_000
      ),
      false
    )
  })
})

describe('full system backup API errors', () => {
  test('recognizes the security proof error contract', () => {
    const error = {
      isAxiosError: true,
      response: {
        data: {
          code: 'SECURITY_PROOF_EXPIRED',
          message: 'proof expired',
        },
      },
    }

    assert.equal(isSystemBackupProofError(error), true)
    assert.equal(getSystemBackupErrorMessage(error), 'proof expired')
  })

  test('does not treat an unrelated API error as a proof error', () => {
    const error = {
      isAxiosError: true,
      response: {
        data: { code: 'VALIDATION_ERROR', message: 'invalid backup' },
      },
    }

    assert.equal(isSystemBackupProofError(error), false)
    assert.equal(getSystemBackupErrorMessage(error), 'invalid backup')
  })
})

describe('full backup password verification transport', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    clearPasswordEncryptionCache()
  })

  test('uses encrypted credentials when required by the server', async () => {
    const { publicKey, privateKey } = generateKeyPairSync('rsa', {
      modulusLength: 2048,
    })
    vi.spyOn(api, 'get').mockImplementation(async (url) => ({
      data: {
        success: true,
        data:
          url === '/api/verify/methods'
            ? { password_encryption_enabled: true }
            : {
                kid: 'backup-key',
                public_key: publicKey.export({ type: 'spki', format: 'pem' }),
              },
      },
    }))
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: importProof } })
    await verifySystemBackupPassword({
      username: 'root',
      password: 'backup-password',
      scope: importProof.scope,
    })
    const payload = post.mock.calls[0][1] as Record<string, string>
    expect(payload).not.toHaveProperty('password')
    expect(payload).toMatchObject({
      username: 'root',
      method: 'password',
      scope: importProof.scope,
      encryption_key_id: 'backup-key',
    })
    const decrypted = privateDecrypt(
      {
        key: privateKey,
        padding: constants.RSA_PKCS1_OAEP_PADDING,
        oaepHash: 'sha256',
      },
      Buffer.from(payload.password_encrypted, 'base64')
    )
    expect(decrypted.toString()).toBe('backup-password')
  })

  test('does not submit credentials when verification requirements cannot be loaded', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({ data: { success: false } })
    const post = vi.spyOn(api, 'post').mockResolvedValue({})
    await expect(
      verifySystemBackupPassword({
        username: 'root',
        password: 'backup-password',
        scope: importProof.scope,
      })
    ).rejects.toThrow()
    expect(post).not.toHaveBeenCalled()
  })
})
