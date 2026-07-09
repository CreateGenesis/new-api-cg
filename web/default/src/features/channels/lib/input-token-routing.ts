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
export type InputTokenRoutingRange = {
  min_tokens: number
  max_tokens: number
}

export type ParseInputTokenRoutingRangesResult =
  | { ok: true; ranges: InputTokenRoutingRange[] }
  | { ok: false }

const RANGE_PATTERN = /^(\d+)\s*-\s*(\d+)$/

export function parseInputTokenRoutingRanges(
  value: string | undefined
): ParseInputTokenRoutingRangesResult {
  const lines = String(value || '')
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
  if (lines.length === 0) return { ok: false }

  const ranges: InputTokenRoutingRange[] = []
  for (const line of lines) {
    const match = RANGE_PATTERN.exec(line)
    if (!match) return { ok: false }

    const first = Number(match[1])
    const second = Number(match[2])
    if (
      !Number.isSafeInteger(first) ||
      !Number.isSafeInteger(second) ||
      first < 0 ||
      second < 0 ||
      (first === 0 && second === 0)
    ) {
      return { ok: false }
    }

    ranges.push({
      min_tokens: Math.min(first, second),
      max_tokens: Math.max(first, second),
    })
  }

  return { ok: true, ranges }
}

export function formatInputTokenRoutingRanges(
  ranges: InputTokenRoutingRange[]
): string {
  return ranges
    .map((item) => `${item.min_tokens}-${item.max_tokens}`)
    .join('\n')
}
