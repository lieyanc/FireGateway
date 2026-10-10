# FireGateway HTTP API

## Peer replication and router failover

These management endpoints require an administrator. Node replication does not use the management API at all: it runs over a private mutual-TLS link on `cluster.peerPort`. The shared state carries user accounts, tenants and API token hashes, so both nodes authenticate the same users. Replication never copies the peer token or the router credentials.

- `GET /api/cluster`: `{enabled, nodeId, peerId, clusterId, initialWriter, paired, role, serving, ready, sync, writable, peer, ingress, issues, details}`.
  - `role` is `primary`, `backup` or `standalone`. The primary is the only node that saves shared state, and it keeps the router pointing at itself.
  - `serving` is true while the router's DNAT points at this node, which opens the forwarding gate. `ready` means every active rule is bound here.
  - `sync` is `unpaired`, `synced`, `syncing`, `waiting`, `local_only` (the primary saves alone) or `conflict` (both nodes saved alone). `writable` reports whether shared state can be edited through this node.
  - `peer` is `{state, role?, ready, serving}`. `state` is `online`, `offline`, `error` (reachable but misconfigured, which never triggers takeover) or `unknown`.
  - `ingress` is `{state, owner?, address?}`. `state` is `observed`, `switching` (the primary is pointing the router at itself), `unavailable` (the router API failed) or `unknown`.
  - `issues` is a list of `{code, message?}` with stable codes: `unpaired`, `peer_offline`, `peer_error`, `sync_error`, `conflict`, `not_ready`, `peer_not_ready`, `router_unavailable`, `ingress_error`, `ingress_unknown`, `ingress_elsewhere`, `local_only`, `read_only`.
  - `details` holds `{epoch, peerEpoch, revision, peerRevision, checksum, peerChecksum?, pendingUpdateId?, lastSync?}`. Compare checksums, not revision numbers alone, across epochs.
- `GET /api/cluster/config`: saved node-local connection settings `{nodeId, cluster, peerTokenConfigured, routerPasswordConfigured, restartRequired, identityLocked}`. `cluster` is null in standalone mode; otherwise it contains the fields in the deployment guide. `peerToken` and router `password` are always returned as empty strings. Responses are not cacheable.
- `PUT /api/cluster/config` with `{nodeId, cluster}`: validate and atomically save connection settings for the next process restart. Blank or omitted secret fields retain the latest saved secrets; initial setup requires both credentials. This endpoint validates local CA files without contacting either device. The running controller, shared rules, and local rule overrides remain unchanged, and subsequent local settings writes preserve pending connection changes. Returns the same redacted shape as GET. An enabled cluster cannot be disabled or assigned a different node ID, peer ID, cluster ID, or initial writer through this endpoint. A pending first activation can be cancelled with `cluster: null` before restarting.
- `POST /api/cluster/test-peer`: use the saved connection (including pending settings) to send one authenticated peer heartbeat; returns `{connected: true, nodeId}` or 502 on connection/authentication/identity failure. Does not save, pair, replicate rules, apply pending settings or contact OpenWrt. The peer must already be running matching settings.
- `POST /api/cluster/bootstrap`: pair from the configured initial writer; returns `{initialized: true}`. The peer must be reachable and empty or have matching initial rules. Does not contact the router.
- `POST /api/cluster/switchover` with `{target}`: make `target` (this node or its peer) the primary. Either node can request it. The primary role moves first, then the new primary points the router at itself. Requires the peer online, both nodes on the same committed state with nothing pending, and the new primary able to bind its listeners.
- `POST /api/cluster/promote` with `{confirm: true}`: let this node save shared state alone while the peer is unreachable. Refused while the peer is online; use switchover instead. A backup becomes primary at a new epoch, which fences the old primary, and takes over ingress. Writes report `local_only` until the peer is back and has followed.
- `POST /api/cluster/rejoin` with `{confirm: true}`: archive this node's state and adopt the peer primary's committed snapshot. This is needed only for `conflict`. A node that did not save alone follows the newer primary automatically.
- `GET /api/cluster/transactions/{id}`: inspect a pending transaction or a retained request receipt. IDs can be transaction IDs (while pending) or the original `Idempotency-Key`. Recent 128 request results are retained; 404 requires refreshing current state, not assuming the operation never happened.
- `GET /api/cluster/snapshot`: download the committed shared snapshot without credentials or local overrides.
- `GET /api/node`: `{node: {id, ruleOverrides}, addresses, effectiveRules, rulesFile}`. `PUT /api/node` updates local overrides only, keyed by rule ID with optional `localHost`, `targetHost`, `localPort`, `targetPort`. Unspecified fields inherit shared values. Node identity is immutable while running; single-port overrides do not apply to ranges. OpenWrt mode requires matching ingress ports across nodes.
- `GET /ready`: public, 200 only while forwarding is allowed and all active rules are listening; otherwise 503. Does not probe target application health.

