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

import {
  relayActionLabel,
  relayReasonLabel,
  relayStageLabel,
} from '../relay-debug-labels'

const translate = ((key: string) => key) as Parameters<
  typeof relayActionLabel
>[1]

describe('relay debug labels', () => {
  test('maps retry tokens to readable admin labels', () => {
    assert.equal(
      relayStageLabel('overload_admission', translate),
      'Overload admission'
    )
    assert.equal(
      relayActionLabel('rotate_key', translate),
      'Rotate channel key'
    )
    assert.equal(
      relayReasonLabel('budget_exhausted', translate),
      'Retry budget exhausted'
    )
  })

  test('preserves unknown backend tokens for forward compatibility', () => {
    assert.equal(relayStageLabel('future_stage', translate), 'future_stage')
    assert.equal(relayActionLabel('future_action', translate), 'future_action')
    assert.equal(relayReasonLabel('future_reason', translate), 'future_reason')
  })
})
