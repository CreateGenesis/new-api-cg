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

import type { UsageNormalizationAudit } from '../../types'
import {
  getUsageAccountingModeLabelKey,
  getUsageNormalizationSourceLabelKey,
  getUsageNormalizationStatusLabelKey,
  isUsageNormalizationWarning,
} from '../usage-normalization'

const matchedAudit: UsageNormalizationAudit = {
  mode: 'included',
  source: 'billing_usage',
  status: 'matched',
  reported_input_tokens: 100,
  reported_output_tokens: 20,
  reported_total_tokens: 120,
  cache_read_input_tokens: 30,
  cache_creation_input_tokens: 0,
  normalized_uncached_input_tokens: 70,
  normalized_total_input_tokens: 100,
}

describe('usage normalization labels', () => {
  test('maps included and separate accounting modes', () => {
    assert.equal(
      getUsageAccountingModeLabelKey('included'),
      'Input includes cache'
    )
    assert.equal(
      getUsageAccountingModeLabelKey('separate'),
      'Cache reported separately'
    )
  })

  test('maps metadata, reconciliation, and fallback sources', () => {
    assert.equal(
      getUsageNormalizationSourceLabelKey('billing_usage'),
      'Billing usage metadata'
    )
    assert.equal(
      getUsageNormalizationSourceLabelKey('total_tokens'),
      'Total token reconciliation'
    )
    assert.equal(getUsageNormalizationSourceLabelKey('fallback'), 'Fallback')
  })

  test('marks fallback and mismatch audits as warnings', () => {
    assert.equal(isUsageNormalizationWarning(matchedAudit), false)
    assert.equal(
      isUsageNormalizationWarning({ ...matchedAudit, source: 'fallback' }),
      true
    )
    assert.equal(
      isUsageNormalizationWarning({ ...matchedAudit, status: 'mismatch' }),
      true
    )
    assert.equal(
      getUsageNormalizationStatusLabelKey('not_checked'),
      'Not checked'
    )
    assert.equal(getUsageNormalizationStatusLabelKey('mismatch'), 'Mismatch')
  })
})
