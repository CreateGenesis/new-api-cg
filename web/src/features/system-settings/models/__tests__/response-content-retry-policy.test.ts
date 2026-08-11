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
  DEFAULT_RESPONSE_CONTENT_RETRY_POLICY,
  parseResponseContentRetryPolicy,
  serializeResponseContentRetryPolicy,
} from '../response-content-retry-policy'

describe('response content retry policy parsing', () => {
  test('parses valid rules and trims rule content', () => {
    const policy = parseResponseContentRetryPolicy(
      JSON.stringify({
        enabled: false,
        rules: [
          { mode: 'prefix', content: '  blocked  ' },
          { mode: 'exact', content: 'filtered' },
        ],
      })
    )

    assert.deepEqual(policy, {
      enabled: false,
      rules: [
        { mode: 'prefix', content: 'blocked' },
        { mode: 'exact', content: 'filtered' },
      ],
    })
  })

  test('falls back to defaults for malformed JSON', () => {
    assert.deepEqual(
      parseResponseContentRetryPolicy('{'),
      DEFAULT_RESPONSE_CONTENT_RETRY_POLICY
    )
  })

  test('mirrors backend zero values when optional object fields are absent', () => {
    assert.deepEqual(parseResponseContentRetryPolicy('{}'), {
      enabled: false,
      rules: [],
    })
  })

  test('falls back to defaults for empty, duplicate, or invalid-mode rules', () => {
    const invalidPolicies = [
      { enabled: true, rules: [{ mode: 'prefix', content: '  ' }] },
      {
        enabled: true,
        rules: [
          { mode: 'prefix', content: 'blocked' },
          { mode: 'prefix', content: ' blocked ' },
        ],
      },
      { enabled: true, rules: [{ mode: 'contains', content: 'blocked' }] },
    ]

    for (const policy of invalidPolicies) {
      assert.deepEqual(
        parseResponseContentRetryPolicy(JSON.stringify(policy)),
        DEFAULT_RESPONSE_CONTENT_RETRY_POLICY
      )
    }
  })
})

describe('response content retry policy serialization', () => {
  test('writes normalized compact JSON for the atomic option value', () => {
    assert.equal(
      serializeResponseContentRetryPolicy({
        enabled: true,
        rules: [{ mode: 'exact', content: '  blocked  ' }],
      }),
      '{"enabled":true,"rules":[{"mode":"exact","content":"blocked"}]}'
    )
  })
})
