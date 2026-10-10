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
import type { FieldErrors, FieldPath } from 'react-hook-form'

import {
  CHANNEL_TYPE_OLLAMA,
  CLAUDE_FIELD_PASSTHROUGH_TYPES,
  MODEL_FETCHABLE_TYPES,
  OPENAI_FIELD_PASSTHROUGH_TYPES,
} from '../constants'
import { CHANNEL_TYPE_ADVANCED_CUSTOM } from './advanced-custom'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  type ChannelFormValues,
} from './channel-form'
import { supportsResponsesWebSocket } from './responses-websocket'

export type ChannelProviderTarget =
  | { kind: 'builtin'; type: number }
  | { kind: 'plugin'; key: string }

export type ChannelConfigurationSection =
  | 'connection'
  | 'routing'
  | 'request'
  | 'other'

export type ChannelConfigurationStatus =
  | 'idle'
  | 'configured'
  | 'ready'
  | 'error'

const CONFIGURATION_BLOCKS = {
  modelMapping: {
    section: 'routing',
    fields: [
      'model_mapping',
      'response_model_mapping',
      'response_model_mapping_enabled',
    ],
  },
  routingStrategy: {
    section: 'routing',
    fields: [
      'priority',
      'weight',
      'test_model',
      'auto_ban',
      'group_scheduling',
      'input_token_routing_enabled',
      'input_token_routing_estimation_mode',
      'input_token_routing_ranges',
      'channel_overload_enabled',
      'channel_overload_requests_per_second',
      'channel_overload_requests_per_minute',
      'channel_overload_concurrent_requests',
      'channel_overload_recovery_seconds',
    ],
  },
  overrideRules: {
    section: 'request',
    fields: [
      'status_code_mapping',
      'param_override',
      'header_override',
      'header_rewrite_preset_id',
      'header_rewrite',
    ],
  },
  requestProcessing: {
    section: 'request',
    fields: [
      'force_format',
      'thinking_to_content',
      'pass_through_body_enabled',
      'responses_websocket_enabled',
      'ollama_openai_chat',
      'system_prompt',
      'system_prompt_override',
      'tnt_tencent_openai_conversion',
      'anthropic_input_includes_cache',
      'kimi_k3_official_compatibility',
      'glm_5_3_official_compatibility',
      'deepseek_v4_official_compatibility',
      'deepseek_v4_request_sanitization_enabled',
      'response_header_timeout_enabled',
      'response_header_timeout_seconds',
      'status_code_retry_enabled',
      'status_code_retry_times',
      'status_code_retry_interval_ms',
      'status_code_retry_status_codes',
      'retry_zero_output',
      'usage_estimation_enabled',
      'usage_estimation_model_family',
      'usage_estimation_input_multiplier',
      'usage_estimation_output_multiplier',
      'disable_stream',
      'disable_non_stream',
      'disable_video_understanding',
      'usage_token_limit_input_tokens',
      'usage_token_limit_output_tokens',
      'stream_interruption_billing_mode',
      'cache_usage_validation_split',
      'simulated_model_cache_enabled',
      'simulated_model_cache_multimodal_enabled',
    ],
  },
  fieldPassthrough: {
    section: 'request',
    fields: [
      'allow_service_tier',
      'disable_store',
      'allow_safety_identifier',
      'allow_include_obfuscation',
      'allow_inference_geo',
      'allow_speed',
      'claude_beta_query',
    ],
  },
  extraSettings: {
    section: 'other',
    fields: [
      'proxy',
      'proxy_fallback_direct',
      'http_protocol',
      'http2_connection_shards',
      'tls_insecure_skip_verify',
      'disable_task_polling_sleep',
    ],
  },
  upstreamModelDetection: {
    section: 'other',
    fields: [
      'upstream_model_update_check_enabled',
      'upstream_model_update_auto_sync_enabled',
      'upstream_model_update_ignored_models',
    ],
  },
  internalNotes: { section: 'other', fields: ['tag', 'remark'] },
} as const satisfies Record<
  string,
  {
    section: ChannelConfigurationSection
    fields: readonly FieldPath<ChannelFormValues>[]
  }
>

export type ChannelConfigurationBlock = keyof typeof CONFIGURATION_BLOCKS

export function getChannelConfigurationSection(
  field: string
): ChannelConfigurationSection {
  for (const block of Object.values(CONFIGURATION_BLOCKS)) {
    if ((block.fields as readonly string[]).includes(field)) {
      return block.section
    }
  }
  return 'connection'
}

function hasConfiguredJson(value: string | undefined): boolean {
  const trimmed = value?.trim()
  if (!trimmed || trimmed === 'null') return false
  try {
    const parsed = JSON.parse(trimmed)
    if (Array.isArray(parsed)) return parsed.length > 0
    if (typeof parsed === 'object') return Object.keys(parsed).length > 0
  } catch {
    // Preserve the presence of invalid input; validation errors take precedence.
  }
  return true
}

