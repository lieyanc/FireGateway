# FireGateway HTTP API

## Peer replication and router failover

These management endpoints require ordinary user authentication. Node replication uses a separate credential and never copies user or router credentials.

- `GET /api/cluster`: traffic `role` (`standalone`, `active`, `standby`, `disconnected`), `owner`, `address`, `configRole` (`writer`, `replica`, `read_only`), `writer`, `paired`, `epoch`, `desiredRevision`, `checksum`, `appliedRevision`, `localPreparedChecksum`, `peerState`, `peerEpoch`, `peerRevision`, `peerChecksum`, `peerPreparedChecksum`, `replicationState`, `pendingUpdateId?`, `upstreamState`, `takenOver`, `error?`, `syncError?`, `lastSync?`. `lastSync` records successful peer contact; readiness and durable replication are separate. Compare checksums, not revision numbers alone across writer epochs.
- `GET /api/cluster/config`: saved node-local connection settings `{nodeId, cluster, peerTokenConfigured, routerPasswordConfigured, restartRequired, identityLocked}`. `cluster` is null in standalone mode; otherwise it contains the fields in the deployment guide. `peerToken` and router `password` are always returned as empty strings. Responses are not cacheable.
- `PUT /api/cluster/config` with `{nodeId, cluster}`: validate and atomically save connection settings for the next process restart. Blank or omitted secret fields retain the latest saved secrets; initial setup requires both credentials. This endpoint validates local CA files without contacting either device. The running controller, shared rules, and local rule overrides remain unchanged, and subsequent local settings writes preserve pending connection changes. Returns the same redacted shape as GET. An enabled cluster cannot be disabled or assigned a different node ID, peer ID, cluster ID, or initial writer through this endpoint. A pending first activation can be cancelled with `cluster: null` before restarting.
- `POST /api/cluster/test-peer`: use the saved connection (including pending settings) to send one authenticated peer heartbeat; returns `{connected: true, nodeId}` or 502 on connection/authentication/identity failure. Does not save, pair, replicate rules, apply pending settings or contact OpenWrt. The peer must already be running matching settings.
- `POST /api/cluster/bootstrap`: pair from the configured initial writer; returns `{initialized: true}`. The peer must be reachable and empty or have matching initial rules. Does not contact the router.
- `POST /api/cluster/transfer`: freeze and durably transfer configuration write authority to the paired node. Does not move ingress.
- `POST /api/cluster/promote` with `{fencedPeer: true}`: explicitly promote this node after stopping/isolating the previous writer. Archives old state and enables clearly marked local-only writes until rejoin.
- `POST /api/cluster/rejoin` with `{archiveLocal: true}`: archive this node's state and adopt the peer writer's committed snapshot. Re-establish matching state before disabling degraded mode.
- `POST /api/cluster/retry-switch`: retry an unconfirmed upstream change after checking the device's current state.
- `POST /api/cluster/rearm`: on B, rearm automatic takeover after restoring peer replication and manually returning ingress to the initial primary A.
- `GET /api/cluster/transactions/{id}`: inspect a pending transaction or a retained request receipt. IDs can be transaction IDs (while pending) or the original `Idempotency-Key`. Recent 128 request results are retained; 404 requires refreshing current state, not assuming the operation never happened.
- `GET /api/cluster/snapshot`: download the committed shared snapshot without credentials or local overrides.
- `GET /api/node`: `{node: {id, ruleOverrides}, addresses, effectiveRules, rulesFile}`. `PUT /api/node` updates local overrides only, keyed by rule ID with optional `localHost`, `targetHost`, `localPort`, `targetPort`. Unspecified fields inherit shared values. Node identity is immutable while running; single-port overrides do not apply to ranges. OpenWrt mode requires matching ingress ports across nodes.
- `GET /ready`: public, 200 only while forwarding is allowed and all active rules are listening; otherwise 503. Does not probe target application health.

Cluster shared-rule mutations require `If-Match: "<checksum>"` (from the `GET /api/rules` ETag) and a stable `Idempotency-Key` of 8–128 characters. The web UI sends them automatically. Standby requests are authenticated locally and forwarded to the unique configuration writer. Stale versions or reused IDs with different content return 409. Known disconnected pairs are read-only; confirmed promotion allows local-only writes.

Ordinary successful writes have durably committed on both nodes. `X-Rule-Durability` is `replicated` or `local_only`; it does not mean listeners are ready. A lost commit confirmation returns 503 with `error: "sync_pending"` and an update identifier; the write may already exist on the peer. Query/retry with the same request ID and original precondition. Never blindly resubmit using a fresh ID after an uncertain result. An outcome persisted before an HTTP receipt could be saved is reported as already committed; refresh rather than executing it again.

