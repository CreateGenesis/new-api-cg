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
/**
 * Billing expression parsing utilities.
 *
 * Parses the dynamic billing expression format so that the pricing breakdown
 * UI can be rendered from the same backend expressions.
 *
 * Display adapters intentionally accept fewer shapes than the shared
 * simulator. Existing ordered-tier, task-unit and request-rule contracts
 * stay intact; executable custom expressions do not imply fixed unit prices.
 */

import type { BillingUsageSchema } from '../types'
import {
  readTokenTierChain,
  readTaskTierChain,
  readTimeTokenPricing,
  type TokenTier,
} from './billing-expression/display'
import { compileBillingExpression } from './billing-expression/parser'
import {
  splitExpressionAtTopLevel,
  unwrapExpressionParens,
} from './billing-expression/structure'

// ---------------------------------------------------------------------------
// Variable registry
// ---------------------------------------------------------------------------

export type BillingVar = {
  key: string
  field: string | null
  tierField: string | null
  label: string
  shortLabel: string
  side: 'input' | 'output' | 'condition'
  isBase?: boolean
  isConditionOnly?: boolean
  group?: string
}

export const BILLING_VARS: BillingVar[] = [
  {
    key: 'p',
    field: 'inputPrice',
    tierField: 'input_unit_cost',
    label: 'Input price',
    shortLabel: 'Input',
    side: 'input',
    isBase: true,
  },
  {
    key: 'c',
    field: 'outputPrice',
    tierField: 'output_unit_cost',
    label: 'Completion price',
    shortLabel: 'Output',
    side: 'output',
    isBase: true,
  },
  {
    key: 'len',
    field: null,
    tierField: null,
    label: 'Input length',
    shortLabel: 'Length',
    side: 'condition',
    isConditionOnly: true,
  },
  {
    key: 'cr',
    field: 'cacheReadPrice',
    tierField: 'cache_read_unit_cost',
    label: 'Cache read price',
    shortLabel: 'Cache Read',
    side: 'input',
    group: 'cache',
  },
  {
    key: 'cc',
    field: 'cacheCreatePrice',
    tierField: 'cache_create_unit_cost',
    label: 'Cache create price',
    shortLabel: 'Cache Write',
    side: 'input',
    group: 'cache',
  },
  {
    key: 'img_cr',
    field: 'imageCachePrice',
    tierField: 'image_cache_unit_cost',
    label: 'Image cache input price',
    shortLabel: 'Image Cache',
    side: 'input',
    group: 'cache',
  },
  {
    key: 'cc1h',
    field: 'cacheCreate1hPrice',
    tierField: 'cache_create_1h_unit_cost',
    label: 'Cache create (1h) price',
    shortLabel: 'Cache Write (1h)',
    side: 'input',
    group: 'cache',
  },
  {
    key: 'img',
    field: 'imagePrice',
    tierField: 'image_unit_cost',
    label: 'Image input price',
    shortLabel: 'Image In',
    side: 'input',
    group: 'media',
  },
  {
    key: 'img_o',
    field: 'imageOutputPrice',
    tierField: 'image_output_unit_cost',
    label: 'Image output price',
    shortLabel: 'Image Out',
    side: 'output',
    group: 'media',
  },
  {
    key: 'ai',
    field: 'audioInputPrice',
    tierField: 'audio_input_unit_cost',
    label: 'Audio input price',
    shortLabel: 'Audio In',
    side: 'input',
    group: 'media',
  },
  {
    key: 'ao',
    field: 'audioOutputPrice',
    tierField: 'audio_output_unit_cost',
    label: 'Audio output price',
    shortLabel: 'Audio Out',
    side: 'output',
    group: 'media',
  },
]

/** Vars that have real price fields (excludes condition-only vars like `len`) */
export const BILLING_PRICING_VARS: BillingVar[] = BILLING_VARS.filter(
  (v) => !v.isConditionOnly
)

/** Vars valid in tier conditions (`p`, `c`, `len`) */
export const BILLING_CONDITION_VARS: string[] = BILLING_VARS.filter(
  (v) => v.isBase || v.isConditionOnly
).map((v) => v.key)

const BILLING_VAR_KEY_TO_FIELD = Object.fromEntries(
  BILLING_PRICING_VARS.map((v) => [v.key, v.field as string])
) as Record<string, string>

export const BILLING_EXTRA_VARS: BillingVar[] = BILLING_VARS.filter(
  (v) => !v.isBase && !v.isConditionOnly
)

export const BILLING_CACHE_VAR_MAP = BILLING_EXTRA_VARS.map((v) => ({
  field: v.tierField as string,
  exprVar: v.key,
}))

// ---------------------------------------------------------------------------
// Request rule constants
// ---------------------------------------------------------------------------

export const SOURCE_PARAM = 'param'
export const SOURCE_HEADER = 'header'
export const SOURCE_TIME = 'time'
export const SOURCE_MULTIPART = 'multipart'