In a cluster, writes to shared state run on the primary. Shared state covers rules, accounts, API tokens, users and tenants.

- Rule mutations require `If-Match` with the ETag from `GET /api/rules`. The ETag is a version of the rules the caller can see, plus the caller's tenant record. Changes to another tenant's rules therefore do not invalidate it, and it reveals nothing about them.
- Rule mutations also need a stable `Idempotency-Key` of 8–128 characters.
- Account, token, user and tenant writes take no precondition. When their `Idempotency-Key` is missing, the server generates one.

The web UI sends these headers automatically. Requests on the backup are authenticated locally and forwarded to the primary. Stale versions or reused IDs with different content return 409. While the peer is unreachable the primary is read-only, unless it was promoted or took over, which allows local-only writes.

Ordinary successful writes have durably committed on both nodes. `X-Rule-Durability` is `replicated` or `local_only`; it does not mean listeners are ready. A lost commit confirmation returns 503 with `error: "sync_pending"` and an update identifier; the write may already exist on the peer. Query/retry with the same request ID and original precondition. Never blindly resubmit using a fresh ID after an uncertain result. An outcome persisted before an HTTP receipt could be saved is reported as already committed; refresh rather than executing it again.

The node link is not part of this HTTP API. Each node serves it on `cluster.address:cluster.peerPort` (default 9091) with mutual TLS 1.3: both nodes derive the same private CA from `peerToken` and the cluster ID, and each presents a certificate naming its node ID. Requests use protocol 4 (`heartbeat`, `snapshot`, `prepare`, `commit`, `handoff-prepare`, `handoff-commit`, `resume`, `yield`, `claim`, `mutate`), so both nodes must run releases with the same protocol. Requests must also match the cluster identity, sender and receiver node IDs, and pair topology. User cookies and API tokens are never accepted there. Mutations carry pair identity, writer epoch, parent checksum, transaction identity, and the acting user's ID. The writer re-checks that user's current role and tenant before executing. Arbitrary newer snapshots are not installed.

Legacy `forward` imports/exports remain supported. `rulesFile` is independent of local settings; clustered state is recovered from the adjacent `.ha.json` atomic checkpoint. On first cluster activation, startup adopts existing standalone schema-1 rules into the configured cluster if no HA checkpoint exists. Existing cluster identities cannot be relabelled. See [deployment and recovery](ha-openwrt.md).

The web UI and the HTTP API share one listener (`api.host:api.port`, default `127.0.0.1:8080`). The UI is served from `/`; the API lives under `/api`.

Conventions:

- JSON in and out, `camelCase` keys. Timestamps are RFC 3339 strings unless noted (`t` fields in metric points are unix seconds).
- Byte counters are integers in bytes; rates are bytes per second.
- Errors: non-2xx status with `{"error": "<code>", "message": "<human readable>", "field"?: "<field path>"}`. Codes used: `bad_request`, `validation`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `quota_exceeded`, `rate_limited`, `not_initialized`, `already_initialized`, `unavailable`, `sync_pending`, `internal`, `not_supported`.

## Authentication

Accounts belong to one of two roles:

- **Administrators** (`admin`) see and manage everything: all rules, users, tenants, cluster, node, settings, DNS and updates.
- **Members** (`member`) belong to exactly one tenant. All members of a tenant have the same rights. A member sees and manages only the tenant's rules, together with their connections, metrics, events and log records. Every other endpoint returns `403 forbidden`.

Two ways to authenticate:

1. **Session cookie** `fg_session` (HttpOnly, SameSite=Lax), set by `login`/`setup`. For cookie-authenticated `POST/PUT/PATCH/DELETE` requests, a present `Origin` header must match the request host (CSRF defense). Browsers send it automatically; the UI must use same-origin `fetch` with `credentials: "same-origin"`.
2. **API token** `Authorization: Bearer fgw_...` created in the UI. A token acts as the user who created it, with that user's current role and tenant. Disabling or deleting the user disables the token as well.

