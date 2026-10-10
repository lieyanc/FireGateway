import type {
  ApiErrorBody,
  NodeInfo,
  NodeConfig,
  ClusterStatus,
  ClusterConnectionInfo,
  ClusterConnectionInput,
  Account,
  ApiToken,
  AuthState,
  BatchAction,
  BatchResult,
  ChangeSummary,
  Connection,
  DnsCache,
  DnsEntry,
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
  Tenant,
  TenantInput,
  TopItem,
  UpdateCheckResult,
  UpdateStatus,
  User,
  UserInput,
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

let ruleVersion = ""
function sharedRuleMutation(method: string, path: string) {
  return (
    method !== "GET" &&
    (path === "/api/rules" ||
      path === "/api/rules/import" ||
      path === "/api/rules/batch" ||
      (path.startsWith("/api/rules/") &&
        !path.startsWith("/api/rules/import/") &&
        !path.endsWith("/restart")))
  )
}

/** Writes to state the cluster replicates; each carries an Idempotency-Key. */
function replicatedWrite(method: string, path: string) {
  return (
    sharedRuleMutation(method, path) ||
    (method !== "GET" &&
      (path === "/api/auth/setup" ||
        path === "/api/auth/password" ||
        path.startsWith("/api/auth/tokens") ||
        path.startsWith("/api/users") ||
        path.startsWith("/api/tenants")))
  )
}

function newRequestId() {
  return Array.from(crypto.getRandomValues(new Uint8Array(24)), (v) =>
    v.toString(16).padStart(2, "0")
  ).join("")
}

export async function request<T>(
  method: string,
  path: string,
  options: {
    body?: unknown
    query?: Query
    signal?: AbortSignal
    requestId?: string
  } = {}
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  if (sharedRuleMutation(method, path)) {
    if (!ruleVersion) {
      const snapshot = await fetch("/api/rules", {
        credentials: "same-origin",
        signal: options.signal,
      })
      if (!snapshot.ok) throw await toApiError(snapshot)
      ruleVersion = snapshot.headers.get("ETag") ?? ""
      await snapshot.text()
    }
    if (ruleVersion) headers["If-Match"] = ruleVersion
  }
  if (replicatedWrite(method, path)) {
    headers["Idempotency-Key"] = options.requestId ?? newRequestId()
  }
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

  const etag = res.headers.get("ETag")
  if (etag && (path === "/api/rules" || sharedRuleMutation(method, path)))
    ruleVersion = etag
  if (!res.ok) {
    if (res.status === 409) ruleVersion = ""
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
  cluster: {
    connection: () => get<ClusterConnectionInfo>("/api/cluster/config"),
    saveConnection: (body: ClusterConnectionInput) =>
      put<ClusterConnectionInfo>("/api/cluster/config", body),
    testPeer: () =>
      post<{ connected: boolean; nodeId: string }>("/api/cluster/test-peer"),
    status: () => get<ClusterStatus>("/api/cluster"),
    bootstrap: () => post<{ initialized: boolean }>("/api/cluster/bootstrap"),
    switchover: (target: string) =>
      post<ClusterStatus>("/api/cluster/switchover", { target }),
    promote: () =>
      post<ClusterStatus>("/api/cluster/promote", { confirm: true }),
    rejoin: () => post<ClusterStatus>("/api/cluster/rejoin", { confirm: true }),
  },
  node: {
    get: () => get<NodeInfo>("/api/node"),
    update: (node: NodeConfig) => put<NodeInfo>("/api/node", node),
  },
  auth: {
    state: () => get<AuthState>("/api/auth/state"),
    setup: (body: { setupToken: string; username: string; password: string }) =>
      post<Account>("/api/auth/setup", body),
    login: (body: { username: string; password: string }) =>
      post<Account>("/api/auth/login", body),
    logout: () => post<void>("/api/auth/logout"),
    changePassword: (body: {
      currentPassword: string
      newPassword: string
      username?: string
    }) => post<Account>("/api/auth/password", body),
    tokens: () => get<{ items: ApiToken[] }>("/api/auth/tokens"),
    createToken: (name: string) =>
      post<{ token: string; item: ApiToken }>("/api/auth/tokens", { name }),
    revokeToken: (id: string) => del<void>(`/api/auth/tokens/${enc(id)}`),
  },
  users: {
    list: () => get<{ items: User[] }>("/api/users"),
    create: (body: UserInput) => post<User>("/api/users", body),
    update: (id: string, body: UserInput) =>
      put<User>(`/api/users/${enc(id)}`, body),
    remove: (id: string) => del<void>(`/api/users/${enc(id)}`),
  },
  tenants: {
    list: () => get<{ items: Tenant[] }>("/api/tenants"),
    /** The caller's own tenant (members only). */
    own: () => get<Tenant>("/api/tenant"),
    create: (body: TenantInput) => post<Tenant>("/api/tenants", body),
    update: (id: string, body: Omit<TenantInput, "id">) =>
      put<Tenant>(`/api/tenants/${enc(id)}`, body),
    remove: (id: string) => del<void>(`/api/tenants/${enc(id)}`),
    resetUsage: (id: string) =>
      post<Tenant>(`/api/tenants/${enc(id)}/usage/reset`),
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
  dns: {
    list: () => get<DnsCache>("/api/dns"),
    /** Re-resolves one host, or every cached host when omitted. */
    refresh: (host?: string) =>
      post<{ items: DnsEntry[] }>("/api/dns/refresh", { host }),
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
    apply: (force = false) =>
      post<Record<string, never>>("/api/update/apply", { force }),
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