export const MATCH_EQ = 'eq'
export const MATCH_CONTAINS = 'contains'
export const MATCH_GT = 'gt'
export const MATCH_GTE = 'gte'
export const MATCH_LT = 'lt'
export const MATCH_LTE = 'lte'
export const MATCH_EXISTS = 'exists'
export const MATCH_RANGE = 'range'

export const TIME_FUNCS = ['hour', 'minute', 'weekday', 'month', 'day'] as const
export type TimeFunc = (typeof TIME_FUNCS)[number]

export const COMMON_TIMEZONES: { value: string; label: string }[] = [
  { value: 'Asia/Shanghai', label: 'UTC+8 Shanghai (Asia/Shanghai)' },
  { value: 'UTC', label: 'UTC' },
  { value: 'America/New_York', label: 'UTC-5 New York (America/New_York)' },
  {
    value: 'America/Los_Angeles',
    label: 'UTC-8 Los Angeles (America/Los_Angeles)',
  },
  { value: 'America/Chicago', label: 'UTC-6 Chicago (America/Chicago)' },
  { value: 'Europe/London', label: 'UTC+0 London (Europe/London)' },
  { value: 'Europe/Berlin', label: 'UTC+1 Berlin (Europe/Berlin)' },
  { value: 'Asia/Tokyo', label: 'UTC+9 Tokyo (Asia/Tokyo)' },
  { value: 'Asia/Singapore', label: 'UTC+8 Singapore (Asia/Singapore)' },
  { value: 'Asia/Seoul', label: 'UTC+9 Seoul (Asia/Seoul)' },
  { value: 'Australia/Sydney', label: 'UTC+10 Sydney (Australia/Sydney)' },
]

const NUMERIC_LITERAL_REGEX = /^-?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/

export type ParamHeaderCondition = {
  source: 'param' | 'header' | 'multipart'
  path: string
  mode: string
  value: string
}

export type TimeCondition = {
  source: 'time'
  timeFunc: TimeFunc
  timezone: string
  mode: string
  value: string
  rangeStart: string
  rangeEnd: string
}

export type RequestCondition = TimeCondition | ParamHeaderCondition

export type RequestPriceOverride = {
  label?: string
  input_unit_cost?: number
  output_unit_cost?: number
  cache_read_unit_cost?: number
  cache_create_unit_cost?: number
  cache_create_1h_unit_cost?: number
  image_unit_cost?: number
  image_output_unit_cost?: number
  audio_input_unit_cost?: number
  audio_output_unit_cost?: number
}

export type RequestRuleGroup = {
  conditions: RequestCondition[]
  multiplier: string
  action?: 'multiplier' | 'override'
  override?: RequestPriceOverride
  conditionText?: string
  matched?: boolean
}

export type RequestRuleTrace = {
  cond: string
  multiplier: number
  matched: boolean
}

export type TierCondition = {
  var: 'p' | 'c' | 'len'
  op: '<' | '<=' | '>' | '>='
  value: number
}

export type ParsedTier = {
  billingUnit?: 'token' | 'request'
  fixedPrice?: number
  conditionText?: string
  label: string
  conditions: TierCondition[]
  [field: string]: unknown
}

export type TaskTierCondition = {
  field: string
  value: string
}

export type ParsedTaskTier = {
  conditionText?: string
  label: string
  conditions: TaskTierCondition[]
  constant: number
  unitPrices: Record<string, number>
}

// ---------------------------------------------------------------------------
// Tier parser
// ---------------------------------------------------------------------------

function mapTokenTier(
  tier: TokenTier & { conditionText?: string }
): ParsedTier {
  return {
    label: tier.label,
    ...(tier.imageCount ? { imageCount: true } : {}),
    conditions: tier.conditions,
    ...(tier.billingUnit === 'request'
      ? { billingUnit: tier.billingUnit, fixedPrice: tier.fixedPrice }
      : {}),
    ...(tier.conditionText ? { conditionText: tier.conditionText } : {}),
    ...Object.fromEntries(
      Object.entries(tier.prices).map(([key, price]) => [
        BILLING_VAR_KEY_TO_FIELD[key],
        price,
      ])
    ),
  }
}

export function parseTiersFromExpr(exprStr: string): ParsedTier[] {
  if (!exprStr) return []
  const compiled = compileBillingExpression(exprStr)
  if (compiled.status !== 'ready') return []
  const canonical = readTokenTierChain(compiled.ast)
  if (canonical) return canonical.map(mapTokenTier)
  return readTimeTokenPricing(exprStr)?.tiers.map(mapTokenTier) ?? []
}

/** Current-time selection is exclusively for summaries; detail and log callers retain all rows. */
export function getCurrentTimePricingTiers(
  exprStr: string,
  now: Date
): ParsedTier[] | null {
  return (
    readTimeTokenPricing(exprStr, now)?.currentTiers.map(mapTokenTier) ?? null
  )
}

