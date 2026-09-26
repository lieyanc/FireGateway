import { useSyncExternalStore } from "react"

import { api } from "@/lib/api"

// "Restart required" survives tab switches and page reloads for the session,
// and clears itself once the server has actually restarted.
const STORAGE_KEY = "fg-restart-required"
const listeners = new Set<() => void>()

function read() {
  try {
    return sessionStorage.getItem(STORAGE_KEY) === "1"
  } catch {
    return false
  }
}

let required = read()

export function setRestartRequired(value: boolean) {
  required = value
  try {
    if (value) sessionStorage.setItem(STORAGE_KEY, "1")
    else sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // Storage unavailable: keep the in-memory flag only.
  }
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function useRestartRequired() {
  return useSyncExternalStore(subscribe, () => required, () => required)
}

const POLL_MS = 1500
const TIMEOUT_MS = 60_000
// The old process may still answer for a moment after accepting the restart,
// so a success only counts once the server was seen down or this has passed.
const GRACE_MS = 5_000

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * Waits until the server answers /api/version again after a restart.
 * Resolves true when it is back, false on timeout.
 */
export async function waitForServer() {
  const started = Date.now()
  let sawDown = false
  while (Date.now() - started < TIMEOUT_MS) {
    await sleep(POLL_MS)
    try {
      await api.system.version()
      if (sawDown || Date.now() - started >= GRACE_MS) return true
    } catch {
      sawDown = true
    }
  }
  return false
}