`POST /internal/ha/v1` is the protocol-1 node endpoint (`heartbeat`, `snapshot`, `prepare`, `commit`, `handoff-prepare`, `handoff-commit`, `resume`, `mutate`). It accepts only the configured peer bearer token, node identity, cluster identity, receiver identity and matching pair topology. User cookies/API tokens do not authenticate this endpoint. Mutations carry pair identity, writer epoch, parent checksum and transaction identity; arbitrary newer snapshots are not installed.

Legacy `forward` imports/exports remain supported. `rulesFile` is independent of local settings; clustered state is recovered from the adjacent `.ha.json` atomic checkpoint. On first cluster activation, startup adopts existing standalone schema-1 rules into the configured cluster if no HA checkpoint exists. Existing cluster identities cannot be relabelled. See [deployment and recovery](ha-openwrt.md).

The web UI and the HTTP API share one listener (`api.host:api.port`, default `127.0.0.1:8080`). The UI is served from `/`; the API lives under `/api`.

Conventions:

- JSON in and out, `camelCase` keys. Timestamps are RFC 3339 strings unless noted (`t` fields in metric points are unix seconds).
- Byte counters are integers in bytes; rates are bytes per second.
- Errors: non-2xx status with `{"error": "<code>", "message": "<human readable>", "field"?: "<field path>"}`. Codes used: `bad_request`, `validation`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `rate_limited`, `not_initialized`, `already_initialized`, `internal`, `not_supported`.

## Authentication

Single admin account. Two ways to authenticate:

1. **Session cookie** `fg_session` (HttpOnly, SameSite=Lax), set by `login`/`setup`. For cookie-authenticated `POST/PUT/PATCH/DELETE` requests, a present `Origin` header must match the request host (CSRF defense). Browsers send it automatically; the UI must use same-origin `fetch` with `credentials: "same-origin"`.
2. **API token** `Authorization: Bearer fgw_...` created in the UI. Tokens have full admin rights.

When no admin exists yet (`initialized=false`), a one-time **setup token** is printed to the server log on startup; `POST /api/auth/setup` requires it.

Public endpoints (no auth): `GET /health`, `GET /api/version`, `GET /api/auth/state`, `POST /api/auth/login`, `POST /api/auth/setup`. Everything else under `/api` returns `401 unauthorized` without valid credentials.

| Method & path | Body | Response |
|---|---|---|
| `GET /api/auth/state` | – | `{initialized: bool, authenticated: bool, username?: string}` |
| `POST /api/auth/setup` | `{setupToken, username, password}` | `200 {username}` + cookie. `409 already_initialized`, `403 forbidden` (bad token) |
| `POST /api/auth/login` | `{username, password}` | `200 {username}` + cookie. `401`, `429 rate_limited` (after repeated failures; `Retry-After` header) |
| `POST /api/auth/logout` | – | `204` |
| `POST /api/auth/password` | `{currentPassword, newPassword, username?}` | `200 {username}`. Invalidates all other sessions; re-issues the caller's cookie |
| `GET /api/auth/tokens` | – | `{items: ApiToken[]}` |
| `POST /api/auth/tokens` | `{name}` | `201 {token: string, item: ApiToken}` — `token` is shown only once |
| `DELETE /api/auth/tokens/{id}` | – | `204` |

Password rules: 8–128 characters. Username: 1–64 characters.

```ts
type ApiToken = { id: string; name: string; prefix: string; createdAt: string }
```

## Types

