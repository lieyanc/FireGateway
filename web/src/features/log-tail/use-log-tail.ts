import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"

import { api } from "@/lib/api"
import { qk } from "@/lib/queries"
import type { LogEntry } from "@/lib/types"

export const MAX_LOG_ENTRIES = 5000
const BACKLOG_LIMIT = 2000
const FLUSH_MS = 150
const MIN_BACKOFF = 1_000
const MAX_BACKOFF = 30_000

export type TailStatus = "connecting" | "open" | "reconnecting"

function cap(list: LogEntry[]) {
  return list.length > MAX_LOG_ENTRIES
    ? list.slice(list.length - MAX_LOG_ENTRIES)
    : list
}

/**
 * Tails the server log: loads the backlog from GET /api/logs, then follows
 * /api/logs/stream. Entries are de-duplicated by `seq` and flushed to React
 * in small batches. Remount (e.g. with a `key`) to change the level.
 */
export function useLogTail(level: string | undefined) {
  const queryClient = useQueryClient()
  const [entries, setEntries] = React.useState<LogEntry[]>([])
  const [status, setStatus] = React.useState<TailStatus>("connecting")
  const [paused, setPausedState] = React.useState(false)
  const [buffered, setBuffered] = React.useState(0)
  const [backlogError, setBacklogError] = React.useState<unknown>(null)

  const pausedRef = React.useRef(false)
  const bufferRef = React.useRef<LogEntry[]>([])
  const lastSeqRef = React.useRef(-1)

  const append = React.useCallback((items: LogEntry[]) => {
    if (items.length === 0) return
    if (pausedRef.current) {
      bufferRef.current = cap(bufferRef.current.concat(items))
      setBuffered(bufferRef.current.length)
    } else {
      setEntries((prev) => cap(prev.concat(items)))
    }
  }, [])

  React.useEffect(() => {
    let disposed = false
    let source: EventSource | null = null
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    let flushTimer: ReturnType<typeof setTimeout> | undefined
    let attempt = 0
    let ready = false
    let pending: LogEntry[] = []
    let queue: LogEntry[] = []

    const flush = () => {
      flushTimer = undefined
      const items = queue
      queue = []
      if (!disposed) append(items)
    }

    const enqueue = (entry: LogEntry) => {
      if (entry.seq <= lastSeqRef.current) return
      lastSeqRef.current = entry.seq
      queue.push(entry)
      flushTimer ??= setTimeout(flush, FLUSH_MS)
    }

    const loadBacklog = async () => {
      try {
        const { items } = await api.logs.list({ limit: BACKLOG_LIMIT, level })
        if (disposed) return
        const newest = items.length ? items[items.length - 1].seq : -1
        // A lower newest seq than we have seen means the server restarted.
        if (newest < lastSeqRef.current) lastSeqRef.current = -1
        items.forEach(enqueue)
        setBacklogError(null)
      } catch (error) {
        if (!disposed) setBacklogError(error)
      }
      if (disposed) return
      ready = true
      const early = pending
      pending = []
      early.forEach(enqueue)
    }

    const connect = () => {
      if (disposed) return
      ready = false
      pending = []
      const url = level
        ? `/api/logs/stream?level=${encodeURIComponent(level)}`
        : "/api/logs/stream"
      source = new EventSource(url, { withCredentials: true })

      source.onopen = () => {
        attempt = 0
        setStatus("open")
        void loadBacklog()
      }

      source.addEventListener("log", (event) => {
        let entry: LogEntry
        try {
          entry = JSON.parse((event as MessageEvent).data) as LogEntry
        } catch {
          return
        }
        if (ready) enqueue(entry)
        else pending.push(entry)
      })

      source.onerror = () => {
        source?.close()
        source = null
        if (disposed) return
        void queryClient.invalidateQueries({ queryKey: qk.authState })
        const delay = Math.min(MAX_BACKOFF, MIN_BACKOFF * 2 ** attempt)
        attempt++
        setStatus("reconnecting")
        retryTimer = setTimeout(connect, delay)
      }
    }

    connect()

    return () => {
      disposed = true
      clearTimeout(retryTimer)
      clearTimeout(flushTimer)
      source?.close()
    }
  }, [level, append, queryClient])

  const setPaused = React.useCallback((next: boolean) => {
    pausedRef.current = next
    setPausedState(next)
    if (!next && bufferRef.current.length) {
      const items = bufferRef.current
      bufferRef.current = []
      setBuffered(0)
      setEntries((prev) => cap(prev.concat(items)))
    }
  }, [])

  const clear = React.useCallback(() => {
    bufferRef.current = []
    setBuffered(0)
    setEntries([])
  }, [])

  return { entries, status, paused, setPaused, buffered, clear, backlogError }
}
