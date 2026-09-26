import { useSyncExternalStore } from "react"

// One shared timer per interval, however many components tick with it.
type Ticker = { now: number; listeners: Set<() => void>; id?: number }
const tickers = new Map<number, Ticker>()

function getTicker(interval: number) {
  let ticker = tickers.get(interval)
  if (!ticker) {
    ticker = { now: Date.now(), listeners: new Set() }
    tickers.set(interval, ticker)
  }
  return ticker
}

function subscribeTo(interval: number) {
  return (listener: () => void) => {
    const ticker = getTicker(interval)
    ticker.listeners.add(listener)
    if (ticker.id === undefined) {
      ticker.now = Date.now()
      ticker.id = window.setInterval(() => {
        ticker.now = Date.now()
        for (const l of ticker.listeners) l()
      }, interval)
    }
    return () => {
      ticker.listeners.delete(listener)
      if (ticker.listeners.size === 0 && ticker.id !== undefined) {
        window.clearInterval(ticker.id)
        ticker.id = undefined
      }
    }
  }
}

const subscribers = new Map<number, (listener: () => void) => () => void>()

/** Current time in ms, re-rendering every `interval` ms while mounted. */
export function useNow(interval = 1000, enabled = true) {
  let subscribe = subscribers.get(interval)
  if (!subscribe) {
    subscribe = subscribeTo(interval)
    subscribers.set(interval, subscribe)
  }
  const getSnapshot = () => getTicker(interval).now
  return useSyncExternalStore(
    enabled ? subscribe : noopSubscribe,
    getSnapshot,
    getSnapshot
  )
}

function noopSubscribe() {
  return () => {}
}
