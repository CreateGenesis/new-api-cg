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

export const RESPONSE_CONTENT_RETRY_MAX_RULES = 64
export const RESPONSE_CONTENT_RETRY_MAX_RULE_BYTES = 4 * 1024
export const RESPONSE_CONTENT_RETRY_MAX_TOTAL_BYTES = 64 * 1024

export const responseContentMatchModes = ['prefix', 'exact'] as const

export type ResponseContentMatchMode =
  (typeof responseContentMatchModes)[number]

export type ResponseContentRetryRule = {
  mode: ResponseContentMatchMode
  content: string
}

export type ResponseContentRetryPolicy = {
  enabled: boolean
  rules: ResponseContentRetryRule[]
}

export const DEFAULT_RESPONSE_CONTENT_RETRY_POLICY: ResponseContentRetryPolicy =
  {
    enabled: true,
    rules: [
      {
        mode: 'prefix',
        content:
          '抱歉，系统检测到您当前输入的信息存在敏感内容，我无法响应您的请求，请检查后重输入',
      },
      { mode: 'prefix', content: '[内容已过滤]' },
    ],
  }

export const DEFAULT_RESPONSE_CONTENT_RETRY_POLICY_JSON = JSON.stringify(
  DEFAULT_RESPONSE_CONTENT_RETRY_POLICY
)

const textEncoder = new TextEncoder()

function cloneDefaultPolicy(): ResponseContentRetryPolicy {
  return {
    enabled: DEFAULT_RESPONSE_CONTENT_RETRY_POLICY.enabled,
    rules: DEFAULT_RESPONSE_CONTENT_RETRY_POLICY.rules.map((rule) => ({
      ...rule,
    })),
  }
}

function isResponseContentMatchMode(
  value: unknown
): value is ResponseContentMatchMode {
  return value === 'prefix' || value === 'exact'
}

export function parseResponseContentRetryPolicy(
  value: string
): ResponseContentRetryPolicy {
  try {
    const parsed: unknown = JSON.parse(value)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return cloneDefaultPolicy()
    }

    const candidate = parsed as Record<string, unknown>
    const enabled = candidate.enabled ?? false
    const rawRules = candidate.rules ?? []
    if (
      typeof enabled !== 'boolean' ||
      !Array.isArray(rawRules) ||
      rawRules.length > RESPONSE_CONTENT_RETRY_MAX_RULES
    ) {
      return cloneDefaultPolicy()
    }

    const rules: ResponseContentRetryRule[] = []
    const seen = new Set<string>()
    let totalBytes = 0

    for (const rawRule of rawRules) {
      if (!rawRule || typeof rawRule !== 'object' || Array.isArray(rawRule)) {
        return cloneDefaultPolicy()
      }
      const rule = rawRule as Record<string, unknown>
      if (
        !isResponseContentMatchMode(rule.mode) ||
        typeof rule.content !== 'string'
      ) {
        return cloneDefaultPolicy()
      }

      const content = rule.content.trim()
      const contentBytes = textEncoder.encode(content).byteLength
      const duplicateKey = `${rule.mode}\0${content}`
      totalBytes += contentBytes
      if (
        contentBytes === 0 ||
        contentBytes > RESPONSE_CONTENT_RETRY_MAX_RULE_BYTES ||
        totalBytes > RESPONSE_CONTENT_RETRY_MAX_TOTAL_BYTES ||
        seen.has(duplicateKey)
      ) {
        return cloneDefaultPolicy()
      }

      seen.add(duplicateKey)
      rules.push({ mode: rule.mode, content })
    }

    return { enabled, rules }
  } catch {
    return cloneDefaultPolicy()
  }
}

export function serializeResponseContentRetryPolicy(
  policy: ResponseContentRetryPolicy
): string {
  return JSON.stringify({
    enabled: policy.enabled,
    rules: policy.rules.map((rule) => ({
      mode: rule.mode,
      content: rule.content.trim(),
    })),
  })
}
