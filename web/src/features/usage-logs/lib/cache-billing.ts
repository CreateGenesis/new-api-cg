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
import type { LogOtherData } from '../types'

export interface CacheBillingUsage {
  hasAny: boolean
  read: boolean
  creation: boolean
  creation5m: boolean
  creation1h: boolean
}

export function getCacheBillingUsage(
  other: LogOtherData | null | undefined
): CacheBillingUsage {
  const read = (other?.cache_tokens || 0) > 0
  const creation5m = (other?.cache_creation_tokens_5m || 0) > 0
  const creation1h = (other?.cache_creation_tokens_1h || 0) > 0
  const creation =
    !creation5m && !creation1h && (other?.cache_creation_tokens || 0) > 0

  return {
    hasAny: read || creation || creation5m || creation1h,
    read,
    creation,
    creation5m,
    creation1h,
  }
}

export function hasAnyCacheTokens(
  other: LogOtherData | null | undefined
): boolean {
  return getCacheBillingUsage(other).hasAny
}
