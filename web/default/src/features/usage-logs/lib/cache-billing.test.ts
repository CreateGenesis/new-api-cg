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
import { describe, test } from 'node:test'

import { getCacheBillingUsage } from './cache-billing'

describe('cache billing usage', () => {
  test('does not treat a default creation ratio as used creation billing', () => {
    assert.deepEqual(
      getCacheBillingUsage({
        cache_tokens: 100,
        cache_ratio: 0.1,
        cache_creation_tokens: 0,
        cache_creation_ratio: 1.25,
      }),
      {
        hasAny: true,
        read: true,
        creation: false,
        creation5m: false,
        creation1h: false,
      }
    )
  })

  test('shows only the cache creation durations with actual token usage', () => {
    assert.deepEqual(
      getCacheBillingUsage({
        cache_creation_tokens: 80,
        cache_creation_tokens_5m: 30,
        cache_creation_tokens_1h: 50,
      }),
      {
        hasAny: true,
        read: false,
        creation: false,
        creation5m: true,
        creation1h: true,
      }
    )
  })

  test('shows legacy cache creation when it has actual token usage', () => {
    assert.deepEqual(
      getCacheBillingUsage({
        cache_creation_tokens: 20,
        cache_creation_ratio: 1.25,
      }),
      {
        hasAny: true,
        read: false,
        creation: true,
        creation5m: false,
        creation1h: false,
      }
    )
  })
})
