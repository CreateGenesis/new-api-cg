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
import type { FieldPath } from 'react-hook-form'

import type { ChannelFormValues } from './channel-form'

type ChannelFormErrorMap = Partial<
  Record<FieldPath<ChannelFormValues>, unknown>
>

const ADVANCED_SETTINGS_FIELDS = new Set<FieldPath<ChannelFormValues>>([
  'priority',
  'weight',
  'test_model',
  'auto_ban',
  'tag',
  'remark',
  'param_override',
  'header_override',
  'header_rewrite_preset_id',
  'header_rewrite',
  'status_code_mapping',
  'advanced_custom',
  'force_format',
  'thinking_to_content',
  'pass_through_body_enabled',
  'proxy',
  'proxy_fallback_direct',
  'system_prompt',
  'system_prompt_override',
  'allow_service_tier',
  'disable_store',
  'allow_safety_identifier',
  'allow_include_obfuscation',
  'allow_inference_geo',
  'allow_speed',
  'claude_beta_query',
  'disable_task_polling_sleep',
  'deepseek_v4_request_sanitization_enabled',
  'cache_usage_validation_split',
  'retry_zero_output',
  'disable_stream',
  'disable_non_stream',
  'missing_output_token_multiplier',
  'simulated_model_cache_enabled',
  'simulated_model_cache_estimate_missing_input_tokens',
  'simulated_model_cache_missing_input_token_multiplier',
  'simulated_model_cache_ttl_seconds',
  'simulated_model_cache_min_match_ratio',
  'simulated_model_cache_multimodal_enabled',
  'simulated_model_cache_image_tokens_per_megapixel',
  'simulated_model_cache_video_tokens_per_second_megapixel',
  'simulated_model_cache_audio_tokens_per_second',
  'simulated_model_cache_file_tokens_per_mib',
  'simulated_model_cache_image_fallback_tokens',
  'simulated_model_cache_video_fallback_tokens',
  'simulated_model_cache_audio_fallback_tokens',
  'simulated_model_cache_file_fallback_tokens',
  'status_code_retry_enabled',
  'status_code_retry_times',
  'status_code_retry_interval_ms',
  'status_code_retry_status_codes',
  'response_header_timeout_enabled',
  'response_header_timeout_seconds',
  'input_token_routing_enabled',
  'input_token_routing_estimation_mode',
  'input_token_routing_ranges',
  'stream_interruption_billing_mode',
  'upstream_model_update_check_enabled',
  'upstream_model_update_auto_sync_enabled',
  'upstream_model_update_ignored_models',
])

export function isAdvancedSettingsField(
  fieldName: string
): fieldName is FieldPath<ChannelFormValues> {
  return ADVANCED_SETTINGS_FIELDS.has(fieldName as FieldPath<ChannelFormValues>)
}

export function hasAdvancedSettingsErrors(
  errors: ChannelFormErrorMap
): boolean {
  return Object.keys(errors).some((fieldName) =>
    isAdvancedSettingsField(fieldName)
  )
}
