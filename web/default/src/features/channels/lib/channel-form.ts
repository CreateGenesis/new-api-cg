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
import { z } from 'zod'

import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

import {
  CHANNEL_STATUS,
  ERROR_MESSAGES,
  MODEL_FETCHABLE_TYPES,
} from '../constants'
import type { Channel } from '../types'
import {
  CHANNEL_TYPE_ADVANCED_CUSTOM,
  advancedCustomConfigUsesRelativeUpstreamPath,
  parseAdvancedCustomConfig,
  stringifyAdvancedCustomConfig,
  validateAdvancedCustomConfig,
} from './advanced-custom'
import {
  formatInputTokenRoutingRanges,
  parseInputTokenRoutingRanges,
  type InputTokenRoutingRange,
} from './input-token-routing'

// ============================================================================
// Form Validation Schema
// ============================================================================

function parseOptionalJson(value: string | undefined): unknown {
  if (!value?.trim()) return undefined
  return JSON.parse(value)
}

function isJsonObjectValue(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isOptionalJsonObject(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    return parsed === undefined || isJsonObjectValue(parsed)
  } catch {
    return false
  }
}

function isOptionalModelMapping(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (!isJsonObjectValue(parsed)) return false
    return Object.values(parsed).every((item) => typeof item === 'string')
  } catch {
    return false
  }
}

function isOptionalStatusCodeMapping(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (!isJsonObjectValue(parsed)) return false
    return Object.entries(parsed).every(([from, to]) => {
      const fromCode = Number(from)
      const toCode = Number(to)
      return (
        Number.isInteger(fromCode) &&
        Number.isInteger(toCode) &&
        fromCode >= 100 &&
        fromCode <= 599 &&
        toCode >= 100 &&
        toCode <= 599
      )
    })
  } catch {
    return false
  }
}

function isCodexCredential(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    return (
      isJsonObjectValue(parsed) &&
      typeof parsed.access_token === 'string' &&
      parsed.access_token.trim().length > 0 &&
      typeof parsed.account_id === 'string' &&
      parsed.account_id.trim().length > 0
    )
  } catch {
    return false
  }
}

function isVertexJsonKey(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (Array.isArray(parsed)) {
      return parsed.every((item) => isJsonObjectValue(item))
    }
    return isJsonObjectValue(parsed)
  } catch {
    return false
  }
}

function addRequiredIssue(
  ctx: z.RefinementCtx,
  path: string,
  message: string
): void {
  ctx.addIssue({
    code: z.ZodIssueCode.custom,
    path: [path],
    message,
  })
}

export const DEFAULT_STATUS_CODE_RETRY_STATUS_CODES =
  '100-199,300-399,401-407,409-499,500-503,505-523,525-599'
export const DEFAULT_STATUS_CODE_RETRY_INTERVAL_MS = 50