export function getChannelConfigurationState(
  values: ChannelFormValues,
  errors: FieldErrors<ChannelFormValues>,
  isEditing: boolean
) {
  const openaiPassthrough = OPENAI_FIELD_PASSTHROUGH_TYPES.has(values.type)
  const claudePassthrough = CLAUDE_FIELD_PASSTHROUGH_TYPES.has(values.type)
  const configured: Record<ChannelConfigurationBlock, boolean> = {
    modelMapping:
      hasConfiguredJson(values.model_mapping) ||
      Boolean(values.response_model_mapping_enabled) ||
      hasConfiguredJson(values.response_model_mapping),
    routingStrategy: Boolean(
      values.priority ||
      values.weight ||
      values.test_model?.trim() ||
      (values.auto_ban ?? 1) !== 1 ||
      values.group_scheduling?.enabled ||
      values.input_token_routing_enabled ||
      values.input_token_routing_estimation_mode !== 'default' ||
      values.input_token_routing_ranges?.trim() ||
      values.channel_overload_enabled
    ),
    overrideRules:
      hasConfiguredJson(values.status_code_mapping) ||
      hasConfiguredJson(values.param_override) ||
      hasConfiguredJson(values.header_override) ||
      Boolean(values.header_rewrite_preset_id?.trim()) ||
      hasConfiguredJson(values.header_rewrite),
    requestProcessing: Boolean(
      (values.type === 1 && values.force_format) ||
      values.thinking_to_content ||
      (values.type !== CHANNEL_TYPE_ADVANCED_CUSTOM &&
        values.pass_through_body_enabled) ||
      (supportsResponsesWebSocket(values.type) &&
        values.responses_websocket_enabled) ||
      (values.type === CHANNEL_TYPE_OLLAMA && values.ollama_openai_chat) ||
      values.system_prompt?.trim() ||
      values.system_prompt_override ||
      (values.type === 14 &&
        (values.tnt_tencent_openai_conversion ||
          values.anthropic_input_includes_cache)) ||
      ([1, 14, 25].includes(values.type) &&
        values.kimi_k3_official_compatibility) ||
      ([1, 14].includes(values.type) &&
        values.glm_5_3_official_compatibility) ||
      ([1, 14, 43].includes(values.type) &&
        values.deepseek_v4_official_compatibility) ||
      (values.type === 43 && values.deepseek_v4_request_sanitization_enabled) ||
      values.retry_zero_output ||
      values.usage_estimation_enabled ||
      values.disable_stream ||
      values.disable_non_stream ||
      values.disable_video_understanding ||
      (values.usage_token_limit_input_tokens ?? 0) > 0 ||
      (values.usage_token_limit_output_tokens ?? 0) > 0 ||
      values.status_code_retry_enabled ||
      values.response_header_timeout_enabled !==
        CHANNEL_FORM_DEFAULT_VALUES.response_header_timeout_enabled ||
      values.response_header_timeout_seconds !==
        CHANNEL_FORM_DEFAULT_VALUES.response_header_timeout_seconds ||
      values.stream_interruption_billing_mode !== 'off' ||
      values.cache_usage_validation_split ||
      values.simulated_model_cache_enabled ||
      values.simulated_model_cache_multimodal_enabled ||
      values.multi_key_type === 'cache_affinity_least_requests'
    ),
    fieldPassthrough: Boolean(
      ((openaiPassthrough || claudePassthrough) &&
        (values.allow_service_tier || values.allow_inference_geo)) ||
      (openaiPassthrough &&
        (values.disable_store ||
          values.allow_safety_identifier ||
          values.allow_include_obfuscation)) ||
      (claudePassthrough &&
        (values.allow_speed ||
          (values.type === 14 && values.claude_beta_query)))
    ),
    extraSettings: Boolean(
      values.proxy?.trim() ||
      values.proxy_fallback_direct ||
      (values.http_protocol && values.http_protocol !== 'auto') ||
      (values.http2_connection_shards ?? 1) > 1 ||
      values.tls_insecure_skip_verify ||
      values.disable_task_polling_sleep
    ),
    upstreamModelDetection:
      MODEL_FETCHABLE_TYPES.has(values.type) &&
      Boolean(
        values.upstream_model_update_check_enabled ||
        values.upstream_model_update_auto_sync_enabled ||
        values.upstream_model_update_ignored_models?.trim()
      ),
    internalNotes: Boolean(values.tag?.trim() || values.remark?.trim()),
  }
  const blocks = {} as Record<
    ChannelConfigurationBlock,
    ChannelConfigurationStatus
  >
  const sections: Record<
    ChannelConfigurationSection,
    ChannelConfigurationStatus
  > = {
    connection: 'idle',
    routing: 'idle',
    request: 'idle',
    other: 'idle',
  }
  for (const id of Object.keys(
    CONFIGURATION_BLOCKS
  ) as ChannelConfigurationBlock[]) {
    const block = CONFIGURATION_BLOCKS[id]
    const hasError = block.fields.some((field) => Boolean(errors[field]))
    blocks[id] = configured[id] ? 'configured' : 'idle'
    if (hasError) blocks[id] = 'error'
    if (hasError || (sections[block.section] !== 'error' && configured[id])) {
      sections[block.section] = blocks[id]
    }
  }

  // Reuse provider-specific validation without displaying errors before submit.
  const validation = channelFormSchema.safeParse(values)
  const connectionInvalid =
    !validation.success &&
    validation.error.issues.some(
      (issue) =>
        getChannelConfigurationSection(String(issue.path[0])) === 'connection'
    )
  if (
    !connectionInvalid &&
    values.name.trim() &&
    values.type > 0 &&
    values.models.trim() &&
    (isEditing || values.key?.trim())
  ) {
    sections.connection = 'ready'
  }
  if (
    Object.keys(errors).some(
      (field) => getChannelConfigurationSection(field) === 'connection'
    )
  ) {
    sections.connection = 'error'
  }
  return { blocks, sections }
}
