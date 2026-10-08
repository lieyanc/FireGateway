import { toast } from "sonner"

import { setUnauthorizedListener } from "@/lib/api"
import { authStateQuery, qk, queryClient } from "@/lib/queries"
import { statsStore } from "@/lib/stats-store"
import type { Account, AuthState } from "@/lib/types"

/**
 * Marks the browser session as signed in (after login, setup or a password
 * change), then reloads the role and tenant; until they arrive for a new
 * session the UI treats the user as a member.
 */
export function signedIn(account: Account) {
  queryClient.setQueryData<AuthState>(qk.authState, (prev) =>
    prev?.userId === account.id
      ? { ...prev, authenticated: true, username: account.username }
      : {
          initialized: true,
          authenticated: true,
          userId: account.id,
          username: account.username,
        }
  )
  void queryClient.invalidateQueries({ queryKey: qk.authState })
}

/** Forgets everything cached for the signed-out session. */
export function signedOut() {
  queryClient.setQueryData<AuthState>(qk.authState, (prev) => ({
    initialized: prev?.initialized ?? true,
    authenticated: false,
  }))
  queryClient.removeQueries({
    predicate: (query) => query.queryKey[0] !== qk.authState[0],
  })
  queryClient.removeQueries({ queryKey: qk.tokens })
  statsStore.reset()
}

let checking = false

/**
 * Any 401 means the session may be gone: re-check the auth state and, if it
 * really expired, drop cached data so the auth gate sends the user to /login.
 */
export function installUnauthorizedHandler(sessionExpiredMessage: () => string) {
  setUnauthorizedListener((path) => {
    if (path.startsWith("/api/auth/state") || checking) return
    const prev = queryClient.getQueryData<AuthState>(qk.authState)
    if (!prev?.authenticated) return
    checking = true
    void queryClient
      .fetchQuery({ ...authStateQuery, staleTime: 0 })
      .then((state) => {
        if (!state.authenticated) {
          toast.warning(sessionExpiredMessage())
          signedOut()
        }
      })
      .catch(() => {})
      .finally(() => {
        checking = false
      })
  })
}

export const PUBLIC_PATHS = ["/login", "/setup"]

/** Only same-app relative paths are accepted as a post-login target. */
export function safeNext(next: string | null) {
  if (!next || !next.startsWith("/") || next.startsWith("//")) return "/"
  if (PUBLIC_PATHS.some((p) => next === p || next.startsWith(p + "?"))) return "/"
  return next
}
