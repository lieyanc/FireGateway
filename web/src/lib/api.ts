import type {
  ApiErrorBody,
  ApiToken,
  AuthState,
  BatchAction,
  BatchResult,
  ChangeSummary,
  Connection,
  ImportFormat,
  ImportMode,
  ImportPreview,
  LogEntry,
  LogLevelInfo,
  MetricRange,
  MetricSeries,
  Overview,
  Rule,
  RuleView,
  Settings,
  SettingsUpdateResult,
  TopItem,
  UpdateCheckResult,
  UpdateStatus,
  VersionInfo,
} from "@/lib/types"

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly field?: string
  /** Seconds, from the Retry-After header on 429 responses. */
  readonly retryAfter?: number

  constructor(
    status: number,
    code: string,
    message: string,
    field?: string,
    retryAfter?: number
  ) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.field = field
    this.retryAfter = retryAfter
  }
}

export function isApiError(error: unknown): error is ApiError {
  return error instanceof ApiError
}

type UnauthorizedListener = (path: string) => void
let unauthorizedListener: UnauthorizedListener | null = null

/** Registers the handler called whenever any request comes back 401. */
export function setUnauthorizedListener(listener: UnauthorizedListener | null) {
  unauthorizedListener = listener
}

type Query = Record<string, string | number | undefined | null>

function withQuery(path: string, query?: Query) {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== null && value !== "") {
      params.set(key, String(value))
    }
  }
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

async function toApiError(res: Response): Promise<ApiError> {
  let body: Partial<ApiErrorBody> = {}
  try {
    body = (await res.json()) as Partial<ApiErrorBody>
  } catch {
    // Non-JSON error (e.g. a proxy page); fall back to the status text.
  }
  const retryHeader = res.headers.get("Retry-After")
  const retryAfter = retryHeader ? Number(retryHeader) : undefined
  return new ApiError(
    res.status,
    body.error ?? (res.status === 401 ? "unauthorized" : "internal"),
    body.message || res.statusText || `HTTP ${res.status}`,
    body.field,
    Number.isFinite(retryAfter) ? retryAfter : undefined
  )
}

export async function request<T>(
  method: string,
  path: string,
  options: { body?: unknown; query?: Query; signal?: AbortSignal } = {}
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  const init: RequestInit = {
    method,
    headers,
    credentials: "same-origin",
    signal: options.signal,
  }
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json"
    init.body = JSON.stringify(options.body)
  }

  let res: Response
  try {
    res = await fetch(withQuery(path, options.query), init)
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw error
    }
    throw new ApiError(0, "network", "Network error")
  }

  if (!res.ok) {
    const error = await toApiError(res)
    if (res.status === 401) unauthorizedListener?.(path)
    throw error
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

const get = <T>(path: string, query?: Query, signal?: AbortSignal) =>
  request<T>("GET", path, { query, signal })
const post = <T>(path: string, body?: unknown, query?: Query) =>
  request<T>("POST", path, { body, query })
const put = <T>(path: string, body?: unknown) =>
  request<T>("PUT", path, { body })
const del = <T>(path: string, query?: Query) =>
  request<T>("DELETE", path, { query })

const enc = encodeURIComponent

export const api = {
  auth: {
    state: () => get<AuthState>("/api/auth/state"),
    setup: (body: { setupToken: string; username: string; password: string }) =>
      post<{ username: string }>("/api/auth/setup", body),
    login: (body: { username: string; password: string }) =>
      post<{ username: string }>("/api/auth/login", body),
    logout: () => post<void>("/api/auth/logout"),
    changePassword: (body: {
      currentPassword: string
      newPassword: string
      username?: string
    }) => post<{ username: string }>("/api/auth/password", body),
    tokens: () => get<{ items: ApiToken[] }>("/api/auth/tokens"),
    createToken: (name: string) =>
      post<{ token: string; item: ApiToken }>("/api/auth/tokens", { name }),
    revokeToken: (id: string) => del<void>(`/api/auth/tokens/${enc(id)}`),
  },
  system: {
    version: () => get<VersionInfo>("/api/version"),
    overview: () => get<Overview>("/api/overview"),
    restart: () => post<Record<string, never>>("/api/system/restart"),
    reloadConfig: () => post<ChangeSummary>("/api/config/reload"),
  },
  rules: {
    list: () => get<{ items: RuleView[] }>("/api/rules"),
    get: (id: string) => get<RuleView>(`/api/rules/${enc(id)}`),
    create: (rule: Rule) => post<RuleView>("/api/rules", rule),
    update: (id: string, rule: Rule) =>
      put<RuleView>(`/api/rules/${enc(id)}`, rule),
    remove: (id: string) => del<void>(`/api/rules/${enc(id)}`),
    enable: (id: string) => post<RuleView>(`/api/rules/${enc(id)}/enable`),
    disable: (id: string) => post<RuleView>(`/api/rules/${enc(id)}/disable`),
    restart: (id: string) => post<RuleView>(`/api/rules/${enc(id)}/restart`),
    batch: (action: BatchAction, ids: string[]) =>
      post<BatchResult>("/api/rules/batch", { action, ids }),
    export: () => get<{ forward: Rule[] }>("/api/rules/export"),
    import: (forward: Rule[], mode: ImportMode) =>
      post<ChangeSummary>("/api/rules/import", { forward, mode }),
    parseImport: (text: string, format: ImportFormat) =>
      post<ImportPreview>("/api/rules/import/parse", { text, format }),
    localRinetd: () => get<ImportPreview>("/api/rules/import/rinetd"),
  },
  connections: {
    list: (rule?: string, signal?: AbortSignal) =>
      get<{ items: Connection[] }>("/api/connections", { rule }, signal),
    close: (id: string) => del<void>(`/api/connections/${enc(id)}`),
    closeForRule: (rule: string) =>
      del<{ closed: number }>("/api/connections", { rule }),
  },
  metrics: {
    realtime: (rule?: string) =>
      get<MetricSeries>("/api/metrics/realtime", { rule }),
    history: (range: MetricRange, rule?: string) =>
      get<MetricSeries>("/api/metrics/history", { range, rule }),
    top: (range: MetricRange) =>
      get<{ items: TopItem[] }>("/api/metrics/top", { range }),
  },
  logs: {
    list: (query: { limit?: number; level?: string; q?: string } = {}) =>
      get<{ items: LogEntry[] }>("/api/logs", query),
    level: () => get<LogLevelInfo>("/api/log-level"),
    setLevel: (level: string) =>
      put<{ level: string }>("/api/log-level", { level }),
  },
  settings: {
    get: () => get<Settings>("/api/settings"),
    update: (patch: Partial<Settings>) =>
      put<SettingsUpdateResult>("/api/settings", patch),
  },
  update: {
    status: () => get<UpdateStatus>("/api/update/status"),
    check: () => post<UpdateCheckResult>("/api/update/check"),
    apply: () => post<Record<string, never>>("/api/update/apply"),
    dismiss: () => post<void>("/api/update/dismiss"),
  },
}

/** Triggers a browser download of a JSON value. */
export function downloadJson(filename: string, value: unknown) {
  const blob = new Blob([JSON.stringify(value, null, 2) + "\n"], {
    type: "application/json",
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
