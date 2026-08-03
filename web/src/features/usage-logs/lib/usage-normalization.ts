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
import type { UsageNormalizationAudit } from '../types'

const ACCOUNTING_MODE_LABELS: Record<string, string> = {
  included: 'Input includes cache',
  separate: 'Cache reported separately',
}

const NORMALIZATION_SOURCE_LABELS: Record<string, string> = {
  billing_usage: 'Billing usage metadata',
  usage_source: 'Usage source marker',
  usage_semantic: 'Usage semantic marker',
  total_tokens: 'Total token reconciliation',
  fallback: 'Fallback',
}

const NORMALIZATION_STATUS_LABELS: Record<string, string> = {
  matched: 'Matched',
  not_checked: 'Not checked',
  mismatch: 'Mismatch',
}

function labelKey(labels: Record<string, string>, value: string): string {
  return labels[value] || value || 'Unknown'
}

export function getUsageAccountingModeLabelKey(mode: string): string {
  return labelKey(ACCOUNTING_MODE_LABELS, mode)
}

export function getUsageNormalizationSourceLabelKey(source: string): string {
  return labelKey(NORMALIZATION_SOURCE_LABELS, source)
}

export function getUsageNormalizationStatusLabelKey(status: string): string {
  return labelKey(NORMALIZATION_STATUS_LABELS, status)
}

export function isUsageNormalizationWarning(
  audit: UsageNormalizationAudit
): boolean {
  return audit.source === 'fallback' || audit.status === 'mismatch'
}
