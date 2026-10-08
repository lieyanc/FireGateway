import type { PortRange } from "@/lib/types"

export const GIB = 1024 ** 3

/** "80, 1000–1999" style summary of a tenant's port pool. */
export function formatPortRanges(ranges: PortRange[]) {
  return ranges.map(([a, b]) => (a === b ? String(a) : `${a}–${b}`)).join(", ")
}

/** Bytes as a GiB string for the form; up to three decimals, "" for 0. */
export function bytesToGiB(bytes: number | undefined) {
  if (!bytes) return ""
  return String(Number((bytes / GIB).toFixed(3)))
}

/** Share of a quota in use, clamped to [0, 1]. */
export function usageRatio(used: number, limit: number) {
  if (limit <= 0) return 0
  return Math.min(1, Math.max(0, used / limit))
}