export function parseTaskTiersFromExpr(
  exprStr: string,
  schema: BillingUsageSchema | null | undefined,
  includeBooleanConditions = false
): ParsedTaskTier[] {
  if (!exprStr || !schema || Object.keys(schema).length === 0) return []
  const { billingExpr } = splitBillingExprAndRequestRules(exprStr)
  const compiled = compileBillingExpression(billingExpr)
  if (compiled.status !== 'ready') return []
  return readTaskTierChain(compiled.ast, schema, includeBooleanConditions) ?? []
}

export function normalizeTierLabel(label: string | undefined): string {
  if (!label) return ''
  return label
    .replaceAll(/<[=＝]?|≤|＜[=＝]?/g, '<')
    .replaceAll(/>[=＝]?|≥|＞[=＝]?/g, '>')
    .replaceAll(/\s+/g, '')
    .toLowerCase()
}

// ---------------------------------------------------------------------------
// Request rule parser
// ---------------------------------------------------------------------------

function splitTopLevelMultiply(expr: string): string[] {
  return splitExpressionAtTopLevel(expr, '*')
}

function splitTopLevelAnd(expr: string): string[] {
  return splitExpressionAtTopLevel(expr, '&&')
}

function parseExprLiteral(raw: string): string | null {
  const text = raw.trim()
  if (text === 'true' || text === 'false') return text
  if (NUMERIC_LITERAL_REGEX.test(text)) return text
  try {
    return JSON.parse(text) as string
  } catch {
    return null
  }
}

// Time function value domains. Values outside these ranges are invalid for
// the corresponding time function (e.g. hour() is 0-23) and would otherwise
// produce always-true conditions like hour >= -1 || hour < -5.
const TIME_FUNC_RANGES: Record<TimeFunc, [number, number]> = {
  hour: [0, 23],
  minute: [0, 59],
  weekday: [0, 6],
  month: [1, 12],
  day: [1, 31],
}

function isTimeValueInRange(timeFunc: TimeFunc, text: string): boolean {
  if (!NUMERIC_LITERAL_REGEX.test(text)) return false
  const value = Number(text)
  if (!Number.isInteger(value)) return false
  const [min, max] = TIME_FUNC_RANGES[timeFunc]
  return value >= min && value <= max
}

function tryParseTimeCondition(expr: string): RequestCondition | null {
  let m = expr.match(
    /^(hour|minute|weekday|month|day)\("([^"]+)"\) >= ([\d.eE+-]+) (?:&&|\|\|) \1\("\2"\) < ([\d.eE+-]+)$/
  )
  if (!m) {
    m = expr.match(
      /^\((hour|minute|weekday|month|day)\("([^"]+)"\) >= ([\d.eE+-]+) (?:&&|\|\|) \1\("\2"\) < ([\d.eE+-]+)\)$/
    )
  }
  if (m) {
    // Reject invalid bounds at parse time too: an unparseable rule keeps the
    // editor in raw mode, while a leniently parsed one would be silently
    // dropped when the visual editor rebuilds the expression.
    if (
      !isTimeValueInRange(m[1] as TimeFunc, m[3]) ||
      !isTimeValueInRange(m[1] as TimeFunc, m[4])
    ) {
      return null
    }
    return {
      source: 'time',
      timeFunc: m[1] as TimeFunc,
      timezone: m[2],
      mode: MATCH_RANGE,
      value: '',
      rangeStart: m[3],
      rangeEnd: m[4],
    }
  }
  m = expr.match(
    /^(hour|minute|weekday|month|day)\("([^"]+)"\) (==|>=|<) ([\d.eE+-]+)$/
  )
  if (m) {
    if (!isTimeValueInRange(m[1] as TimeFunc, m[4])) return null
    const opMap: Record<string, string> = {
      '==': MATCH_EQ,
      '>=': MATCH_GTE,
      '<': MATCH_LT,
    }
    return {
      source: 'time',
      timeFunc: m[1] as TimeFunc,
      timezone: m[2],
      mode: opMap[m[3]] || MATCH_EQ,
      value: m[4],
      rangeStart: '',
      rangeEnd: '',
    }
  }
  return null
}

