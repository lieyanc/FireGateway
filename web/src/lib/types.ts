// Mirrors docs/api.md. Keep in sync with the server contract.

export type ApiErrorCode =
  | "bad_request"
  | "validation"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "quota_exceeded"
  | "rate_limited"
  | "not_initialized"
  | "already_initialized"
  | "unavailable"
  | "sync_pending"
  | "internal"
  | "not_supported"

export type ApiErrorBody = {
  error: ApiErrorCode | string
  message: string
  field?: string
}

// ---- Auth ----

export type Role = "admin" | "member"

export type AuthState = {
  initialized: boolean
  /** First start, or account recovery enabled with -reset-auth. */
  setupAvailable?: boolean
  authenticated: boolean
  userId?: string
  username?: string
  role?: Role
  tenantId?: string
  tenantName?: string
}

export type Account = { id: string; username: string }

export type ApiToken = {
  id: string
  name: string
  prefix: string
  createdAt: string
}

// ---- Users and tenants ----

export type User = {
  id: string
  username: string
  role: Role
  tenantId?: string
  disabled: boolean
  tokens: number
  createdAt: string
}

export type UserInput = {
  username?: string
  password?: string
  role?: Role
  tenantId?: string
  disabled?: boolean
}

/** Inclusive [from, to] span of local ports. */
export type PortRange = [number, number]

export type TenantQuota = {
  /** 0 or absent = unlimited. */
  maxRules?: number
  /** Up+down bytes per period across both nodes; 0 or absent = unlimited. */
  monthlyBytes?: number
  /** 1-28, day of month (UTC) a period starts; absent = 1. */
  resetDay?: number
}

export type TenantInput = {
  id: string
  name: string
  portRanges: PortRange[]
  quota: TenantQuota
}

export type Tenant = TenantInput & {
  usageResetAt?: string
  createdAt: string
  rules: number
  users: number
  usage: { since: string; bytes: number }
  suspended: boolean
}

// ---- Rules ----

export type RuleType = "tcp" | "udp"
export type RuleStatus = "active" | "inactive"
export type AclMode = "allow" | "deny"

export type RuleAcl = {
  mode: AclMode
  cidrs: string[]
}

export type RuleLimits = {
  maxConnections?: number
  maxConnectionsPerIp?: number
  bandwidth?: number
}

export type Rule = {
  id: string
  name: string
  type: RuleType
  status: RuleStatus
  localHost: string
  localPort?: number
  targetHost: string
  targetPort?: number
  localPortRange?: [number, number]
  targetPortRange?: [number, number]
  remark?: string
  /** Owning tenant id; empty = administrators only. */
  owner?: string
  acl?: RuleAcl
  limits?: RuleLimits
}

export type RuleState = "running" | "partial" | "error" | "stopped" | "suspended"

export type Counters = {
  activeConnections: number
  totalConnections: number
  bytesUp: number
  bytesDown: number
  rateUp: number
  rateDown: number
  errors: number
  rejected: number
  messages?: number
}

export type RuleFailure = { port: number; error: string }

export type RuleRuntime = Counters & {
  state: RuleState
  error?: string
  listeners: number
  failures?: RuleFailure[]
  startedAt?: string
}

export type RuleView = Rule & { runtime: RuleRuntime }

export type BatchAction = "enable" | "disable" | "delete"

export type BatchResult = {
  ok: string[]
  failed: { id: string; message: string }[]
}

export type ImportMode = "merge" | "replace"

/** Input format for the import parser; "json" covers FireGateway and FireProxy. */
export type ImportFormat = "auto" | "json" | "rinetd"

/** Something skipped or changed while converting; `line` for rinetd, `index` (1-based rule) for JSON. */
export type ImportWarning = {
  line?: number
  index?: number
  message: string
}

/** Converted rules awaiting review; ids may be empty (assigned on import). */
export type ImportPreview = {
  path?: string
  format: Exclude<ImportFormat, "auto">
  rules: Rule[]
  warnings: ImportWarning[]
}

export type ChangeSummary = {
  added: number
  updated: number
  removed: number
}

// ---- Connections ----

export type Connection = {
  id: string
  ruleId: string
  ruleName: string
  type: RuleType
  listen: string
  target: string
  client: string
  startedAt: string
  lastActive: string
  bytesUp: number
  bytesDown: number
}

// ---- DNS ----

export type DnsRuleRef = {
  id: string
  name: string
}

export type DnsEntry = {
  host: string
  addrs: string[]
  error?: string
  /** First lookup still in flight. */
  pending: boolean
  resolvedAt: string
  checkedAt: string
  nextAt: string
  rules: DnsRuleRef[]
}

export type DnsCache = {
  /** Seconds between refreshes of a successful answer. */
  ttl: number
  /** Seconds before a failed lookup is retried. */
  retryInterval: number
  items: DnsEntry[]
}

// ---- Logs ----

export type LogLevel = "ERROR" | "WARN" | "INFO" | "DEBUG" | "TRACE"

