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
  MATCH_GT,
  SOURCE_MULTIPART,
  buildRequestRuleExpr,
  combineBillingExpr,
  splitBillingExprAndRequestRules,
  tryParseRequestRuleExpr,
} from '../billing-expr'

describe('billing expression request rules', () => {
  test('round trips an explicit multipart price override', () => {
    const requestRuleExpr = buildRequestRuleExpr([
      {
        conditions: [
          {
            source: SOURCE_MULTIPART,
            path: 'files.input_reference.#',
            mode: MATCH_GT,
            value: '0',
          },
        ],
        multiplier: '',
        action: 'override',
        override: {
          label: 'video_edit',
          input_unit_cost: 2,
          output_unit_cost: 20,
          cache_read_unit_cost: 0.5,
        },
      },
    ])
    const fullExpr = combineBillingExpr(
      'tier("base", p * 2 + c * 8)',
      requestRuleExpr
    )

    assert.match(fullExpr, /^rule_override_tier\(/)
    assert.match(fullExpr, /p_total/)
    assert.match(fullExpr, /cr_total/)
    const split = splitBillingExprAndRequestRules(fullExpr)
    assert.equal(split.billingExpr, 'tier("base", p * 2 + c * 8)')
    const parsed = tryParseRequestRuleExpr(split.requestRuleExpr)
    assert.ok(parsed)
    assert.equal(parsed[0].action, 'override')
    assert.equal(parsed[0].conditions[0].source, SOURCE_MULTIPART)
    assert.equal(parsed[0].override?.output_unit_cost, 20)
  })

  test('keeps multiplier rules in the legacy representation', () => {
    const rules = buildRequestRuleExpr([
      {
        conditions: [
          {
            source: SOURCE_MULTIPART,
            path: 'files.input_reference.#',
            mode: MATCH_GT,
            value: '0',
          },
        ],
        multiplier: '2',
      },
    ])
    assert.equal(
      rules,
      '(multipart_param("files.input_reference.#") != nil && multipart_param("files.input_reference.#") > 0 ? 2 : 1)'
    )
  })

  test('keeps direct overrides outside multiplier evaluation', () => {
    const multiplier = buildRequestRuleExpr([
      {
        conditions: [
          {
            source: SOURCE_MULTIPART,
            path: 'fields.quality.0',
            mode: 'eq',
            value: 'high',
          },
        ],
        multiplier: '2',
      },
    ])
    const override = buildRequestRuleExpr([
      {
        conditions: [
          {
            source: SOURCE_MULTIPART,
            path: 'files.input_reference.#',
            mode: MATCH_GT,
            value: '0',
          },
        ],
        multiplier: '',
        action: 'override',
        override: {
          label: 'video_edit',
          input_unit_cost: 2,
          output_unit_cost: 20,
        },
      },
    ])
    const fullExpr = combineBillingExpr(
      'tier("base", p * 2 + c * 8)',
      `${multiplier} * ${override}`
    )
    assert.match(fullExpr, /^rule_override_tier\(\(tier\("base"/)
    const split = splitBillingExprAndRequestRules(fullExpr)
    assert.equal(split.billingExpr, 'tier("base", p * 2 + c * 8)')
    const parsed = tryParseRequestRuleExpr(split.requestRuleExpr)
    assert.ok(parsed)
    assert.equal(parsed.length, 2)
    assert.equal(parsed[0].action, 'override')
    assert.equal(parsed[1].action, 'multiplier')
  })
})