function tryParseRequestCondition(expr: string): RequestCondition | null {
  const tc = tryParseTimeCondition(expr)
  if (tc) return tc

  let m = expr.match(/^(header|multipart_param)\("([^"]+)"\) != ""$/)
  if (m) {
    return {
      source: m[1] === 'multipart_param' ? SOURCE_MULTIPART : 'header',
      path: m[2],
      mode: MATCH_EXISTS,
      value: '',
    }
  }

  m = expr.match(/^(param|multipart_param)\("([^"]+)"\) != nil$/)
  if (m) {
    return {
      source: m[1] === 'multipart_param' ? SOURCE_MULTIPART : 'param',
      path: m[2],
      mode: MATCH_EXISTS,
      value: '',
    }
  }

  m = expr.match(
    /^has\((header|multipart_param)\("([^"]+)"\), ((?:"(?:[^"\\]|\\.)*"))\)$/
  )
  if (m) {
    return {
      source: m[1] === 'multipart_param' ? SOURCE_MULTIPART : 'header',
      path: m[2],
      mode: MATCH_CONTAINS,
      value: JSON.parse(m[3]) as string,
    }
  }

  m = expr.match(
    /^(param|multipart_param)\("([^"]+)"\) != nil && has\((param|multipart_param)\("([^"]+)"\), ((?:"(?:[^"\\]|\\.)*"))\)$/
  )
  if (m && m[1] === m[3] && m[2] === m[4]) {
    return {
      source: m[1] === 'multipart_param' ? SOURCE_MULTIPART : 'param',
      path: m[2],
      mode: MATCH_CONTAINS,
      value: JSON.parse(m[5]) as string,
    }
  }

  m = expr.match(
    /^(param|multipart_param)\("([^"]+)"\) != nil && \1\("([^"]+)"\) (>|>=|<|<=) ([\d.eE+-]+)$/
  )
  if (m && m[2] === m[3]) {
    const opMap: Record<string, string> = {
      '>': MATCH_GT,
      '>=': MATCH_GTE,
      '<': MATCH_LT,
      '<=': MATCH_LTE,
    }
    return {
      source: m[1] === 'multipart_param' ? SOURCE_MULTIPART : 'param',
      path: m[2],
      mode: opMap[m[4]],
      value: m[5],
    }
  }

  m = expr.match(/^(param|header|multipart_param)\("([^"]+)"\) == (.+)$/)
  if (m) {
    const parsedValue = parseExprLiteral(m[3])
    if (parsedValue === null) return null
    return {
      source:
        m[1] === 'multipart_param'
          ? SOURCE_MULTIPART
          : (m[1] as 'param' | 'header'),
      path: m[2],
      mode: MATCH_EQ,
      value: String(parsedValue),
    }
  }

  return null
}

function splitTopLevelArgs(expr: string): string[] {
  const parts: string[] = []
  let start = 0
  let depth = 0
  let quote = ''
  let escaped = false
  for (let index = 0; index < expr.length; index += 1) {
    const char = expr[index]
    if (quote) {
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === quote) quote = ''
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      continue
    }
    if (char === '(') depth += 1
    else if (char === ')') depth -= 1
    else if (char === ',' && depth === 0) {
      parts.push(expr.slice(start, index).trim())
      start = index + 1
    }
  }
  parts.push(expr.slice(start).trim())
  return parts
}

function parseOverrideCost(expr: string): RequestPriceOverride | null {
  const trimmed = expr.trim()
  if (!trimmed.startsWith('tier(') || !trimmed.endsWith(')')) return null
  const args = splitTopLevelArgs(trimmed.slice('tier('.length, -1))
  if (args.length !== 2) return null
  let label: string
  try {
    label = JSON.parse(args[0]) as string
  } catch {
    return null
  }
  if (typeof label !== 'string') return null
  const prices: RequestPriceOverride = { label }
  const fields: Record<string, keyof RequestPriceOverride> = {
    p_total: 'input_unit_cost',
    c_total: 'output_unit_cost',
    cr_total: 'cache_read_unit_cost',
    cc_total: 'cache_create_unit_cost',
    cc1h_total: 'cache_create_1h_unit_cost',
    img_total: 'image_unit_cost',
    img_o_total: 'image_output_unit_cost',
    ai_total: 'audio_input_unit_cost',
    ao_total: 'audio_output_unit_cost',
    // Accept the pre-existing spelling for manually-authored expressions.
    p: 'input_unit_cost',
    c: 'output_unit_cost',
    cr: 'cache_read_unit_cost',
    cc: 'cache_create_unit_cost',
    cc1h: 'cache_create_1h_unit_cost',
    img: 'image_unit_cost',
    img_o: 'image_output_unit_cost',
    ai: 'audio_input_unit_cost',
    ao: 'audio_output_unit_cost',
  }
  const baseTermRe =
    /\(\s*(p_total|c_total)(?:\s*-\s*(?:cr_total|cc_total|cc1h_total|img_total|ai_total|img_o_total|ao_total))*\s*\)\s*\*\s*(-?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)/g
  let found = false
  let baseTerm: RegExpExecArray | null
  while ((baseTerm = baseTermRe.exec(args[1])) !== null) {
    const value = Number(baseTerm[2])
    if (!Number.isFinite(value)) return null
    Object.assign(prices, { [fields[baseTerm[1]]]: value })
    found = true
  }
  const termRe =
    /\b(p_total|c_total|cr_total|cc_total|cc1h_total|img_total|img_o_total|ai_total|ao_total|p|c|cr|cc|cc1h|img|img_o|ai|ao)\s*\*\s*(-?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)/g
  let term: RegExpExecArray | null
  while ((term = termRe.exec(args[1])) !== null) {
    const value = Number(term[2])
    if (!Number.isFinite(value)) return null
    Object.assign(prices, { [fields[term[1]]]: value })
    found = true
  }
  return found ? prices : null
}

