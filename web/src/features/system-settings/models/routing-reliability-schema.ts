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
import type { TFunction } from 'i18next'
import * as z from 'zod'

import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

import {
  RESPONSE_CONTENT_RETRY_MAX_RULE_BYTES,
  RESPONSE_CONTENT_RETRY_MAX_RULES,
  RESPONSE_CONTENT_RETRY_MAX_TOTAL_BYTES,
  responseContentMatchModes,
} from './response-content-retry-policy'

export const channelTestModes = ['scheduled_all', 'passive_recovery'] as const
export type ChannelTestMode = (typeof channelTestModes)[number]

const textEncoder = new TextEncoder()

export function createRoutingReliabilitySchema(t: TFunction) {
  const numericString = z.string().refine((value) => {
    const trimmed = value.trim()
    if (!trimmed) return true
    return !Number.isNaN(Number(trimmed)) && Number(trimmed) >= 0
  }, t('Enter a non-negative number or leave empty'))

  const responseContentRetryRuleSchema = z.object({
    mode: z.enum(responseContentMatchModes),
    content: z
      .string()
      .transform((value) => value.trim())
      .pipe(
        z
          .string()
          .min(1, t('Rule content cannot be empty'))
          .refine(
            (value) =>
              textEncoder.encode(value).byteLength <=
              RESPONSE_CONTENT_RETRY_MAX_RULE_BYTES,
            t('Rule content cannot exceed 4096 bytes')
          )
      ),
  })

  return z
    .object({
      RetryTimes: z.coerce.number().min(0).max(10),
      ChannelDisableThreshold: numericString,
      AutomaticDisableChannelEnabled: z.boolean(),
      AutomaticEnableChannelEnabled: z.boolean(),
      AutomaticDisableKeywords: z.string(),
      AutomaticDisableStatusCodes: z.string(),
      AutomaticRetryStatusCodes: z.string(),
      monitor_setting: z.object({
        auto_test_channel_enabled: z.boolean(),
        auto_test_channel_minutes: z.coerce
          .number()
          .int()
          .min(1, t('Interval must be at least 1 minute')),
        channel_test_mode: z.enum(channelTestModes),
      }),
      response_content_retry_policy: z.object({
        enabled: z.boolean(),
        rules: z
          .array(responseContentRetryRuleSchema)
          .max(
            RESPONSE_CONTENT_RETRY_MAX_RULES,
            t('No more than 64 response matching rules are allowed')
          ),
      }),
    })
    .superRefine((values, ctx) => {
      const disableParsed = parseHttpStatusCodeRules(
        values.AutomaticDisableStatusCodes
      )
      if (!disableParsed.ok) {
        ctx.addIssue({
          code: 'custom',
          path: ['AutomaticDisableStatusCodes'],
          message: t('Invalid status code rules: {{tokens}}', {
            tokens: disableParsed.invalidTokens.join(', '),
          }),
        })
      }

      const retryParsed = parseHttpStatusCodeRules(
        values.AutomaticRetryStatusCodes
      )
      if (!retryParsed.ok) {
        ctx.addIssue({
          code: 'custom',
          path: ['AutomaticRetryStatusCodes'],
          message: t('Invalid status code rules: {{tokens}}', {
            tokens: retryParsed.invalidTokens.join(', '),
          }),
        })
      }

      const seenRules = new Set<string>()
      let totalBytes = 0
      values.response_content_retry_policy.rules.forEach((rule, index) => {
        totalBytes += textEncoder.encode(rule.content).byteLength
        const duplicateKey = `${rule.mode}\0${rule.content}`
        if (seenRules.has(duplicateKey)) {
          ctx.addIssue({
            code: 'custom',
            path: ['response_content_retry_policy', 'rules', index, 'content'],
            message: t('Duplicate response matching rule'),
          })
        }
        seenRules.add(duplicateKey)
      })

      if (totalBytes > RESPONSE_CONTENT_RETRY_MAX_TOTAL_BYTES) {
        ctx.addIssue({
          code: 'custom',
          path: ['response_content_retry_policy', 'rules'],
          message: t('Rule content cannot exceed 65536 bytes in total'),
        })
      }
    })
}

export type RoutingReliabilitySchema = ReturnType<
  typeof createRoutingReliabilitySchema
>
export type RoutingReliabilityFormValues = z.output<RoutingReliabilitySchema>
export type RoutingReliabilityFormInput = z.input<RoutingReliabilitySchema>
