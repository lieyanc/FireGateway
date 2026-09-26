/* eslint-disable react-refresh/only-export-components */
import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"

import { qk } from "@/lib/queries"
import { statsStore } from "@/lib/stats-store"
import type { RulesEvent, StatsEvent } from "@/lib/types"

export type LiveStatus = "idle" | "connecting" | "open" | "reconnecting"

type EventsContextValue = {
  status: LiveStatus
  /** Epoch ms of the next reconnect attempt while reconnecting. */
  retryAt: number | null
  reconnectNow: () => void
}

const EventsContext = React.createContext<EventsContextValue>({
  status: "idle",
  retryAt: null,
  reconnectNow: () => {},
})

const MIN_BACKOFF = 1_000
const MAX_BACKOFF = 30_000

/**
 * Owns the single shared EventSource on /api/events for the whole app.
 * `stats` ticks go to the external stats store; `rules` events invalidate
 * rule queries. Reconnects with exponential backoff.
 */
export function EventsProvider({
  enabled,
  children,
}: {
  enabled: boolean
  children: React.ReactNode
}) {
  const queryClient = useQueryClient()
  const [status, setStatus] = React.useState<LiveStatus>("idle")
  const [retryAt, setRetryAt] = React.useState<number | null>(null)
  const [generation, setGeneration] = React.useState(0)

  React.useEffect(() => {
    if (!enabled) {
      statsStore.reset()
      return
    }

    let source: EventSource | null = null
    let timer: ReturnType<typeof setTimeout> | undefined
    let attempt = 0
    let disposed = false
    let hadConnection = false

    const invalidateRules = () => {
      void queryClient.invalidateQueries({ queryKey: qk.rules })
      void queryClient.invalidateQueries({ queryKey: qk.overview })
    }

    const connect = () => {
      if (disposed) return
      setStatus(attempt === 0 ? "connecting" : "reconnecting")
      setRetryAt(null)
      source = new EventSource("/api/events", { withCredentials: true })

      source.onopen = () => {
        attempt = 0
        setStatus("open")
        setRetryAt(null)
        // Anything could have changed while we were disconnected.
        if (hadConnection) invalidateRules()
        hadConnection = true
      }

      source.addEventListener("stats", (event) => {
        try {
          statsStore.push(JSON.parse((event as MessageEvent).data) as StatsEvent)
        } catch {
          // Ignore a malformed frame; the next tick replaces it.
        }
      })

      source.addEventListener("rules", (event) => {
        let data: RulesEvent | null = null
        try {
          data = JSON.parse((event as MessageEvent).data) as RulesEvent
        } catch {
          // Fall through: refetch everything.
        }
        invalidateRules()
        if (data?.id && data.action === "deleted") {
          queryClient.removeQueries({ queryKey: qk.rule(data.id) })
        }
      })

      source.onerror = () => {
        source?.close()
        source = null
        if (disposed) return
        // An expired session looks like a stream error: re-check auth.
        void queryClient.invalidateQueries({ queryKey: qk.authState })
        const delay = Math.min(MAX_BACKOFF, MIN_BACKOFF * 2 ** attempt)
        const jitter = Math.random() * 0.2 * delay
        attempt++
        setStatus("reconnecting")
        setRetryAt(Date.now() + delay + jitter)
        timer = setTimeout(connect, delay + jitter)
      }
    }

    connect()

    return () => {
      disposed = true
      clearTimeout(timer)
      source?.close()
      setStatus("idle")
      setRetryAt(null)
    }
  }, [enabled, generation, queryClient])

  const reconnectNow = React.useCallback(() => setGeneration((g) => g + 1), [])

  const value = React.useMemo(
    () => ({ status, retryAt, reconnectNow }),
    [status, retryAt, reconnectNow]
  )

  return (
    <EventsContext.Provider value={value}>{children}</EventsContext.Provider>
  )
}

export function useLiveStatus() {
  return React.useContext(EventsContext)
}