function parseOverrideRuleFactor(part: string): RequestRuleGroup | null {
  const match = part.match(/^override_rule\(([\s\S]+)\)$/)
  if (!match) return null
  const args = splitTopLevelArgs(match[1])
  if (args.length !== 2) return null
  const override = parseOverrideCost(args[1])
  if (!override) return null
  const conditions = splitTopLevelAnd(args[0])
    .map((part) => tryParseRequestCondition(part.trim()))
    .filter((condition): condition is RequestCondition => condition !== null)
  if (conditions.length === 0) return null
  return { conditions, multiplier: '', action: 'override', override }
}

function tryParseTimeRangePair(
  lower: string,
  upper: string
): RequestCondition | null {
  const a = tryParseTimeCondition(lower)
  const b = tryParseTimeCondition(upper)
  if (!a || !b || a.source !== 'time' || b.source !== 'time') return null
  const ta = a as TimeCondition
  const tb = b as TimeCondition
  if (ta.timeFunc !== tb.timeFunc || ta.timezone !== tb.timezone) return null
  if (ta.mode !== MATCH_GTE || tb.mode !== MATCH_LT) return null
  return {
    source: 'time',
    timeFunc: ta.timeFunc,
    timezone: ta.timezone,
    mode: MATCH_RANGE,
    value: '',
    rangeStart: ta.value,
    rangeEnd: tb.value,
  }
}

function tryParseRequestConditions(
  conditionStr: string
): RequestCondition[] | null {
  // A single time range like hour(tz) >= 9 && hour(tz) < 12 must stay one
  // MATCH_RANGE condition instead of being split into two scalar conditions.
  const wholeTimeCond = tryParseTimeCondition(conditionStr.trim())
  if (wholeTimeCond) return [wholeTimeCond]

  const andParts = splitTopLevelAnd(conditionStr)
  const conditions: RequestCondition[] = []
  for (let i = 0; i < andParts.length; i += 1) {
    const part = andParts[i].trim()
    // Adjacent matching time bounds (fn >= X && fn < Y) form one range; merge
    // them so the visual editor keeps a single MATCH_RANGE row even when
    // other conditions follow in the same group.
    const next = i + 1 < andParts.length ? andParts[i + 1].trim() : ''
    const merged = next ? tryParseTimeRangePair(part, next) : null
    if (merged) {
      conditions.push(merged)
      i += 1
      continue
    }
    const condition = tryParseRequestCondition(part)
    if (!condition) return null
    conditions.push(condition)
  }
  return conditions.length > 0 ? conditions : null
}

function tryParseRuleGroupFactor(part: string): RequestRuleGroup | null {
  const override = parseOverrideRuleFactor(part)
  if (override) return override
  const m = part.match(/^\((.+) \? ([\d.eE+-]+) : 1\)$/s)
  if (!m) return null

  const conditions = tryParseRequestConditions(m[1])
  if (!conditions) return null
  return { conditions, action: 'multiplier', multiplier: m[2] }
}

export function requestRuleGroupsFromTrace(
  requestRules: RequestRuleTrace[]
): RequestRuleGroup[] {
  return requestRules.map((rule) => {
    const conditionText = rule.cond.trim()
    return {
      conditions: tryParseRequestConditions(conditionText) || [],
      multiplier: String(rule.multiplier),
      conditionText,
      matched: rule.matched,
    }
  })
}

export function tryParseRequestRuleExpr(
  expr: string
): RequestRuleGroup[] | null {
  const trimmed = (expr || '').trim()
  if (!trimmed) return []

  const parts = splitTopLevelMultiply(trimmed)
  const groups: RequestRuleGroup[] = []
  for (const part of parts) {
    const group = tryParseRuleGroupFactor(part)
    if (!group) return null
    groups.push(group)
  }
  return groups
}

// ---------------------------------------------------------------------------
// Combine / split billing expr and request rules
// ---------------------------------------------------------------------------

function unwrapOuterParens(expr: string): string {
  return unwrapExpressionParens(expr)
}

