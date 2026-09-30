// Mirrors docs/api.md. Keep in sync with the server contract.

export type ApiErrorCode =
  | "bad_request"
  | "validation"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "rate_limited"
  | "not_initialized"
  | "already_initialized"
  | "internal"
  | "not_supported"

export type ApiErrorBody = {
  error: ApiErrorCode | string
  message: string
  field?: string
}

// ---- Auth ----

export type AuthState = {
  initialized: boolean
  authenticated: boolean
  username?: string
}

export type ApiToken = {
  id: string
  name: string
  prefix: string
  createdAt: string
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
  acl?: RuleAcl
  limits?: RuleLimits
}

export type RuleState = "running" | "partial" | "error" | "stopped"

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

export type Overview = {
  version: string
  commit: string
  buildTime: string
  startedAt: string
  uptime: number
  goVersion: string
  os: string
  arch: string
  cpus: number
  goroutines: number
  memory: { sys: number; heapAlloc: number; heapInuse: number }
  rules: {
    total: number
    active: number
    running: number
    partial: number
    error: number
  }
  totals: Counters
  configPath: string
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
  "idle" | "checking" | "downloading" | "ready" | "waiting" | "applying" | "failed"

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
export type ClusterStatus = {
  enabled: boolean
  nodeId: string
  clusterId: string
  role: "standalone" | "active" | "standby" | "disconnected"
  owner: string
  address: string
  desiredRevision: number
  appliedRevision: number
  checksum: string
  error?: string
  syncError?: string
  lastSync?: string
  paired: boolean
  epoch: number
  writer: string
  peerEpoch: number
  takenOver: boolean
  configRole: "writer" | "replica" | "read_only"
  replicationState: "waiting" | "local_only" | "pending" | "synced" | "unpaired"
  peerState: "unknown" | "online" | "offline" | "error"
  upstreamState:
    "unknown" | "observed" | "applied" | "unavailable" | "switch_pending"
  peerRevision: number
  peerChecksum: string
  peerPreparedChecksum: string
  localPreparedChecksum: string
  pendingUpdateId?: string
}
