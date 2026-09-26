import { useCallback, useEffect, useRef, useSyncExternalStore } from "react"

import type { Counters, MetricPoint, RuleStats, StatsEvent } from "@/lib/types"

// The 1 Hz `stats` stream lives here, outside React Query and React state.
// Components subscribe with selectors, so a tick only re-renders the parts
// that read a value which actually changed.

export const SERIES_CAPACITY = 300
export const TOTAL_SERIES = "__total__"

/** One point of a live throughput chart; up/down are bytes/s. */
export type RatePoint = { t: number; up: number; down: number }

type Listener = () => void

let latest: StatsEvent | null = null
const listeners = new Set<Listener>()

// Series are kept only while something displays them (reference counted),
// except the totals series which always runs so the dashboard opens warm.
const series = new Map<string, RatePoint[]>([[TOTAL_SERIES, []]])
const seriesRefs = new Map<string, number>()

function emit() {
  for (const listener of listeners) listener()
}

function subscribe(listener: Listener) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function appendPoint(key: string, point: RatePoint) {
  const current = series.get(key)
  if (!current) return
  const last = current[current.length - 1]
  let next: RatePoint[]
  if (last && point.t <= last.t) {
    if (point.t < last.t) return
    next = current.slice(0, -1)
    next.push(point)
  } else {
    next =
      current.length >= SERIES_CAPACITY
        ? current.slice(current.length - SERIES_CAPACITY + 1)
        : current.slice()
    next.push(point)
  }
  series.set(key, next)
}

function counterPoint(t: number, c: Counters | undefined): RatePoint {
  return { t, up: c?.rateUp ?? 0, down: c?.rateDown ?? 0 }
}

export const statsStore = {
  push(event: StatsEvent) {
    latest = event
    for (const key of series.keys()) {
      appendPoint(
        key,
        counterPoint(
          event.t,
          key === TOTAL_SERIES ? event.totals : event.rules[key]
        )
      )
    }
    emit()
  },

  /** Drops live values, e.g. after logout. */
  reset() {
    latest = null
    for (const key of series.keys()) series.set(key, [])
    emit()
  },

  getLatest() {
    return latest
  },

  /**
   * Merges historical points (from /api/metrics/realtime) under the live
   * ones. Live points win when both have the same timestamp.
   */
  seed(key: string, points: MetricPoint[]) {
    const current = series.get(key)
    if (!current) return
    const byTime = new Map<number, RatePoint>()
    // The newest bucket is still filling up; live ticks cover that second.
    for (const p of points.slice(0, -1)) {
      byTime.set(p.t, { t: p.t, up: p.up, down: p.down })
    }
    for (const p of current) byTime.set(p.t, p)
    const merged = [...byTime.values()].sort((a, b) => a.t - b.t)
    series.set(key, merged.slice(-SERIES_CAPACITY))
    emit()
  },

  retain(key: string) {
    seriesRefs.set(key, (seriesRefs.get(key) ?? 0) + 1)
    if (!series.has(key)) series.set(key, [])
  },

  release(key: string) {
    const refs = (seriesRefs.get(key) ?? 1) - 1
    if (refs > 0) {
      seriesRefs.set(key, refs)
      return
    }
    seriesRefs.delete(key)
    if (key !== TOTAL_SERIES) series.delete(key)
  },

  getSeries(key: string): RatePoint[] {
    return series.get(key) ?? EMPTY_SERIES
  },

  subscribe,
}

const EMPTY_SERIES: RatePoint[] = []

export function shallowEqual<T>(a: T, b: T) {
  if (Object.is(a, b)) return true
  if (
    typeof a !== "object" ||
    typeof b !== "object" ||
    a === null ||
    b === null
  ) {
    return false
  }
  const ka = Object.keys(a)
  const kb = Object.keys(b)
  if (ka.length !== kb.length) return false
  for (const k of ka) {
    if (
      !Object.is(
        (a as Record<string, unknown>)[k],
        (b as Record<string, unknown>)[k]
      )
    ) {
      return false
    }
  }
  return true
}

/**
 * Reads a derived value from the latest stats tick. The component only
 * re-renders when `isEqual(prev, next)` is false.
 */
export function useStats<T>(
  selector: (event: StatsEvent | null) => T,
  isEqual: (a: T, b: T) => boolean = shallowEqual
): T {
  const cache = useRef<{ source: StatsEvent | null; value: T } | null>(null)
  const selectorRef = useRef(selector)
  const isEqualRef = useRef(isEqual)
  useEffect(() => {
    selectorRef.current = selector
    isEqualRef.current = isEqual
  })

  const getSnapshot = useCallback(() => {
    const source = latest
    const prev = cache.current
    if (prev && prev.source === source) return prev.value
    const value = selectorRef.current(source)
    if (prev && isEqualRef.current(prev.value, value)) {
      cache.current = { source, value: prev.value }
      return prev.value
    }
    cache.current = { source, value }
    return value
  }, [])

  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}

/** Live counters of one rule, or undefined before the first tick. */
export function useRuleStats(id: string): RuleStats | undefined {
  return useStats((event) => event?.rules[id])
}

/** Live totals across all rules. */
export function useTotalStats(): Counters | undefined {
  return useStats((event) => event?.totals)
}

/** Whether at least one stats tick has arrived since the store was reset. */
export function useHasStats() {
  return useStats((event) => event !== null)
}

/** The bounded live series for a rule (or all rules when id is omitted). */
export function useLiveSeries(ruleId?: string): RatePoint[] {
  const key = ruleId ?? TOTAL_SERIES
  useEffect(() => {
    statsStore.retain(key)
    return () => statsStore.release(key)
  }, [key])

  const getSnapshot = useCallback(() => statsStore.getSeries(key), [key])
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
