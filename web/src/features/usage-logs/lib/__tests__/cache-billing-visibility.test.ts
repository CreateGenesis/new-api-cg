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

import { shouldShowCacheBillingPrices } from '../cache-billing'

const usageNormalization = {
  mode: 'included' as const,
  source: 'billing_usage' as const,
  status: 'matched' as const,
  reported_input_tokens: 100,
  reported_output_tokens: 20,
  reported_total_tokens: 120,
  cache_read_input_tokens: 30,
  cache_creation_input_tokens: 0,
  normalized_uncached_input_tokens: 70,
  normalized_total_input_tokens: 100,
}

describe('cache billing price visibility', () => {
  test('shows cache prices for a channel using cache validation split', () => {
    assert.equal(
      shouldShowCacheBillingPrices({
        cache_tokens: 100,
        cache_ratio: 0.1,
        admin_info: { usage_normalization: usageNormalization },
      }),
      true
    )
  })

  test('keeps legacy non-Claude cache prices hidden when the switch is off', () => {
    assert.equal(
      shouldShowCacheBillingPrices({ cache_tokens: 100, cache_ratio: 0.1 }),
      false
    )
  })

  test('keeps legacy Claude cache prices visible when the switch is off', () => {
    assert.equal(
      shouldShowCacheBillingPrices({
        claude: true,
        cache_tokens: 100,
        cache_ratio: 0.1,
      }),
      true
    )
  })

  test('hides ratio prices when tiered billing owns the breakdown', () => {
    assert.equal(
      shouldShowCacheBillingPrices({
        billing_mode: 'tiered_expr',
        cache_tokens: 100,
        cache_ratio: 0.1,
      }),
      false
    )
  })
})