export type LogEntry = {
  seq: number
  time: string
  level: LogLevel
  msg: string
  attrs?: Record<string, string>
}

export type LogLevelInfo = {
  level: string
  levels: string[]
}

// ---- Metrics ----

export type MetricPoint = {
  t: number
  up: number
  down: number
  conns: number
  active: number
}

export type MetricSeries = {
  step: number
  points: MetricPoint[]
}

export type MetricRange = "1h" | "24h" | "7d" | "30d"

export type TopItem = {
  ruleId: string
  name: string
  up: number
  down: number
  conns: number
}

// ---- System ----

export type VersionInfo = {
  version: string
  commit: string
  buildTime: string
  updateChannel: string
  updateRepo: string
  updateSource: string
}

/** Host fields are only sent to administrators. */
export type Overview = {
  version: string
  commit: string
  buildTime: string
  startedAt: string
  uptime: number
  goVersion?: string
  os?: string
  arch?: string
  cpus?: number
  goroutines?: number
  memory?: { sys: number; heapAlloc: number; heapInuse: number }
  rules: {
    total: number
    active: number
    running: number
    partial: number
    error: number
    suspended?: number
  }
  totals: Counters
  configPath?: string
}

// ---- Settings ----

export type UpdateChannel = "stable" | "dev"
export type UpdateSource = "github" | "proxy"

export type Settings = {
  api: { host: string; port: number; enableCors: boolean }
  logging: {
    level: string
    enableConsole: boolean
    enableFile: boolean
    logDir: string
    maxFileSize: number
    maxFiles: number
  }
  update: {
    enabled: boolean
    channel: UpdateChannel
    checkInterval: number
    source: UpdateSource
    proxyBaseUrl: string
    repo: string
  }
  dataDir: string
}

export type SettingsUpdateResult = {
  settings: Settings
  restartRequired: boolean
}

// ---- Update ----

export type UpdateState =
  | "idle"
  | "checking"
  | "downloading"
  | "ready"
  | "waiting"
  | "applying"
  | "failed"

export type UpdateStatus = {
  state: UpdateState
  currentVersion: string
  latestVersion?: string
  isPrerelease: boolean
  progress?: number
  downloadProgress?: number
  error?: string
  lastCheck?: string
  releaseNotes?: string
}

export type UpdateCheckResult = {
  hasUpdate: boolean
  currentVersion: string
  latestVersion?: string
  isPrerelease: boolean
  releaseNotes?: string
  channel: string
}

// ---- Live events ----

export type RuleStats = Counters & { state: RuleState }

export type StatsEvent = {
  t: number
  totals: Counters
  rules: Record<string, RuleStats>
}

export type RulesEvent = {
  action: "created" | "updated" | "deleted" | "reloaded"
  id?: string
}

// ---- Node-local overrides and upstream-router HA ----
export type ClusterConnection = {
  id: string
  peerId: string
  peerPort: number
  peerToken: string
  initialWriter: string
  address: string
  peerAddress: string
  routerUrl: string
  username: string
  password: string
  caFile?: string
  redirects: string[]
  pollInterval: number
  failoverAfter: number
}
export type ClusterConnectionInput = {
  nodeId: string
  cluster: ClusterConnection | null
}
export type ClusterConnectionInfo = ClusterConnectionInput & {
  peerTokenConfigured: boolean
  routerPasswordConfigured: boolean
  restartRequired: boolean
  identityLocked: boolean
}
export type NodeConfig = {
  id: string
  ruleOverrides: Record<
    string,
    {
      localHost?: string
      targetHost?: string
      localPort?: number
      targetPort?: number
    }
  >
}
export type NodeInfo = {
  node: NodeConfig
  addresses: string[]
  rulesFile: string
  effectiveRules: Rule[]
}
export type ClusterRole = "primary" | "backup" | "standalone"
export type ClusterSync =
  "unpaired" | "synced" | "syncing" | "waiting" | "local_only" | "conflict"
export type ClusterIssue = { code: string; message?: string }
export type ClusterStatus = {
  enabled: boolean
  nodeId: string
  peerId?: string
  clusterId?: string
  initialWriter?: string
  paired: boolean
  /** The primary edits shared rules and keeps the router pointing at itself. */
  role: ClusterRole
  /** The router currently sends new connections to this node. */
  serving: boolean
  /** Every active rule is bound on this node. */
  ready: boolean
  sync: ClusterSync
  /** Shared rules can be edited through this node. */
  writable: boolean
  peer: {
    state: "online" | "offline" | "error" | "unknown" | ""
    role?: "primary" | "backup"
    ready: boolean
    serving: boolean
  }
  ingress: {
    state: "observed" | "switching" | "unavailable" | "unknown" | ""
    /** Node id the router points at; "" when the address is unknown. */
    owner?: string
    address?: string
  }
  issues: ClusterIssue[]
  details: {
    epoch: number
    peerEpoch: number
    revision: number
    peerRevision: number
    checksum: string
    peerChecksum?: string
    pendingUpdateId?: string
    lastSync?: string
  }
}
