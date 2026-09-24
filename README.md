# FireGateway

High-performance TCP/UDP port forwarder written in Go. Single static binary, no dependencies.

> The original Node.js implementation lives in [lieyanc/FireProxy](https://github.com/lieyanc/FireProxy) (also the `legacy` branch). Its `config.json` works unchanged.

## Build & Run

```bash
go build -trimpath -ldflags "-s -w" -o firegateway .
cp config.example.json config.json
./firegateway              # reads ./config.json
./firegateway -c /etc/firegateway.json
```

## Configuration

See `config.example.json`. Each entry in `forward`:

| Field | Description |
|---|---|
| `id` | Rule id (number or string) |
| `name` | Optional display name |
| `type` | `tcp` or `udp` |
| `status` | Only `active` rules are started |
| `localHost` / `targetHost` | Listen / target address |
| `localPort` / `targetPort` | Single port mapping |
| `localPortRange` / `targetPortRange` | `[start, end]`, same length; maps ports one-to-one |

`api` (`enabled`, `host`, `port`, `enableCors`) and `logging` (`level`, `enableConsole`, `enableFile`, `logDir`, `maxFileSize`, `maxFiles`) are optional. Log levels: `error`, `warn`, `info`, `debug`, `trace`. Per-connection events are logged at `debug`.

Environment fallbacks: `API_HOST`, `API_PORT`, `LOG_LEVEL`.

## HTTP API

Default `127.0.0.1:8080`:

- `GET /health` — 200 when at least one proxy is running, else 503
- `GET /stats` — aggregated counters and process memory
- `GET /status` — per-proxy stats (list)
- `GET /proxy-stats` — per-proxy stats (keyed by proxy id)
- `GET|POST /log-level` — read or change the log level at runtime, e.g. `{"level":"debug"}`

## Design

- TCP: one upstream dial per client, bidirectional `io.Copy` which uses `splice(2)` on Linux (zero-copy), proper half-close propagation, TCP keep-alive 15s.
- UDP: one connected upstream socket per client address, idle sessions reaped after 5 minutes, lock-free counters.