```ts
type RuleType = "tcp" | "udp"
type RuleStatus = "active" | "inactive"

type Rule = {
  id: string                 // generated on create when empty; numeric ids from old configs arrive as strings
  name: string
  type: RuleType
  status: RuleStatus         // "active" rules are started
  localHost: string          // listen address, e.g. "0.0.0.0", "::", "192.168.1.3"
  localPort?: number         // single mapping ...
  targetHost: string
  targetPort?: number
  localPortRange?: [number, number]   // ... or a one-to-one port range; same length on both sides
  targetPortRange?: [number, number]
  remark?: string
  acl?: {
    mode: "allow" | "deny"   // allow: only listed sources; deny: all except listed
    cidrs: string[]          // IPs or CIDRs, IPv4/IPv6, e.g. "10.0.0.0/8", "203.0.113.7"
  }
  limits?: {
    maxConnections?: number       // concurrent TCP connections / UDP sessions for the whole rule; 0 = unlimited
    maxConnectionsPerIp?: number  // concurrent per source IP; 0 = unlimited
    bandwidth?: number            // bytes/s per direction, shared by the whole rule; 0 = unlimited
  }
}

type RuleState = "running" | "partial" | "error" | "stopped"
// running: all listeners up; partial: some ports failed; error: none up; stopped: status inactive

type Counters = {
  activeConnections: number
  totalConnections: number
  bytesUp: number            // client -> target
  bytesDown: number          // target -> client
  rateUp: number             // bytes/s, 1s window
  rateDown: number
  errors: number
  rejected: number           // refused by ACL or limits
  messages?: number          // UDP datagrams forwarded (up)
}

type RuleView = Rule & {
  runtime: Counters & {
    state: RuleState
    error?: string                                     // why an active rule is not running at all (e.g. invalid legacy entry)
    listeners: number                                  // listeners currently up
    failures?: { port: number; error: string }[]      // ports that failed to start
    startedAt?: string
  }
}

type Connection = {
  id: string
  ruleId: string
  ruleName: string
  type: RuleType
  listen: string       // "0.0.0.0:3333"
  target: string       // "192.168.1.123:1234"
  client: string       // "203.0.113.7:51234"
  startedAt: string
  lastActive: string
  bytesUp: number
  bytesDown: number
}

type LogEntry = {
  seq: number                                          // monotonically increasing
  time: string
  level: "ERROR" | "WARN" | "INFO" | "DEBUG" | "TRACE"
  msg: string
  attrs?: Record<string, string>                       // e.g. ruleId, proxy, client, err
}

type MetricPoint = {
  t: number            // unix seconds, start of bucket
  up: number           // bytes during bucket
  down: number
  conns: number        // new connections/sessions during bucket
  active: number       // peak concurrent during bucket
}
```

## System

| Method & path | Response |
|---|---|
| `GET /health` | legacy health check: `200 {status:"healthy",...}` when at least one listener is up, else `503` |
| `GET /api/version` | `{version, commit, buildTime, updateChannel, updateRepo, updateSource}` |
| `GET /api/overview` | `Overview` |
| `POST /api/system/restart` | `202 {}` — re-execs the process (Unix); `501 not_supported` on Windows |

```ts
type Overview = {
  version: string; commit: string; buildTime: string
  startedAt: string; uptime: number /* seconds */
  goVersion: string; os: string; arch: string; cpus: number; goroutines: number
  memory: { sys: number; heapAlloc: number; heapInuse: number }   // bytes
  rules: { total: number; active: number; running: number; partial: number; error: number }
  totals: Counters                                                 // across all rules
  configPath: string
}
```

## Rules

| Method & path | Body | Response |
|---|---|---|
| `GET /api/rules` | – | `{items: RuleView[]}` (config order) |
| `GET /api/rules/{id}` | – | `RuleView` |
| `POST /api/rules` | `Rule` (id optional) | `201 RuleView` |
| `PUT /api/rules/{id}` | `Rule` (full replacement; `id` in body ignored) | `RuleView` |
| `DELETE /api/rules/{id}` | – | `204` |
| `POST /api/rules/{id}/enable` | – | `RuleView` |
| `POST /api/rules/{id}/disable` | – | `RuleView` (closes its listeners and live connections) |
| `POST /api/rules/{id}/restart` | – | `RuleView` (re-binds listeners, e.g. after a port conflict cleared) |
| `POST /api/rules/batch` | `{action: "enable"\|"disable"\|"delete", ids: string[]}` | `{ok: string[], failed: {id, message}[]}` |
| `GET /api/rules/export` | – | `{forward: Rule[]}` with `Content-Disposition: attachment` |
| `POST /api/rules/import` | `{forward: Rule[], mode: "merge"\|"replace"}` | `{added, updated, removed: number}` — merge upserts by id; replace drops rules not in the payload |
| `POST /api/config/reload` | – | `{added, updated, removed}` — re-reads node settings and, in standalone mode, the versioned rules snapshot |

Validation failures return `400 validation` with `field` set (e.g. `"localPort"`, `"acl.cidrs[2]"`, `"localPortRange"`). Duplicate id → `409 conflict`. Listen conflicts with another active rule (same type, overlapping host:port) → `409 conflict`. A rule whose ports fail to bind at the OS level is still saved; its `runtime.state` is `error`/`partial` with `failures`.