export const channelFormSchema = z
  .object({
    name: z.string().min(1, ERROR_MESSAGES.REQUIRED_NAME),
    type: z.number().min(0, ERROR_MESSAGES.REQUIRED_TYPE),
    base_url: z.string().optional(),
    key: z.string(),
    openai_organization: z.string().optional(),
    models: z.string().min(1, ERROR_MESSAGES.REQUIRED_MODELS),
    group: z.array(z.string()).min(1, ERROR_MESSAGES.REQUIRED_GROUP),
    model_mapping: z
      .string()
      .optional()
      .refine(
        isOptionalModelMapping,
        'Model mapping must be a JSON object with string values'
      ),
    priority: z.number().optional(),
    weight: z.number().optional(),
    test_model: z.string().optional(),
    auto_ban: z.number().optional(),
    status: z.number(),
    status_code_mapping: z
      .string()
      .optional()
      .refine(
        isOptionalStatusCodeMapping,
        'Status code mapping must use valid HTTP status codes'
      ),
    tag: z.string().optional(),
    remark: z
      .string()
      .max(255, 'Remark must be less than 255 characters')
      .optional(),
    setting: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    param_override: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    header_override: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    settings: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    advanced_custom: z.string().optional(),
    other: z.string().optional(),
    // Multi-key options (not sent to backend directly)
    multi_key_mode: z.enum(['single', 'batch', 'multi_to_single']).optional(),
    multi_key_type: z.enum(['random', 'polling', 'affinity']).optional(),
    multi_key_affinity_ttl_seconds: z.number().optional(),
    batch_add_set_key_prefix_2_name: z.boolean().optional(),
    key_mode: z.enum(['append', 'replace']).optional(), // For editing multi-key channels
    // Channel extra settings (stored in setting JSON, not sent directly)
    force_format: z.boolean().optional(),
    thinking_to_content: z.boolean().optional(),
    proxy: z.string().optional(),
    pass_through_body_enabled: z.boolean().optional(),
    system_prompt: z.string().optional(),
    system_prompt_override: z.boolean().optional(),
    // Type-specific settings (stored in settings JSON)
    is_enterprise_account: z.boolean().optional(), // OpenRouter specific
    vertex_key_type: z.enum(['json', 'api_key']).optional(), // Vertex AI specific
    aws_key_type: z.enum(['ak_sk', 'api_key']).optional(), // AWS specific
    azure_responses_version: z.string().optional(), // Azure specific
    // Field passthrough controls (stored in settings JSON)
    allow_service_tier: z.boolean().optional(), // OpenAI/Anthropic
    disable_store: z.boolean().optional(), // OpenAI only
    allow_safety_identifier: z.boolean().optional(), // OpenAI only
    allow_include_obfuscation: z.boolean().optional(), // OpenAI: include usage obfuscation
    allow_inference_geo: z.boolean().optional(), // OpenAI/Anthropic: inference geography
    allow_speed: z.boolean().optional(), // Anthropic: speed mode control
    claude_beta_query: z.boolean().optional(), // Anthropic: beta query passthrough
    disable_task_polling_sleep: z.boolean().optional(),
    simulated_model_cache_enabled: z.boolean().optional(),
    simulated_model_cache_ttl_seconds: z.number().optional(),
    simulated_model_cache_reuse_limit: z.number().optional(),
    simulated_model_cache_min_match_ratio: z.number().optional(),
    status_code_retry_enabled: z.boolean().optional(),
    status_code_retry_times: z.number().optional(),
    status_code_retry_interval_ms: z.number().optional(),
    status_code_retry_status_codes: z.string().optional(),
    input_token_routing_enabled: z.boolean().optional(),
    input_token_routing_ranges: z.string().optional(),
    // Upstream model update settings (stored in settings JSON)
    upstream_model_update_check_enabled: z.boolean().optional(),
    upstream_model_update_auto_sync_enabled: z.boolean().optional(),
    upstream_model_update_ignored_models: z.string().optional(),
  })
  .superRefine((data, ctx) => {
    if ([3, 8, 36, 45].includes(data.type) && !data.base_url?.trim()) {
      addRequiredIssue(
        ctx,
        'base_url',
        'Base URL is required for this channel type'
      )
    }

    if (data.type === CHANNEL_TYPE_ADVANCED_CUSTOM) {
      const advancedCustomConfig = parseAdvancedCustomConfig(
        data.advanced_custom
      )
      const advancedCustomError =
        validateAdvancedCustomConfig(advancedCustomConfig)
      if (advancedCustomError) {
        addRequiredIssue(ctx, 'advanced_custom', advancedCustomError.message)
      }
      if (
        advancedCustomConfigUsesRelativeUpstreamPath(advancedCustomConfig) &&
        !data.base_url?.trim()
      ) {
        addRequiredIssue(
          ctx,
          'base_url',
          'Base URL is required when an advanced route uses an upstream path'
        )
      }
    }

    if ([3, 18, 21, 39, 41, 49].includes(data.type) && !data.other?.trim()) {
      addRequiredIssue(
        ctx,
        'other',
        'This channel type requires additional configuration'
      )
    }

    if (data.type === 57) {
      if (data.multi_key_mode && data.multi_key_mode !== 'single') {
        addRequiredIssue(
          ctx,
          'multi_key_mode',
          'Codex channels do not support batch creation'
        )
      }
      if (data.key?.trim() && !isCodexCredential(data.key)) {
        addRequiredIssue(
          ctx,
          'key',
          'Codex credential must be a JSON object with access_token and account_id'
        )
      }
    }

    if (
      data.type === 41 &&
      data.vertex_key_type === 'json' &&
      data.key?.trim() &&
      !isVertexJsonKey(data.key)
    ) {
      addRequiredIssue(
        ctx,
        'key',
        'Vertex AI service account key must be valid JSON'
      )
    }

    if (
      data.type === 41 &&
      data.vertex_key_type === 'api_key' &&
      data.multi_key_mode &&
      data.multi_key_mode !== 'single'
    ) {
      addRequiredIssue(
        ctx,
        'multi_key_mode',
        'Vertex AI API Key mode does not support batch creation'
      )
    }

    if (data.multi_key_type === 'affinity') {
      if (
        !Number.isInteger(data.multi_key_affinity_ttl_seconds) ||
        Number(data.multi_key_affinity_ttl_seconds) < 1
      ) {
        addRequiredIssue(
          ctx,
          'multi_key_affinity_ttl_seconds',
          'Affinity TTL seconds must be at least 1.'
        )
      }
    }

    if (data.simulated_model_cache_enabled) {
      if (
        !Number.isInteger(data.simulated_model_cache_ttl_seconds) ||
        Number(data.simulated_model_cache_ttl_seconds) < 1
      ) {
        addRequiredIssue(
          ctx,
          'simulated_model_cache_ttl_seconds',
          'TTL seconds must be at least 1.'
        )
      }
      if (
        !Number.isInteger(data.simulated_model_cache_reuse_limit) ||
        Number(data.simulated_model_cache_reuse_limit) < 1
      ) {
        addRequiredIssue(
          ctx,
          'simulated_model_cache_reuse_limit',
          'Reuse limit must be at least 1.'
        )
      }
      const minMatchRatio = Number(data.simulated_model_cache_min_match_ratio)
      if (
        !Number.isFinite(minMatchRatio) ||
        minMatchRatio < 0.01 ||
        minMatchRatio > 1
      ) {
        addRequiredIssue(
          ctx,
          'simulated_model_cache_min_match_ratio',
          'Minimum match ratio must be between 0.01 and 1.'
        )
      }
    }

    if (data.status_code_retry_enabled) {
      if (
        !Number.isInteger(data.status_code_retry_times) ||
        Number(data.status_code_retry_times) < 0
      ) {
        addRequiredIssue(
          ctx,
          'status_code_retry_times',
          'Retry times must be 0 or greater.'
        )
      }
      if (
        !Number.isInteger(data.status_code_retry_interval_ms) ||
        Number(data.status_code_retry_interval_ms) < 0
      ) {
        addRequiredIssue(
          ctx,
          'status_code_retry_interval_ms',
          'Retry interval must be 0 or greater.'
        )
      }
      const parsed = parseHttpStatusCodeRules(
        data.status_code_retry_status_codes || ''
      )
      if (!parsed.ok) {
        addRequiredIssue(
          ctx,
          'status_code_retry_status_codes',
          'Invalid status code rules.'
        )
      }
    }

    if (
      data.input_token_routing_enabled &&
      !parseInputTokenRoutingRanges(data.input_token_routing_ranges).ok
    ) {
      addRequiredIssue(
        ctx,
        'input_token_routing_ranges',
        'Invalid input token ranges.'
      )
    }
  })