When no administrator exists yet (`initialized=false`), the server log prints a one-time **setup token** on startup, and `POST /api/auth/setup` requires it.

**Account recovery.** Run `firegateway -reset-auth` on a stopped node. It changes nothing else, and its next start prints a recovery setup token. Setup with that token does one of two things:

- If an account with that username exists, it resets the password and makes the account an enabled administrator.
- Otherwise it creates a new administrator.

Either way, every session of that account is signed out. Recovery ends after one successful setup. In a cluster, the reset is a normal replicated write, so it applies on both nodes.

Public endpoints (no auth): `GET /health`, `GET /ready`, `GET /api/version`, `GET /api/auth/state`, `POST /api/auth/login`, `POST /api/auth/setup`. Everything else under `/api` returns `401 unauthorized` without valid credentials.

| Method & path | Body | Response |
|---|---|---|
| `GET /api/auth/state` | – | `AuthState` |
| `POST /api/auth/setup` | `{setupToken, username, password}` | `200 {id, username}` + cookie. `409 already_initialized`, `403 forbidden` (bad token) |
| `POST /api/auth/login` | `{username, password}` | `200 {id, username}` + cookie. `401` (also for disabled users), `429 rate_limited` (after repeated failures; `Retry-After` header) |
| `POST /api/auth/logout` | – | `204` |
| `POST /api/auth/password` | `{currentPassword, newPassword, username?}` | `200 {id, username}`. Signs out the caller's other sessions; re-issues the caller's cookie |
| `GET /api/auth/tokens` | – | `{items: ApiToken[]}`: the caller's own tokens |
| `POST /api/auth/tokens` | `{name}` | `201 {token: string, item: ApiToken}` — `token` is shown only once |
| `DELETE /api/auth/tokens/{id}` | – | `204`; `404` for another user's token |

Password rules: 8–128 characters. Username: 1–64 characters, unique regardless of case.

```ts
type AuthState = {
  initialized: boolean       // an enabled administrator exists
  setupAvailable: boolean    // the setup form applies: first start, or recovery after -reset-auth
  authenticated: boolean
  userId?: string; username?: string
  role?: "admin" | "member"
  tenantId?: string; tenantName?: string   // members only
}
type ApiToken = { id: string; name: string; prefix: string; createdAt: string }
```

Upgrading from a single-admin release keeps working credentials:

- A standalone node turns the old administrator, its API tokens and its open sessions into the first administrator account at startup.
- A cluster does the same through the primary once the nodes are paired.

From then on, the primary's accounts are the cluster's accounts. A node that was set up before pairing adopts them, and its old local administrator is dropped once the shared accounts exist.

## Users and tenants

Only administrators can call these endpoints.

A tenant owns a set of local port ranges. Its members can create rules only with listen ports inside those ranges. Ranges of different tenants must not overlap. A member cannot pick or change a rule's `owner`: rules they create belong to their tenant. Members may set per-rule ACLs and limits, and may use any target host.

| Method & path | Body | Response |
|---|---|---|
| `GET /api/users` | – | `{items: User[]}` |
| `GET /api/users/{id}` | – | `User` |
| `POST /api/users` | `{username, password, role?, tenantId?}` | `201 User`. `role` defaults to `member`; members need `tenantId` |
| `PUT /api/users/{id}` | any of `{username, password, role, tenantId, disabled}` | `User`. A password change or disabling signs the user out everywhere; role and tenant changes apply to existing sessions immediately |
| `DELETE /api/users/{id}` | – | `204`; also revokes the user's tokens |
| `GET /api/tenants` | – | `{items: Tenant[]}` |
| `GET /api/tenants/{id}` | – | `Tenant` |
| `POST /api/tenants` | `{id, name, portRanges, quota}` | `201 Tenant` |
| `PUT /api/tenants/{id}` | `{name, portRanges, quota}` (full replacement) | `Tenant`. `400 validation` if existing rules of the tenant would fall outside the new ranges |
| `DELETE /api/tenants/{id}` | – | `204`; `409 conflict` while users or rules still belong to it |
| `POST /api/tenants/{id}/usage/reset` | – | `Tenant`; starts a new traffic period now, on every node |

Members read their own tenant with `GET /api/tenant`. It returns the same shape, with `users` set to 0.

Administrators cannot demote, disable or delete themselves, or remove the last enabled administrator. Any of these returns `409 conflict`.