function parseRuntimeOverrideExpr(expr: string): {
  baseExpr: string
  rules: string[]
} | null {
  const trimmed = unwrapOuterParens(expr.trim())
  let functionName = ''
  if (trimmed.startsWith('rule_override_tier(')) {
    functionName = 'rule_override_tier'
  } else if (trimmed.startsWith('rule_override(')) {
    functionName = 'rule_override'
  }
  if (!functionName || !trimmed.endsWith(')')) return null
  const args = splitTopLevelArgs(trimmed.slice(functionName.length + 1, -1))
  if (args.length !== (functionName === 'rule_override_tier' ? 4 : 3)) {
    return null
  }
  const nested = parseRuntimeOverrideExpr(args[0])
  let nestedRules: string[] = []
  let baseExpr = args[0]
  if (nested) {
    nestedRules = nested.rules
    baseExpr = nested.baseExpr
  } else {
    const split = splitBillingExprAndRequestRules(args[0])
    nestedRules = split.requestRuleExpr.split(' * ').filter(Boolean)
    baseExpr = split.billingExpr
  }
  const currentRule =
    functionName === 'rule_override_tier'
      ? `override_rule(${args[1]}, tier(${args[3]}, ${args[2]}))`
      : `override_rule(${args[1]}, ${args[2]})`
  return { baseExpr, rules: [currentRule, ...nestedRules] }
}

export function splitBillingExprAndRequestRules(expr: string): {
  billingExpr: string
  requestRuleExpr: string
} {
  const trimmed = (expr || '').trim()
  if (!trimmed) return { billingExpr: '', requestRuleExpr: '' }

  const parts = splitTopLevelMultiply(trimmed)
  if (parts.length <= 1) {
    const runtimeOverride = parseRuntimeOverrideExpr(trimmed)
    if (runtimeOverride) {
      return {
        billingExpr: runtimeOverride.baseExpr,
        requestRuleExpr: runtimeOverride.rules.join(' * '),
      }
    }
    return { billingExpr: trimmed, requestRuleExpr: '' }
  }

  const ruleParts: string[] = []
  const baseParts: string[] = []

  parts.forEach((part) => {
    const parsed = tryParseRequestRuleExpr(part)
    const compiled = compileBillingExpression(part)
    const traced =
      compiled.status === 'ready' &&
      compiled.requestRules.some((rule) => rule.node === compiled.ast)
    if ((parsed && parsed.length > 0) || traced) {
      ruleParts.push(part)
    } else {
      baseParts.push(part)
    }
  })

  if (baseParts.length === 1) {
    const runtimeOverride = parseRuntimeOverrideExpr(baseParts[0])
    if (runtimeOverride) {
      const allRules = [...runtimeOverride.rules, ...ruleParts]
      return {
        billingExpr: runtimeOverride.baseExpr,
        requestRuleExpr: allRules.join(' * '),
      }
    }
  }

  const quantityParts = baseParts.filter(
    (part) => unwrapOuterParens(part) === 'image_count'
  )
  if (ruleParts.length === 0 || baseParts.length - quantityParts.length !== 1) {
    return { billingExpr: trimmed, requestRuleExpr: '' }
  }

  return {
    billingExpr: baseParts.map(unwrapOuterParens).join(' * '),
    requestRuleExpr: ruleParts.join(' * '),
  }
}

export function combineBillingExpr(
  baseExpr: string,
  requestRuleExpr: string
): string {
  const base = (baseExpr || '').trim()
  const rules = (requestRuleExpr || '').trim()
  if (!base) return ''
  if (!rules) return base

  const parts = splitTopLevelMultiply(rules)
  const overrideParts: string[] = []
  const multiplierParts: string[] = []
  for (const part of parts) {
    if (part.trim().startsWith('override_rule(')) {
      overrideParts.push(part.trim())
    } else {
      multiplierParts.push(part.trim())
    }
  }

  let combined = base
  if (multiplierParts.length > 0) {
    combined = `(${combined}) * ${multiplierParts.join(' * ')}`
  }
  for (let index = overrideParts.length - 1; index >= 0; index -= 1) {
    const args = splitTopLevelArgs(
      overrideParts[index].trim().slice('override_rule('.length, -1)
    )
    if (args.length !== 2) return `(${base}) * ${rules}`
    const costExpr = args[1].trim()
    const costArgs = splitTopLevelArgs(
      costExpr.startsWith('tier(') ? costExpr.slice('tier('.length, -1) : ''
    )
    if (costArgs.length !== 2) {
      return `(${base}) * ${rules}`
    }
    combined = `rule_override_tier(${combined}, ${args[0]}, ${costArgs[1]}, ${costArgs[0]})`
  }
  return combined
}

// ---------------------------------------------------------------------------
// Editor: empty constructors
// ---------------------------------------------------------------------------

export function createEmptyCondition(): ParamHeaderCondition {
  return { source: 'param', path: '', mode: MATCH_EQ, value: '' }
}

export function createEmptyTimeCondition(): TimeCondition {
  return {
    source: 'time',
    timeFunc: 'hour',
    timezone: 'Asia/Shanghai',
    mode: MATCH_GTE,
    value: '',
    rangeStart: '',
    rangeEnd: '',
  }
}

export function createEmptyRuleGroup(): RequestRuleGroup {
  return { conditions: [createEmptyCondition()], multiplier: '' }
}

