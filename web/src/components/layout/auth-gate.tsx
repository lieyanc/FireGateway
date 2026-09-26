import { Suspense, useEffect, useRef } from "react"
import { Navigate, Outlet, useLocation, useSearchParams } from "react-router"

import { FullPageSpinner, QueryError } from "@/components/common/query-state"
import { useI18n } from "@/i18n"
import { useAuthState } from "@/lib/queries"
import { installUnauthorizedHandler, PUBLIC_PATHS, safeNext } from "@/lib/session"

/**
 * Root route: routes the user to /setup, /login or the app depending on the
 * server's auth state.
 */
export function AuthGate() {
  const { t } = useI18n()
  const auth = useAuthState()
  const location = useLocation()
  const [params] = useSearchParams()

  const tRef = useRef(t)
  useEffect(() => {
    tRef.current = t
  }, [t])
  useEffect(() => {
    installUnauthorizedHandler(() => tRef.current("shell.sessionExpired"))
  }, [])

  if (auth.isPending) return <FullPageSpinner />
  if (auth.isError) {
    return (
      <div className="flex min-h-svh items-center justify-center p-4">
        <div className="w-full max-w-md">
          <QueryError error={auth.error} onRetry={() => void auth.refetch()} />
        </div>
      </div>
    )
  }

  const path = location.pathname
  const state = auth.data

  if (!state.initialized) {
    return path === "/setup" ? <Page /> : <Navigate to="/setup" replace />
  }
  if (!state.authenticated) {
    if (path === "/login") return <Page />
    const next = path === "/" || path === "/setup" ? "" : path + location.search
    return (
      <Navigate
        to={next ? `/login?next=${encodeURIComponent(next)}` : "/login"}
        replace
      />
    )
  }
  if (PUBLIC_PATHS.includes(path)) {
    return <Navigate to={safeNext(params.get("next"))} replace />
  }
  return <Page />
}

function Page() {
  return (
    <Suspense fallback={<FullPageSpinner />}>
      <Outlet />
    </Suspense>
  )
}