```ts
type User = {
  id: string; username: string
  role: "admin" | "member"
  tenantId?: string
  disabled: boolean
  tokens: number             // API tokens owned
  createdAt: string
}

type Tenant = {
  id: string                 // chosen on create, same rules as rule ids
  name: string
  portRanges: [number, number][]   // inclusive local port spans
  quota: {
    maxRules?: number        // rules the tenant may own; 0 = unlimited
    monthlyBytes?: number    // up+down traffic per period across both nodes; 0 = unlimited
    resetDay?: number        // 1-28, day of month (UTC) a period starts; default 1
  }
  usageResetAt?: string      // last manual usage reset
  createdAt: string
  rules: number; users: number
  usage: { since: string; bytes: number }   // traffic in the current period
  suspended: boolean         // over its monthly traffic quota
}
```

**Quotas.**

- Creating or importing more rules than `maxRules` allows returns `409 quota_exceeded`.
- Traffic is counted per node and persisted in `dataDir/tenant-usage.json`. Paired nodes exchange their counts on every heartbeat, so the limit applies to the sum over both nodes.
- When a tenant reaches `monthlyBytes`, its rules stop and their connections are closed. Their `runtime.state` becomes `suspended`.
- They resume automatically when a new period starts, when the limit is raised, or after a usage reset.
- Enforcement checks the count every few seconds, so a tenant can exceed the limit slightly before the cut-off.

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
  owner?: string             // tenant id; empty = administrators only. Forced to the caller's tenant for members;
                             // an administrator's PUT without the field keeps the current owner
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

type RuleState = "running" | "partial" | "error" | "stopped" | "suspended"
// running: all listeners up; partial: some ports failed; error: none up; stopped: status inactive;
// suspended: active, but the owning tenant is over its traffic quota

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
| `GET /api/overview` | `Overview` (for members: counts and totals of their tenant only, without host details) |
| `POST /api/system/restart` | `202 {}` — re-execs the process (Unix); `501 not_supported` on Windows |

```ts
type Overview = {
  version: string; commit: string; buildTime: string
  startedAt: string; uptime: number /* seconds */
  goVersion: string; os: string; arch: string; cpus: number; goroutines: number
  memory: { sys: number; heapAlloc: number; heapInuse: number }   // bytes
  rules: { total: number; active: number; running: number; partial: number; error: number; suspended: number }
  totals: Counters                                                 // across the visible rules
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
| `POST /api/config/reload` | – | `{added, updated, removed}` — administrators only; re-reads node settings and, in standalone mode, the versioned rules snapshot |

Members see and change only their tenant's rules. Other rules return `404`. Import with `mode: "replace"` replaces only the caller's own rules, and export contains only those.

Further errors for members:

- A listen port outside the tenant's ranges returns `400 validation` with `field: "localPort"`. The same field is used for port ranges.
- A conflict with a rule the caller cannot see returns `409 conflict` without naming that rule.

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

History is persisted in `dataDir` and survives restarts. For members:

- `rule` must be one of their rules (otherwise `404`).
- Omitting `rule` returns the tenant's combined series.
- `top` lists only their rules.

## Live events (Server-Sent Events)

`GET /api/events` — `text/event-stream`, authenticated like any other endpoint (cookie works with `EventSource`). Sends a comment ping every 15s.

| Event | Data | When |
|---|---|---|
| `stats` | `{t: number, totals: Counters, rules: Record<ruleId, Counters & {state: RuleState}>}` | every second |
| `rules` | `{action: "created"\|"updated"\|"deleted"\|"reloaded", id?: string}` | after any rule change — the UI should refetch rule queries. Members receive `stats` for their rules only, and `rules` events about other rules without `id` |

`GET /api/logs/stream?level={error\|warn\|info\|debug\|trace}` — SSE, event `log` with a `LogEntry` per record at or above `level` (default: current log level). On connect, send nothing historical; use `GET /api/logs` for backlog. Members receive only records tagged with one of their rules (`attrs.ruleId`), here and in `GET /api/logs`.

## Logs

| Method & path | Body | Response |
|---|---|---|
| `GET /api/logs?limit={n}&level={lvl}&q={text}` | – | `{items: LogEntry[]}` oldest→newest, from an in-memory ring (last 2000 records). `limit` default 500 |
| `GET /api/log-level` | – | `{level: string, levels: string[]}` |
| `PUT /api/log-level` | `{level}` | `{level}` — administrators only; applies immediately, persisted |

## Settings

Settings, self-update, DNS, node and cluster endpoints are restricted to administrators.

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