export type ChannelFormValues = z.infer<typeof channelFormSchema>

// ============================================================================
// Default Form Values
// ============================================================================

export const CHANNEL_FORM_DEFAULT_VALUES: ChannelFormValues = {
  name: '',
  type: 1,
  base_url: '',
  key: '',
  openai_organization: '',
  models: '',
  group: ['default'],
  model_mapping: '',
  priority: 0,
  weight: 0,
  test_model: '',
  auto_ban: 1,
  status: CHANNEL_STATUS.ENABLED,
  status_code_mapping: '',
  tag: '',
  remark: '',
  setting: '',
  param_override: '',
  header_override: '',
  settings: '{}',
  other: '',
  multi_key_mode: 'single',
  multi_key_type: 'random',
  multi_key_affinity_ttl_seconds: 3600,
  batch_add_set_key_prefix_2_name: false,
  key_mode: 'append',
  // Channel extra settings
  force_format: false,
  thinking_to_content: false,
  proxy: '',
  pass_through_body_enabled: false,
  system_prompt: '',
  system_prompt_override: false,
  // Type-specific settings
  is_enterprise_account: false,
  vertex_key_type: 'json',
  aws_key_type: 'ak_sk',
  azure_responses_version: '',
  // Field passthrough controls
  allow_service_tier: false,
  disable_store: false,
  allow_safety_identifier: false,
  allow_include_obfuscation: false,
  allow_inference_geo: false,
  allow_speed: false,
  claude_beta_query: false,
  disable_task_polling_sleep: false,
  simulated_model_cache_enabled: false,
  simulated_model_cache_ttl_seconds: 86400,
  simulated_model_cache_reuse_limit: 3,
  simulated_model_cache_min_match_ratio: 0.01,
  status_code_retry_enabled: false,
  status_code_retry_times: 10,
  status_code_retry_interval_ms: DEFAULT_STATUS_CODE_RETRY_INTERVAL_MS,
  status_code_retry_status_codes: DEFAULT_STATUS_CODE_RETRY_STATUS_CODES,
  input_token_routing_enabled: false,
  input_token_routing_ranges: '',
  upstream_model_update_check_enabled: false,
  upstream_model_update_auto_sync_enabled: false,
  upstream_model_update_ignored_models: '',
  advanced_custom: '',
}

// ============================================================================
// Transform Functions
// ============================================================================

