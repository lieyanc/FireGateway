# FireGateway

Lightweight TCP/UDP port forwarding manager written in Go, with a built-in web UI. A single static binary: lighter than nginx-proxy-manager, far easier to manage than rinetd.

> The original Node.js implementation lives in [lieyanc/FireProxy](https://github.com/lieyanc/FireProxy) (also the `legacy` branch). Its `config.json` works unchanged.

## Features

- **Forwarding**: TCP and UDP, single ports or one-to-one port ranges, IPv4/IPv6. TCP uses `splice(2)` on Linux (zero-copy) with half-close propagation; UDP keeps one upstream socket per client with idle reaping.
- **Hot changes**: create, edit, enable/disable and delete rules at runtime. Changing only the target, ACL, limits or name keeps listeners and live connections; changing listen address/ports re-binds just that rule.
- **Access control & limits** per rule: IP/CIDR allow- or deny-list, max concurrent connections (total and per source IP), bandwidth limit per direction.
- **Live monitoring**: per-rule counters and rates pushed over Server-Sent Events every second, live connection list with the ability to close connections, traffic history (1h / 24h / 7d / 30d) persisted across restarts.
- **Web UI** (React + shadcn/ui, embedded in the binary): dashboard, rules, connections, traffic, live logs, settings; English and Chinese; light/dark theme.
- **Security**: single admin account (bcrypt), signed HttpOnly session cookie, API tokens for scripts, login throttling, CSRF checks.
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

Enter it in the UI together with a username and password. Forgot the password? Stop the service, run `firegateway -c <config> -reset-auth`, start again and repeat setup.

The UI listens on `127.0.0.1` by default. To reach it remotely, put it behind a TLS reverse proxy (recommended) or set `api.host` to `0.0.0.0`.

Flags: `-c <path>` config file, `-v` print version, `-reset-auth` remove the admin account and API tokens. Signals: `SIGINT`/`SIGTERM` graceful shutdown, `SIGHUP` reload rules and log level from the config file.

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

Everything is editable from the UI; changes are written back to the config file atomically (mode `0600`, as it holds credential hashes). You can also edit the file by hand and send `SIGHUP` or click *Reload*. See `config.example.json`.

The complete default configuration is a JSON template compiled into the binary ([`internal/config/template.json`](internal/config/template.json)). On start, a missing config file is created from it, and fields missing from an existing file (including new ones added by an update) are filled in from it and saved. Values already in the file are never changed.

Each entry in `forward`:

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
| `dataDir` | Traffic history and downloaded updates (`./data`) |
| `auth` | Managed by the UI: admin username, bcrypt hash, session secret, API token hashes |

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
make test       # go vet + go test -race
make lint       # frontend lint
```

Layout:

```
cmd/firegateway     entrypoint
internal/config     config model, validation, atomic store
internal/proxy      TCP/UDP forwarding engine (splice, ACL, limits, connection tracking)
internal/gateway    rule lifecycle and hot reconciliation
internal/metrics    1s sampler, realtime/minute/hour series, persistence
internal/auth       admin login, sessions, API tokens
internal/api        REST + SSE handlers, embedded UI serving
internal/logx       logging, file rotation, in-memory log ring
internal/updater    self-update from GitHub Releases
web/                React + TypeScript + shadcn/ui frontend (embedded via go:embed)
```

Releases: pushing to `master` publishes a rolling `dev` prerelease; pushing a `v*` tag publishes a stable release. Both include binaries for linux (amd64/arm64/riscv64), darwin (amd64/arm64) and windows (amd64/arm64) plus `SHA256SUMS`.
