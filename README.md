# FireGateway

Lightweight TCP/UDP port forwarding manager written in Go, with a built-in web UI. A single static binary: lighter than nginx-proxy-manager, far easier to manage than rinetd.

> The original Node.js implementation lives in [lieyanc/FireProxy](https://github.com/lieyanc/FireProxy) (also the `legacy` branch). Its `config.json` is accepted; inline forwarding rules migrate into a separate versioned rules file on first start.

OpenWrt DMZ failover and per-node local IP/service overrides are described in [the deployment guide](docs/ha-openwrt.md). Gateways replicate rules directly with durable peer acknowledgments. OpenWrt uses its existing UCI API to switch selected DNAT destinations; no custom router plugin or etcd service is required.

## Features

- **Forwarding**: TCP and UDP, single ports or one-to-one port ranges, IPv4/IPv6. TCP uses `splice(2)` on Linux (zero-copy) with half-close propagation; UDP keeps one upstream socket per client with idle reaping, and on Linux moves queued datagrams in batches (`recvmmsg`/`sendmmsg`), which suits WireGuard and game traffic.
- **DNS cache**: hostname targets are resolved in the background and re-resolved every minute, so new connections never wait for DNS. If a lookup fails, the last good addresses stay in use. The cache can be inspected and refreshed from the UI (Settings → DNS).
- **Hot changes**: create, edit, enable/disable and delete rules at runtime. Changing only the target, ACL, limits or name keeps listeners and live connections; changing listen address/ports re-binds just that rule.
- **Access control & limits** per rule: IP/CIDR allow- or deny-list, max concurrent connections (total and per source IP), bandwidth limit per direction.
- **Live monitoring**: per-rule counters and rates pushed over Server-Sent Events every second, live connection list with the ability to close connections, traffic history (1h / 24h / 7d / 30d) persisted across restarts.
- **Web UI** (React + shadcn/ui, embedded in the binary): dashboard, rules, connections, traffic, live logs, settings; English and Chinese; light/dark theme.
- **Multi-tenant**:
  - Administrators create tenants with their own local port ranges, rule limits and monthly traffic quota.
  - Tenant members see and manage only their tenant's rules, connections, traffic and logs.
  - A tenant over its traffic quota is suspended until the next period.
- **Security**: bcrypt user accounts with admin/member roles, signed HttpOnly session cookies, per-user API tokens for scripts, login throttling, CSRF checks. In a cluster, both nodes share the same accounts.
- **Self-update** from GitHub Releases (opt-in), verified against `SHA256SUMS`.

## Quick start

Download a binary from [Releases](https://github.com/lieyanc/FireGateway/releases), or build from source (Go 1.26+, Node 24+):

```bash
make build                     # builds web/ then bin/firegateway
./bin/firegateway              # uses ./config.json, creating/completing it from the built-in template
./bin/firegateway -c /etc/firegateway/config.json
```

Open `http://127.0.0.1:8080`. On first start no admin account exists; the log prints a one-time **setup token**:

```
level=WARN msg="admin account not set up yet: open the web UI and enter this setup token" url=http://127.0.0.1:8080 setupToken=Xy12...
```

Enter it in the UI together with a username and password to create the first administrator. Then add tenants and their members under *Users & tenants* in the UI.

Lost every administrator password? Recover access like this:

1. Stop the service.
2. Run `firegateway -c <config> -reset-auth`.
3. Start the service again. It logs a recovery setup token.
4. Enter the token in the UI with a username and password. If that account exists, its password is reset and it is made an administrator. Otherwise a new administrator is created.

Other accounts, tenants and rules are kept.

The UI listens on `127.0.0.1` by default. To reach it remotely, put it behind a TLS reverse proxy (recommended) or set `api.host` to `0.0.0.0`.

Flags: `-c <path>` config file, `-v` print version, `-reset-auth` enable one-time administrator recovery on the next start. Signals: `SIGINT`/`SIGTERM` graceful shutdown, `SIGHUP` reload node settings and the standalone rules snapshot.

### systemd

```ini
[Unit]
Description=FireGateway
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/firegateway/firegateway -c /opt/firegateway/config.json
WorkingDirectory=/opt/firegateway
Restart=on-failure
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
```

Self-update and the UI's restart button replace the process in place (same PID), so systemd does not see a restart.

## Configuration

Node settings and credentials are saved atomically to `config.json`; forwarding rules are saved separately to `rulesFile` (default `rules.json`, relative to the config directory). Both use mode `0600`. Use the UI/API to edit versioned rules. Router connection settings and node IDs are configured on disk and require restart; local address overrides are editable in Settings → Cluster. See `config.example.json` for legacy import compatibility.

The complete default configuration is a JSON template compiled into the binary ([`internal/config/template.json`](internal/config/template.json)). On start, a missing config file is created from it, and fields missing from an existing file (including new ones added by an update) are filled in from it and saved. Legacy inline `forward` is migrated out of the node file into the rules snapshot.

Each entry in the rules snapshot’s `rules` array (or legacy import/export `forward`):

| Field | Description |
|---|---|
| `id` | Rule id (number or string; generated when created from the UI) |
| `name`, `remark` | Optional display name and note |
| `type` | `tcp` or `udp` |
| `status` | `active` rules are started; `inactive` rules are kept but stopped |
| `localHost` / `targetHost` | Listen address / target host (IP or hostname) |
| `localPort` / `targetPort` | Single port mapping |
| `localPortRange` / `targetPortRange` | `[start, end]`, same length; maps ports one-to-one (max 1024 ports) |
| `acl` | `{"mode": "allow" \| "deny", "cidrs": ["10.0.0.0/8", "203.0.113.7"]}` |
| `limits` | `{"maxConnections", "maxConnectionsPerIp", "bandwidth"}` — bandwidth in bytes/s per direction, shared by the rule; `0` = unlimited |

Other sections (missing fields are filled in from the template on start):

| Section | Fields |
|---|---|
| `api` | `enabled` (default true), `host` (`127.0.0.1`), `port` (`8080`), `enableCors` (false; only API tokens work cross-origin) |
| `logging` | `level` (`error`/`warn`/`info`/`debug`/`trace`), `enableConsole`, `enableFile`, `logDir`, `maxFileSize`, `maxFiles`. Per-connection events are logged at `debug` |
| `update` | `enabled` (false), `channel` (`stable`/`dev`), `checkInterval` (seconds), `source` (`github`/`proxy`), `proxyBaseUrl`, `repo` |
| `dataDir` | Traffic history, per-tenant traffic usage (`tenant-usage.json`) and downloaded updates (`./data`) |
| `auth` | Node-local recovery flag; credentials from single-admin releases are migrated into the shared accounts on start |

Environment variables `API_HOST`, `API_PORT` and `LOG_LEVEL` take precedence over the template for fields that are missing when the file is created or completed.

## HTTP API

The UI is a client of a documented JSON API under `/api` — see [docs/api.md](docs/api.md). Authenticate scripts with an API token created in *Settings → API tokens*:

```bash
curl -H "Authorization: Bearer fgw_..." http://127.0.0.1:8080/api/rules
```

`GET /health` stays public and unauthenticated for monitors (200 when at least one listener is up, else 503).

> Upgrading from the pre-UI Go version: `/stats`, `/status`, `/proxy-stats` and `/log-level` moved under `/api` (`/api/overview`, `/api/rules`, `/api/log-level`) and now require authentication.

## Development

```bash
make run        # full build, then run bin/firegateway (CONFIG=config.json)
make dev        # backend on :8080 with the UI currently in web/dist
make dev-web    # Vite dev server on :5173 proxying /api to :8080 (hot reload)
make test       # go vet + go test -race (excludes local stress tests)
make stress-udp # opt-in local UDP burst stress check
make lint       # frontend lint
```

The UDP burst stress check uses the `stress` build tag and is excluded from CI.
It sends all 120 datagrams before reading replies and requires zero loss, so
its result depends on local socket buffer limits and scheduling. Use it on a
controlled local machine; a timeout alone does not identify a forwarding bug.
Existing throughput benchmarks can be run with
`go test ./internal/proxy -run '^$' -bench . -benchmem`.

Layout:

```
cmd/firegateway     entrypoint
internal/config     config model, validation, atomic store
internal/proxy      TCP/UDP forwarding engine (splice, ACL, limits, connection tracking)
internal/dnscache   background-refreshed resolution of hostname targets
internal/gateway    rule lifecycle and hot reconciliation
internal/metrics    1s sampler, realtime/minute/hour series, persistence
internal/auth       users, tenants, sessions, API tokens, account recovery
internal/quota      per-tenant traffic accounting and quota suspension
internal/api        REST + SSE handlers, embedded UI serving
internal/logx       logging, file rotation, in-memory log ring
internal/updater    self-update from GitHub Releases
web/                React + TypeScript + shadcn/ui frontend (embedded via go:embed)
```

Releases: pushing to `master` publishes a rolling `dev` prerelease; pushing a `v*` tag publishes a stable release. Both include binaries for linux (amd64/arm64/riscv64), darwin (amd64/arm64) and windows (amd64/arm64) plus `SHA256SUMS`.