export function createEmptyTimeRuleGroup(): RequestRuleGroup {
  return { conditions: [createEmptyTimeCondition()], multiplier: '' }
}

// ---------------------------------------------------------------------------
// Editor: match option helpers
// ---------------------------------------------------------------------------

export type MatchOption = { value: string; labelKey: string }

export function getRequestRuleMatchOptions(source: string): MatchOption[] {
  if (source === SOURCE_TIME) {
    return [
      { value: MATCH_EQ, labelKey: 'Equals' },
      { value: MATCH_GTE, labelKey: 'Greater than or equal' },
      { value: MATCH_LT, labelKey: 'Less than' },
      { value: MATCH_RANGE, labelKey: 'Overnight range' },
    ]
  }
  const base: MatchOption[] = [
    { value: MATCH_EQ, labelKey: 'Equals' },
    { value: MATCH_CONTAINS, labelKey: 'Contains' },
    { value: MATCH_EXISTS, labelKey: 'Exists' },
  ]
  if (source === SOURCE_HEADER) return base
  return [
    ...base,
    { value: MATCH_GT, labelKey: 'Greater than' },
    { value: MATCH_GTE, labelKey: 'Greater than or equal' },
    { value: MATCH_LT, labelKey: 'Less than' },
    { value: MATCH_LTE, labelKey: 'Less than or equal' },
  ]
}

// ---------------------------------------------------------------------------
// Editor: normalize a single condition
// ---------------------------------------------------------------------------

function isTimeFunc(value: unknown): value is TimeFunc {
  return typeof value === 'string' && TIME_FUNCS.includes(value as TimeFunc)
}

export function normalizeCondition(
  cond: Partial<RequestCondition> | null | undefined
): RequestCondition {
  let source: 'time' | 'param' | 'header' | 'multipart' = 'param'
  if (cond?.source === 'time') source = 'time'
  else if (cond?.source === SOURCE_MULTIPART) source = SOURCE_MULTIPART
  else if (cond?.source === 'header') source = 'header'

  if (source === 'time') {
    const timeCond = cond as Partial<TimeCondition> | null | undefined
    const timeFunc: TimeFunc = isTimeFunc(timeCond?.timeFunc)
      ? timeCond.timeFunc
      : 'hour'
    const options = getRequestRuleMatchOptions(SOURCE_TIME)
    const mode = options.some((item) => item.value === timeCond?.mode)
      ? (timeCond?.mode as string)
      : MATCH_GTE
    return {
      source: 'time',
      timeFunc,
      timezone: timeCond?.timezone || 'Asia/Shanghai',
      mode,
      value: timeCond?.value == null ? '' : String(timeCond.value),
      rangeStart:
        timeCond?.rangeStart == null ? '' : String(timeCond.rangeStart),
      rangeEnd: timeCond?.rangeEnd == null ? '' : String(timeCond.rangeEnd),
    }
  }

  const phCond = cond as Partial<ParamHeaderCondition> | null | undefined
  const options = getRequestRuleMatchOptions(source)
  const mode = options.some((item) => item.value === phCond?.mode)
    ? (phCond?.mode as string)
    : MATCH_EQ
  return {
    source,
    path: phCond?.path || '',
    mode,
    value: phCond?.value == null ? '' : String(phCond.value),
  }
}

// ---------------------------------------------------------------------------
// Editor: build expression strings
// ---------------------------------------------------------------------------

function buildExprLiteral(mode: string, value: string): string {
  const text = String(value || '').trim()
  if (mode === MATCH_CONTAINS) return JSON.stringify(text)
  if (text === 'true' || text === 'false') return text
  if (NUMERIC_LITERAL_REGEX.test(text)) return text
  return JSON.stringify(text)
}

function buildTimeConditionExpr(cond: TimeCondition): string {
  const normalized = normalizeCondition(cond) as TimeCondition
  const { timeFunc, timezone, mode } = normalized
  const tz = JSON.stringify(timezone)
  const fn = `${timeFunc}(${tz})`

  if (mode === MATCH_RANGE) {
    const s = normalized.rangeStart.trim()
    const e = normalized.rangeEnd.trim()
    if (!isTimeValueInRange(timeFunc, s) || !isTimeValueInRange(timeFunc, e)) {
      return ''
    }
    // Overnight range (start > end) crosses the day boundary, e.g. 21-6.
    // A within-day range (start <= end), e.g. 9-12, must use && so the
    // condition is not a tautology that always applies the multiplier.
    const sNum = Number(s)
    const eNum = Number(e)
    if (sNum > eNum) {
      return `${fn} >= ${s} || ${fn} < ${e}`
    }
    return `${fn} >= ${s} && ${fn} < ${e}`
  }
  const v = normalized.value.trim()
  if (!isTimeValueInRange(timeFunc, v)) return ''
  const opMap: Record<string, string> = {
    [MATCH_EQ]: '==',
    [MATCH_GTE]: '>=',
    [MATCH_LT]: '<',
  }
  return `${fn} ${opMap[mode] || '=='} ${v}`
}

