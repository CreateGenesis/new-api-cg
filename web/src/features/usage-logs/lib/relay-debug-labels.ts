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

const relayStageLabels: Record<string, string> = {
  channel_selection: 'Channel selection',
  overload_admission: 'Overload admission',
  relay: 'Upstream relay',
  retry_setup: 'Retry setup',
  task_submit: 'Task submission',
}

const relayActionLabels: Record<string, string> = {
  reroute_input_tokens: 'Reroute by input tokens',
  retry_same_channel: 'Retry same channel',
  rotate_key: 'Rotate channel key',
  stop: 'Stop retrying',
  switch_channel: 'Switch channel',
}

const relayReasonLabels: Record<string, string> = {
  affinity_locked: 'Channel affinity prevents switching',
  always_skip_code: 'Error code is excluded from retries',
  budget_exhausted: 'Retry budget exhausted',
  channel_error: 'Channel error is retryable',
  input_token_limit: 'Input token limit requires another route',
  no_error: 'No error',
  retry_setup_failed: 'Retry setup failed',
  same_channel_overload: 'Same channel became overloaded',
  skip_retry: 'Error explicitly disables retries',
  specific_channel: 'Request is locked to a specific channel',
  status_code_excluded: 'Status code is excluded from retries',
  status_code_match: 'Status code matches retry policy',
  task_local_error: 'Local task error is not retryable',
}

function translatedRelayLabel(
  value: string,
  labels: Record<string, string>,
  t: TFunction
): string {
  return t(labels[value] ?? value)
}

export function relayStageLabel(value: string, t: TFunction): string {
  return translatedRelayLabel(value, relayStageLabels, t)
}

export function relayActionLabel(value: string, t: TFunction): string {
  return translatedRelayLabel(value, relayActionLabels, t)
}

export function relayReasonLabel(value: string, t: TFunction): string {
  return translatedRelayLabel(value, relayReasonLabels, t)
}
