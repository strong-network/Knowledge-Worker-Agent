// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Compact number formatting for token counts and other large metrics.
// Abbreviates with K (thousands), M (millions), and B (billions), keeping one
// decimal and trimming a trailing ".0".
//
//   compactNumber(500)                    -> "500"
//   compactNumber(2302)                   -> "2.3K"
//   compactNumber(2920751)                -> "2.9M"
//   compactNumber(1000000)                -> "1M"
//   compactNumber(1200000000)             -> "1.2B"
//   compactNumber(42315, { from: 'M' })   -> "42,315"   (below the M threshold)
//   compactNumber(2920751, { from: 'M' }) -> "2.9M"
//
// `from` sets the smallest unit at which to abbreviate:
//   'K' (default) — abbreviate from 1,000
//   'M'           — abbreviate only from 1,000,000 (values below render with
//                   full thousands separators)

type CompactUnit = 'K' | 'M'

interface CompactOptions {
  from?: CompactUnit
}

function trimDecimal(n: number): string {
  // One decimal place, dropping a trailing ".0" (2.0 -> "2", 2.3 -> "2.3").
  return n.toFixed(1).replace(/\.0$/, '')
}

// fullNumber renders a value with locale thousands separators (e.g. "42,315").
export function fullNumber(value: number | undefined | null): string {
  return new Intl.NumberFormat().format(value || 0)
}

export function compactNumber(
  value: number | undefined | null,
  opts: CompactOptions = {},
): string {
  const v = value || 0
  const abs = Math.abs(v)
  const from = opts.from || 'K'
  if (abs >= 1_000_000_000) return trimDecimal(v / 1_000_000_000) + 'B'
  if (abs >= 1_000_000) return trimDecimal(v / 1_000_000) + 'M'
  if (from === 'K' && abs >= 1_000) return trimDecimal(v / 1_000) + 'K'
  // Below the abbreviation threshold: full number with separators.
  return fullNumber(v)
}

// formatTokens is compactNumber with K abbreviation, named for its primary use.
export const formatTokens = (value: number | undefined | null): string =>
  compactNumber(value)

// Format a USD cost for display. Small per-turn costs (e.g. 0.0712) keep
// enough precision to be meaningful; larger costs round to cents.
export function formatCost(cost: number): string {
  if (cost > 0 && cost < 0.01) return `$${cost.toFixed(4)}`
  return `$${cost.toFixed(2)}`
}