function buildRequestConditionExpr(cond: RequestCondition): string {
  if (cond.source === 'time') return buildTimeConditionExpr(cond)
  const normalized = normalizeCondition(cond) as ParamHeaderCondition
  const path = normalized.path.trim()
  if (!path) return ''

  let sourceExpr: string
  if (normalized.source === SOURCE_HEADER) {
    sourceExpr = `header(${JSON.stringify(path)})`
  } else if (normalized.source === SOURCE_MULTIPART) {
    sourceExpr = `multipart_param(${JSON.stringify(path)})`
  } else {
    sourceExpr = `param(${JSON.stringify(path)})`
  }

  switch (normalized.mode) {
    case MATCH_EXISTS:
      return normalized.source === 'header'
        ? `${sourceExpr} != ""`
        : `${sourceExpr} != nil`
    case MATCH_CONTAINS:
      return normalized.source === 'header'
        ? `has(${sourceExpr}, ${buildExprLiteral(normalized.mode, normalized.value)})`
        : `${sourceExpr} != nil && has(${sourceExpr}, ${buildExprLiteral(normalized.mode, normalized.value)})`
    case MATCH_GT:
    case MATCH_GTE:
    case MATCH_LT:
    case MATCH_LTE: {
      const opMap: Record<string, string> = {
        [MATCH_GT]: '>',
        [MATCH_GTE]: '>=',
        [MATCH_LT]: '<',
        [MATCH_LTE]: '<=',
      }
      const numText = String(normalized.value).trim()
      if (!NUMERIC_LITERAL_REGEX.test(numText)) return ''
      return `${sourceExpr} != nil && ${sourceExpr} ${opMap[normalized.mode]} ${numText}`
    }
    case MATCH_EQ:
    default:
      return `${sourceExpr} == ${buildExprLiteral(normalized.mode, normalized.value)}`
  }
}

function buildOverrideCostExpr(override: RequestPriceOverride): string {
  const price = (value: number | string | undefined): number => {
    const normalized = Number(value)
    return Number.isFinite(normalized) && normalized >= 0 ? normalized : 0
  }
  const promptTerms: Array<[string, keyof RequestPriceOverride]> = [
    ['cr_total', 'cache_read_unit_cost'],
    ['cc_total', 'cache_create_unit_cost'],
    ['cc1h_total', 'cache_create_1h_unit_cost'],
    ['img_total', 'image_unit_cost'],
    ['ai_total', 'audio_input_unit_cost'],
  ]
  const outputTerms: Array<[string, keyof RequestPriceOverride]> = [
    ['img_o_total', 'image_output_unit_cost'],
    ['ao_total', 'audio_output_unit_cost'],
  ]
  const promptUsed = promptTerms.filter(
    ([, field]) => override[field] !== undefined
  )
  const outputUsed = outputTerms.filter(
    ([, field]) => override[field] !== undefined
  )
  const promptRemainder = promptUsed.map(([variable]) => variable).join(' - ')
  const outputRemainder = outputUsed.map(([variable]) => variable).join(' - ')
  const parts = [
    `(${['p_total', promptRemainder].filter(Boolean).join(' - ')}) * ${price(override.input_unit_cost)}`,
    `(${['c_total', outputRemainder].filter(Boolean).join(' - ')}) * ${price(override.output_unit_cost)}`,
  ]
  const fields: Array<[string, keyof RequestPriceOverride]> = [
    ...promptTerms,
    ...outputTerms,
  ]
  for (const [variable, field] of fields) {
    if (override[field] !== undefined) {
      parts.push(`${variable} * ${price(override[field])}`)
    }
  }
  const label = override.label || 'override'
  return `tier(${JSON.stringify(label)}, ${parts.join(' + ')})`
}

function buildRuleGroupFactor(group: RequestRuleGroup): string {
  const condExprs = (group.conditions || [])
    .map(buildRequestConditionExpr)
    .filter(Boolean)
  if (condExprs.length === 0) return ''

  const combined =
    condExprs.length === 1
      ? condExprs[0]
      : condExprs.map((e) => (e.includes(' || ') ? `(${e})` : e)).join(' && ')

  if (group.action === 'override') {
    return `override_rule(${combined}, ${buildOverrideCostExpr(group.override || {})})`
  }

  const multiplier = (group.multiplier || '').trim()
  if (!NUMERIC_LITERAL_REGEX.test(multiplier)) return ''
  return `(${combined} ? ${multiplier} : 1)`
}

export function buildRequestRuleExpr(groups: RequestRuleGroup[]): string {
  return (groups || []).map(buildRuleGroupFactor).filter(Boolean).join(' * ')
}
