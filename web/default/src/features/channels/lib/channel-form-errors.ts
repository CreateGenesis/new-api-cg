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
  'simulated_model_cache_enabled',
  'simulated_model_cache_ttl_seconds',
  'simulated_model_cache_min_match_ratio',
  'status_code_retry_enabled',
  'status_code_retry_times',
  'status_code_retry_interval_ms',
  'status_code_retry_status_codes',
  'input_token_routing_enabled',
  'input_token_routing_glm_5_2_mode',
  'input_token_routing_ranges',
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