Changes are hot-applied: editing only `name`, `remark`, `acl`, `limits` or the target keeps listeners and live connections; changing type, listen host or ports re-binds listeners (and closes that rule's connections). Every successful rule mutation is persisted to the versioned rules file atomically; cluster mode confirms a peer transaction first. Batch changes publish one snapshot for all accepted items. Replication never calls the upstream router.

## Connections

| Method & path | Response |
|---|---|
| `GET /api/connections?rule={id}` | `{items: Connection[]}` — live TCP connections and UDP sessions, newest first; `rule` optional |
| `DELETE /api/connections/{id}` | `204` / `404` |
| `DELETE /api/connections?rule={id}` | `{closed: number}` — closes all of a rule's connections (`rule` required) |

## Metrics

| Method & path | Response |
|---|---|
| `GET /api/metrics/realtime?rule={id}` | `{step: 1, points: MetricPoint[]}` — last 300 seconds at 1s resolution, for pre-filling live charts. `rule` optional (omitted = all rules) |
| `GET /api/metrics/history?range={1h\|24h\|7d\|30d}&rule={id}` | `{step: number, points: MetricPoint[]}` — `1h`/`24h` use 60s buckets, `7d`/`30d` use 3600s buckets. Zero buckets are included so the series is continuous |
| `GET /api/metrics/top?range={1h\|24h\|7d\|30d}` | `{items: {ruleId, name, up, down, conns}[]}` sorted by `up+down` desc |

History is persisted in `dataDir` and survives restarts.

## Live events (Server-Sent Events)

`GET /api/events` — `text/event-stream`, authenticated like any other endpoint (cookie works with `EventSource`). Sends a comment ping every 15s.

| Event | Data | When |
|---|---|---|
| `stats` | `{t: number, totals: Counters, rules: Record<ruleId, Counters & {state: RuleState}>}` | every second |
| `rules` | `{action: "created"\|"updated"\|"deleted"\|"reloaded", id?: string}` | after any rule change — the UI should refetch rule queries |

`GET /api/logs/stream?level={error\|warn\|info\|debug\|trace}` — SSE, event `log` with a `LogEntry` per record at or above `level` (default: current log level). On connect, send nothing historical; use `GET /api/logs` for backlog.

## Logs

| Method & path | Body | Response |
|---|---|---|
| `GET /api/logs?limit={n}&level={lvl}&q={text}` | – | `{items: LogEntry[]}` oldest→newest, from an in-memory ring (last 2000 records). `limit` default 500 |
| `GET /api/log-level` | – | `{level: string, levels: string[]}` |
| `PUT /api/log-level` | `{level}` | `{level}` — applies immediately, persisted |

## Settings

| Method & path | Body | Response |
|---|---|---|
| `GET /api/settings` | – | `Settings` |
| `PUT /api/settings` | `Partial<Settings>` (each section is replaced as a whole when present) | `{settings: Settings, restartRequired: boolean}` |

```ts
type Settings = {
  api: { host: string; port: number; enableCors: boolean }                       // restart required
  logging: { level: string; enableConsole: boolean; enableFile: boolean;
             logDir: string; maxFileSize: number; maxFiles: number }             // level live, rest restart
  update: { enabled: boolean; channel: "stable" | "dev"; checkInterval: number;  // live
            source: "github" | "proxy"; proxyBaseUrl: string; repo: string }
  dataDir: string                                                                // restart required
}
```

## Self-update (OTA)

| Method & path | Response |
|---|---|
| `GET /api/update/status` | `UpdateStatus` |
| `POST /api/update/check` | `{hasUpdate, currentVersion, latestVersion?, isPrerelease, releaseNotes?, channel}` |
| `POST /api/update/apply` | `202 {}` — optional body `{force?: boolean}`; if `state=="ready"` applies the pending binary, otherwise starts check+download(+apply on stable). `force: true` applies a ready update immediately or interrupts an existing idle wait; other states return `409`. |
| `POST /api/update/dismiss` | `204` — drops a downloaded pending update |

```ts
type UpdateStatus = {
  state: "idle" | "checking" | "downloading" | "ready" | "waiting" | "applying" | "failed"
  currentVersion: string
  latestVersion?: string
  isPrerelease: boolean
  progress?: number            // 0-100 overall
  downloadProgress?: number    // 0-100 while downloading
  error?: string
  lastCheck?: string
  releaseNotes?: string
}
```

Applying restarts the process; live connections are dropped. By default the updater enters `waiting` and waits up to 10 minutes for active connections to drain, then enters `applying`. Send `{"force":true}` to skip or interrupt that wait for either channel; the downloaded binary must already be verified. An empty body or `{"force":false}` preserves the default behavior (a duplicate non-forced apply during `waiting` returns `409`). Force applies to the current update only and does not change automatic update settings. After a restart the UI should detect `currentVersion` changed and reload the page.