/**
 * Transform Channel from API to Form default values
 */
export function transformChannelToFormDefaults(
  channel: Channel
): ChannelFormValues {
  // Parse channel extra settings from setting field
  let extraSettings = {
    force_format: false,
    thinking_to_content: false,
    proxy: '',
    pass_through_body_enabled: false,
    system_prompt: '',
    system_prompt_override: false,
  }

  if (channel.setting) {
    try {
      const parsed = JSON.parse(channel.setting)
      extraSettings = {
        force_format: parsed.force_format || false,
        thinking_to_content: parsed.thinking_to_content || false,
        proxy: parsed.proxy || '',
        pass_through_body_enabled: parsed.pass_through_body_enabled || false,
        system_prompt: parsed.system_prompt || '',
        system_prompt_override: parsed.system_prompt_override || false,
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse channel setting:', error)
    }
  }

  // Parse type-specific settings from settings field
  let vertexKeyType: 'json' | 'api_key' = 'json'
  let azureResponsesVersion = ''
  let isEnterpriseAccount = false
  let awsKeyType: 'ak_sk' | 'api_key' = 'ak_sk'
  let allowServiceTier = false
  let disableStore = false
  let allowSafetyIdentifier = false
  let allowIncludeObfuscation = false
  let allowInferenceGeo = false
  let allowSpeed = false
  let claudeBetaQuery = false
  let disableTaskPollingSleep = false
  let simulatedModelCacheEnabled = false
  let simulatedModelCacheTTLSeconds = 86400
  let simulatedModelCacheReuseLimit = 3
  let simulatedModelCacheMinMatchRatio = 0.01
  let statusCodeRetryEnabled = false
  let statusCodeRetryTimes = 10
  let statusCodeRetryIntervalMS = DEFAULT_STATUS_CODE_RETRY_INTERVAL_MS
  let statusCodeRetryStatusCodes = DEFAULT_STATUS_CODE_RETRY_STATUS_CODES
  let inputTokenRoutingEnabled = false
  let inputTokenRoutingRanges = ''
  let upstreamModelUpdateCheckEnabled = false
  let upstreamModelUpdateAutoSyncEnabled = false
  let upstreamModelUpdateIgnoredModels = ''
  let advancedCustom = ''

  if (channel.settings) {
    try {
      const parsed = JSON.parse(channel.settings)
      vertexKeyType = parsed.vertex_key_type || 'json'
      azureResponsesVersion = parsed.azure_responses_version || ''
      isEnterpriseAccount = parsed.openrouter_enterprise === true
      awsKeyType = parsed.aws_key_type || 'ak_sk'
      allowServiceTier = parsed.allow_service_tier === true
      disableStore = parsed.disable_store === true
      allowSafetyIdentifier = parsed.allow_safety_identifier === true
      allowIncludeObfuscation = parsed.allow_include_obfuscation === true
      allowInferenceGeo = parsed.allow_inference_geo === true
      allowSpeed = parsed.allow_speed === true
      claudeBetaQuery = parsed.claude_beta_query === true
      disableTaskPollingSleep = parsed.disable_task_polling_sleep === true
      if (
        parsed.simulated_model_cache &&
        typeof parsed.simulated_model_cache === 'object'
      ) {
        const simulatedCache = parsed.simulated_model_cache as Record<
          string,
          unknown
        >
        simulatedModelCacheEnabled = simulatedCache.enabled === true
        const ttlSeconds = Number(simulatedCache.ttl_seconds)
        if (Number.isFinite(ttlSeconds) && ttlSeconds > 0) {
          simulatedModelCacheTTLSeconds = ttlSeconds
        }
        const reuseLimit = Number(simulatedCache.reuse_limit)
        if (Number.isFinite(reuseLimit) && reuseLimit > 0) {
          simulatedModelCacheReuseLimit = reuseLimit
        }
        const minMatchRatio = Number(simulatedCache.min_match_ratio)
        if (
          Number.isFinite(minMatchRatio) &&
          minMatchRatio >= 0 &&
          minMatchRatio <= 1
        ) {
          simulatedModelCacheMinMatchRatio = minMatchRatio
        }
      }
      if (
        parsed.status_code_retry &&
        typeof parsed.status_code_retry === 'object'
      ) {
        const statusCodeRetry = parsed.status_code_retry as Record<
          string,
          unknown
        >
        statusCodeRetryEnabled = statusCodeRetry.enabled === true
        const retryTimes = Number(statusCodeRetry.retry_times)
        if (Number.isInteger(retryTimes) && retryTimes >= 0) {
          statusCodeRetryTimes = retryTimes
        }
        const retryIntervalMS = Number(statusCodeRetry.retry_interval_ms)
        if (Number.isInteger(retryIntervalMS) && retryIntervalMS >= 0) {
          statusCodeRetryIntervalMS = retryIntervalMS
        }
        if (
          typeof statusCodeRetry.status_codes === 'string' &&
          statusCodeRetry.status_codes.trim()
        ) {
          statusCodeRetryStatusCodes = statusCodeRetry.status_codes
        }
      }
      if (
        parsed.input_token_routing &&
        typeof parsed.input_token_routing === 'object'
      ) {
        const inputTokenRouting = parsed.input_token_routing as Record<
          string,
          unknown
        >
        inputTokenRoutingEnabled = inputTokenRouting.enabled === true
        const ranges: InputTokenRoutingRange[] = []
        if (Array.isArray(inputTokenRouting.ranges)) {
          for (const item of inputTokenRouting.ranges) {
            if (!item || typeof item !== 'object') continue
            const range = item as Record<string, unknown>
            const minTokens = Number(range.min_tokens ?? 0)
            const maxTokens = Number(range.max_tokens ?? 0)
            if (
              Number.isSafeInteger(minTokens) &&
              Number.isSafeInteger(maxTokens) &&
              minTokens >= 0 &&
              maxTokens >= 0 &&
              (minTokens > 0 || maxTokens > 0)
            ) {
              ranges.push({
                min_tokens: Math.min(minTokens, maxTokens),
                max_tokens: Math.max(minTokens, maxTokens),
              })
            }
          }
        } else {
          const minTokens = Number(inputTokenRouting.min_tokens ?? 0)
          const maxTokens = Number(inputTokenRouting.max_tokens ?? 0)
          if (
            Number.isSafeInteger(minTokens) &&
            Number.isSafeInteger(maxTokens) &&
            minTokens >= 0 &&
            maxTokens >= 0 &&
            (minTokens > 0 || maxTokens > 0)
          ) {
            ranges.push({
              min_tokens: Math.min(minTokens, maxTokens),
              max_tokens: Math.max(minTokens, maxTokens),
            })
          }
        }
        inputTokenRoutingRanges = formatInputTokenRoutingRanges(ranges)
      }
      upstreamModelUpdateCheckEnabled =
        parsed.upstream_model_update_check_enabled === true
      upstreamModelUpdateAutoSyncEnabled =
        parsed.upstream_model_update_auto_sync_enabled === true
      upstreamModelUpdateIgnoredModels = Array.isArray(
        parsed.upstream_model_update_ignored_models
      )
        ? parsed.upstream_model_update_ignored_models.join(',')
        : ''
      if (parsed.advanced_custom) {
        advancedCustom = stringifyAdvancedCustomConfig(parsed.advanced_custom)
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse channel settings:', error)
    }
  }

  return {
    name: channel.name || '',
    type: channel.type,
    base_url: channel.base_url || '',
    key: '', // Never populate key from backend for security
    openai_organization: channel.openai_organization || '',
    models: channel.models || '',
    group: parseGroups(channel.group || 'default'),
    model_mapping: channel.model_mapping || '',
    priority: channel.priority || 0,
    weight: channel.weight || 0,
    test_model: channel.test_model || '',
    auto_ban: channel.auto_ban ?? 1,
    status: channel.status,
    status_code_mapping: channel.status_code_mapping || '',
    tag: channel.tag || '',
    remark: channel.remark || '',
    setting: channel.setting || '',
    param_override: channel.param_override || '',
    header_override: channel.header_override || '',
    settings: channel.settings || '{}',
    other: channel.other || '',
    multi_key_mode: 'single',
    multi_key_type: channel.channel_info.multi_key_mode || 'random',
    multi_key_affinity_ttl_seconds:
      channel.channel_info.multi_key_affinity_ttl_seconds || 3600,
    batch_add_set_key_prefix_2_name: false,
    key_mode: 'append', // Default to append mode for editing multi-key channels
    // Channel extra settings
    ...extraSettings,
    // Type-specific settings
    is_enterprise_account: isEnterpriseAccount,
    vertex_key_type: vertexKeyType,
    azure_responses_version: azureResponsesVersion,
    aws_key_type: awsKeyType,
    allow_service_tier: allowServiceTier,
    disable_store: disableStore,
    allow_include_obfuscation: allowIncludeObfuscation,
    allow_inference_geo: allowInferenceGeo,
    allow_speed: allowSpeed,
    claude_beta_query: claudeBetaQuery,
    disable_task_polling_sleep: disableTaskPollingSleep,
    simulated_model_cache_enabled: simulatedModelCacheEnabled,
    simulated_model_cache_ttl_seconds: simulatedModelCacheTTLSeconds,
    simulated_model_cache_reuse_limit: simulatedModelCacheReuseLimit,
    simulated_model_cache_min_match_ratio: simulatedModelCacheMinMatchRatio,
    status_code_retry_enabled: statusCodeRetryEnabled,
    status_code_retry_times: statusCodeRetryTimes,
    status_code_retry_interval_ms: statusCodeRetryIntervalMS,
    status_code_retry_status_codes: statusCodeRetryStatusCodes,
    input_token_routing_enabled: inputTokenRoutingEnabled,
    input_token_routing_ranges: inputTokenRoutingRanges,
    allow_safety_identifier: allowSafetyIdentifier,
    upstream_model_update_check_enabled: upstreamModelUpdateCheckEnabled,
    upstream_model_update_auto_sync_enabled: upstreamModelUpdateAutoSyncEnabled,
    upstream_model_update_ignored_models: upstreamModelUpdateIgnoredModels,
    advanced_custom: advancedCustom,
  }
}

/**
 * Build the setting JSON string from form extra settings
 */
function buildSettingJSON(formData: ChannelFormValues): string {
  const settingObj = {
    force_format: formData.force_format || false,
    thinking_to_content: formData.thinking_to_content || false,
    proxy: formData.proxy || '',
    pass_through_body_enabled: formData.pass_through_body_enabled || false,
    system_prompt: formData.system_prompt || '',
    system_prompt_override: formData.system_prompt_override || false,
  }
  return JSON.stringify(settingObj)
}

/**
 * Build the settings JSON string (for type-specific config like vertex_key_type)
 */
function buildSettingsJSON(formData: ChannelFormValues): string {
  let settingsObj: Record<string, unknown> = {}

  // Try to parse existing settings first
  if (formData.settings && formData.settings !== '{}') {
    try {
      settingsObj = JSON.parse(formData.settings)
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse existing settings:', error)
    }
  }

  // Add vertex_key_type for Vertex AI channels (type 41)
  if (formData.type === 41) {
    settingsObj.vertex_key_type = formData.vertex_key_type || 'json'
  } else if ('vertex_key_type' in settingsObj) {
    delete settingsObj.vertex_key_type
  }

  // Add azure_responses_version for Azure channels (type 3)
  if (formData.type === 3 && formData.azure_responses_version) {
    settingsObj.azure_responses_version = formData.azure_responses_version
  } else if ('azure_responses_version' in settingsObj) {
    delete settingsObj.azure_responses_version
  }

  // Add enterprise account setting for OpenRouter (type 20)
  if (formData.type === 20) {
    settingsObj.openrouter_enterprise = formData.is_enterprise_account === true
  } else if ('openrouter_enterprise' in settingsObj) {
    delete settingsObj.openrouter_enterprise
  }

  // Add aws_key_type for AWS channels (type 33)
  if (formData.type === 33) {
    settingsObj.aws_key_type = formData.aws_key_type || 'ak_sk'
  } else if ('aws_key_type' in settingsObj) {
    delete settingsObj.aws_key_type
  }

  // Field passthrough controls:
  // - OpenAI (type 1) and Anthropic (type 14): allow_service_tier
  // - OpenAI only: disable_store, allow_safety_identifier
  if (formData.type === 1 || formData.type === 14 || formData.type === 57) {
    settingsObj.allow_service_tier = formData.allow_service_tier === true
  } else if ('allow_service_tier' in settingsObj) {
    delete settingsObj.allow_service_tier
  }

  if (formData.type === 1 || formData.type === 57) {
    settingsObj.disable_store = formData.disable_store === true
    settingsObj.allow_safety_identifier =
      formData.allow_safety_identifier === true
    settingsObj.allow_include_obfuscation =
      formData.allow_include_obfuscation === true
    settingsObj.allow_inference_geo = formData.allow_inference_geo === true
  } else {
    if ('disable_store' in settingsObj) {
      delete settingsObj.disable_store
    }
    if ('allow_safety_identifier' in settingsObj) {
      delete settingsObj.allow_safety_identifier
    }
    if ('allow_include_obfuscation' in settingsObj) {
      delete settingsObj.allow_include_obfuscation
    }
    if (formData.type !== 14 && 'allow_inference_geo' in settingsObj) {
      delete settingsObj.allow_inference_geo
    }
  }

  // Anthropic (type 14): claude_beta_query, allow_inference_geo, allow_speed
  if (formData.type === 14) {
    settingsObj.allow_inference_geo = formData.allow_inference_geo === true
    settingsObj.allow_speed = formData.allow_speed === true
    settingsObj.claude_beta_query = formData.claude_beta_query === true
  } else {
    if ('allow_speed' in settingsObj) {
      delete settingsObj.allow_speed
    }
    if ('claude_beta_query' in settingsObj) {
      delete settingsObj.claude_beta_query
    }
  }

  settingsObj.disable_task_polling_sleep =
    formData.disable_task_polling_sleep === true

  if (formData.simulated_model_cache_enabled === true) {
    const minMatchRatio = Number(formData.simulated_model_cache_min_match_ratio)
    settingsObj.simulated_model_cache = {
      enabled: true,
      ttl_seconds: Math.max(
        1,
        Math.trunc(Number(formData.simulated_model_cache_ttl_seconds) || 86400)
      ),
      reuse_limit: Math.max(
        1,
        Math.trunc(Number(formData.simulated_model_cache_reuse_limit) || 3)
      ),
      min_match_ratio: Math.min(
        1,
        Math.max(0.01, Number.isFinite(minMatchRatio) ? minMatchRatio : 0.01)
      ),
    }
  } else if ('simulated_model_cache' in settingsObj) {
    delete settingsObj.simulated_model_cache
  }

  if (formData.status_code_retry_enabled === true) {
    const retryTimes = Number(formData.status_code_retry_times)
    const retryIntervalMS = Number(formData.status_code_retry_interval_ms)
    const parsedStatusCodes = parseHttpStatusCodeRules(
      formData.status_code_retry_status_codes || ''
    )
    settingsObj.status_code_retry = {
      enabled: true,
      retry_times: Math.max(
        0,
        Math.trunc(Number.isFinite(retryTimes) ? retryTimes : 10)
      ),
      retry_interval_ms: Math.max(
        0,
        Math.trunc(
          Number.isFinite(retryIntervalMS)
            ? retryIntervalMS
            : DEFAULT_STATUS_CODE_RETRY_INTERVAL_MS
        )
      ),
      status_codes:
        parsedStatusCodes.ok && parsedStatusCodes.normalized
          ? parsedStatusCodes.normalized
          : DEFAULT_STATUS_CODE_RETRY_STATUS_CODES,
    }
  } else if ('status_code_retry' in settingsObj) {
    delete settingsObj.status_code_retry
  }

  if (formData.input_token_routing_enabled === true) {
    const parsedRanges = parseInputTokenRoutingRanges(
      formData.input_token_routing_ranges
    )
    if (parsedRanges.ok) {
      settingsObj.input_token_routing = {
        enabled: true,
        ranges: parsedRanges.ranges,
      }
    }
  } else if ('input_token_routing' in settingsObj) {
    delete settingsObj.input_token_routing
  }

  // Upstream model update settings (for model-fetchable channel types)
  if (MODEL_FETCHABLE_TYPES.has(formData.type)) {
    settingsObj.upstream_model_update_check_enabled =
      formData.upstream_model_update_check_enabled === true
    settingsObj.upstream_model_update_auto_sync_enabled =
      settingsObj.upstream_model_update_check_enabled === true &&
      formData.upstream_model_update_auto_sync_enabled === true
    settingsObj.upstream_model_update_ignored_models = [
      ...new Set(
        String(formData.upstream_model_update_ignored_models || '')
          .split(',')
          .map((model) => model.trim())
          .filter(Boolean)
      ),
    ]
    if (
      !Array.isArray(settingsObj.upstream_model_update_last_detected_models) ||
      settingsObj.upstream_model_update_check_enabled !== true
    ) {
      settingsObj.upstream_model_update_last_detected_models = []
    }
    if (typeof settingsObj.upstream_model_update_last_check_time !== 'number') {
      settingsObj.upstream_model_update_last_check_time = 0
    }
  }

  if (formData.type === CHANNEL_TYPE_ADVANCED_CUSTOM) {
    const advancedCustomConfig = parseAdvancedCustomConfig(
      formData.advanced_custom
    )
    if (advancedCustomConfig) {
      settingsObj.advanced_custom = advancedCustomConfig
    }
  } else if ('advanced_custom' in settingsObj) {
    delete settingsObj.advanced_custom
  }

  return JSON.stringify(settingsObj)
}

function normalizeBaseUrl(value: string | undefined): string {
  return String(value || '')
    .trim()
    .replace(/\/+$/, '')
}

/**
 * Transform form data to API payload for creating channel
 */
export function transformFormDataToCreatePayload(formData: ChannelFormValues): {
  mode: 'single' | 'batch' | 'multi_to_single'
  multi_key_mode?: 'random' | 'polling' | 'affinity'
  multi_key_affinity_ttl_seconds?: number
  batch_add_set_key_prefix_2_name?: boolean
  channel: Partial<Channel>
} {
  const mode = formData.multi_key_mode || 'single'

  const channel: Partial<Channel> = {
    name: formData.name,
    type: formData.type,
    base_url: normalizeBaseUrl(formData.base_url) || null,
    key: formData.key,
    openai_organization: formData.openai_organization || null,
    models: formData.models,
    group: formatGroups(formData.group),
    model_mapping: formData.model_mapping || null,
    priority: formData.priority || null,
    weight: formData.weight || null,
    test_model: formData.test_model || null,
    auto_ban: formData.auto_ban ?? 1,
    status: formData.status,
    status_code_mapping: formData.status_code_mapping || null,
    tag: formData.tag || null,
    remark: formData.remark || '',
    setting: buildSettingJSON(formData),
    param_override: formData.param_override || null,
    header_override: formData.header_override || null,
    settings: buildSettingsJSON(formData),
    other: formData.other || '',
  }

  // Clean up empty strings to null for optional fields
  Object.keys(channel).forEach((key) => {
    if (channel[key as keyof typeof channel] === '') {
      ;(channel as Record<string, unknown>)[key] = null
    }
  })

  return {
    mode,
    multi_key_mode:
      mode === 'multi_to_single' ? formData.multi_key_type : undefined,
    multi_key_affinity_ttl_seconds:
      mode === 'multi_to_single'
        ? Math.max(
            1,
            Math.trunc(Number(formData.multi_key_affinity_ttl_seconds) || 3600)
          )
        : undefined,
    batch_add_set_key_prefix_2_name:
      mode === 'batch' ? formData.batch_add_set_key_prefix_2_name : undefined,
    channel,
  }
}

/**
 * Transform form data to API payload for updating channel
 */
export function transformFormDataToUpdatePayload(
  formData: ChannelFormValues,
  channelId: number
): Partial<Channel> {
  const payload: Partial<Channel> = {
    id: channelId,
    name: formData.name,
    type: formData.type,
    base_url: normalizeBaseUrl(formData.base_url) || null,
    openai_organization: formData.openai_organization || null,
    models: formData.models,
    group: formatGroups(formData.group),
    model_mapping: formData.model_mapping || null,
    priority: formData.priority ?? 0,
    weight: formData.weight ?? 0,
    test_model: formData.test_model || null,
    auto_ban: formData.auto_ban ?? 1,
    status_code_mapping: formData.status_code_mapping || null,
    tag: formData.tag || null,
    remark: formData.remark || '',
    setting: buildSettingJSON(formData),
    param_override: formData.param_override || null,
    header_override: formData.header_override || null,
    settings: buildSettingsJSON(formData),
    other: formData.other || '',
  }

  // Only include key if it was changed (not empty)
  if (formData.key && formData.key.trim()) {
    payload.key = formData.key
  }

  // Clean up empty strings to null for optional fields
  Object.keys(payload).forEach((key) => {
    if (payload[key as keyof typeof payload] === '') {
      ;(payload as Record<string, unknown>)[key] = null
    }
  })

  // Send explicit empty strings for nullable fields so GORM updates can clear them.
  payload.base_url = normalizeBaseUrl(formData.base_url) || ''
  payload.openai_organization = formData.openai_organization || ''
  payload.test_model = formData.test_model || ''
  payload.tag = formData.tag || ''
  payload.remark = formData.remark || ''
  payload.model_mapping = formData.model_mapping || ''
  payload.status_code_mapping = formData.status_code_mapping || ''
  payload.param_override = formData.param_override || ''
  payload.header_override = formData.header_override || ''

  return payload
}

// ============================================================================
// Validation Helpers
// ============================================================================

/**
 * Validate JSON string
 */
export function validateJSON(value: string): boolean {
  if (!value || value.trim() === '') return true
  try {
    JSON.parse(value)
    return true
  } catch {
    return false
  }
}

/**
 * Validate model mapping format
 */
export function validateModelMapping(value: string): boolean {
  if (!value || value.trim() === '') return true
  return validateJSON(value)
}

/**
 * Parse models string to array
 */
export function parseModels(models: string): string[] {
  if (!models) return []
  return models
    .split(',')
    .map((m) => m.trim())
    .filter((m) => m.length > 0)
}

/**
 * Parse groups string to array
 */
export function parseGroups(groups: string): string[] {
  if (!groups) return []
  return groups
    .split(',')
    .map((g) => g.trim())
    .filter((g) => g.length > 0)
}

/**
 * Format models array to string
 */
export function formatModels(models: string[]): string {
  return models.join(',')
}

/**
 * Format groups array to string
 */
export function formatGroups(groups: string[]): string {
  return groups.join(',')
}
