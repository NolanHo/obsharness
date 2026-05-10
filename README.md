# obsharness

`obsharness` is an incident-first observability harness for agents.

Instead of treating logs, traces, and metrics as separate tools, `obsharness` makes the common production-debugging path explicit:

1. constrain by time when you have it
2. search by stable ids when you have them
3. fall back to keyword search when you do not
4. pivot from search hits into related logs, traces, and metrics

## Status

This repository is the clean Go rewrite target for the current local Python prototype.

## Current command surface

```bash
obsh search [--provider NAME] [--since DUR|--start T --end T] [--limit N] [--json] <query>
obsh logs [--provider NAME] [--since DUR|--start T --end T] [filters] [--json] [query]
obsh trace [--provider NAME] [--since DUR|--start T --end T] [--json] <trace_id>
obsh span [--provider NAME] [--trace-id ID|--service NAME] [--since DUR|--start T --end T] [--limit N] [--json] <span_id>
obsh metrics [--provider NAME] [--since DUR|--start T --end T] [--step DUR] [--json] <expr>
```

Examples:

```bash
obsh search --limit 3 "checkout timeout"
obsh logs --trace-id tr-1 --start 2026-05-09T10:00:00Z --end 2026-05-09T13:00:00Z
obsh trace --start 2026-05-09T10:00:00Z --end 2026-05-09T13:00:00Z tr-1
obsh span --trace-id tr-1 s6
obsh span --service checkout --since 48h s6
obsh metrics 'rate(http_requests_total{service="checkout",status=~"5.."}[5m])'
```

Stable id searches such as `trace_id=...`, `span_id=...`, and `request_id=...` are treated as pivots. When `VICTORIA_TRACES_URL` is configured, the Victoria provider routes trace and request pivots through VictoriaTraces' Jaeger-compatible API instead of sending them to VictoriaLogs as free text. Span lookup prefers `--trace-id`; without it, Victoria first tries a logs pivot from `span_id` to `trace_id`, then scans Jaeger traces by `--service` and the supplied time window.

VictoriaLogs uses LogSQL field filters. `obsh logs` accepts exact-id convenience input such as `request_id=...` and rewrites it to `request_id:"..."` before calling VictoriaLogs.

Default output is native text:

- logs: one line per record
- trace: ASCII tree with hidden attrs/events markers
- span: expanded attrs/events for one span
- metrics: Prometheus text form
- `--json`: opt-in for tests and scripting

## Search providers

- `victoria` (default): uses local `victoriaq` when available, then falls back to `VICTORIA_LOGS_URL`
- `openobserve`: OpenObserve HTTP API adapter (logs, trace summary, PromQL metrics)
- `mock`: deterministic local provider used for fixture-based CLI checks

A provider router is in place under `internal/search/` so real backends can be added without changing the command surface. `obsh q search` remains as a compatibility alias during migration.

## Pi Extension

This repository now also exposes a pi package named `pi-extension-obsh`.
The package keeps the Go CLI as the execution backend and adds session-scoped pi tools on top:

- `obsh_list_profiles`
- `obsh_status`
- `obsh_use_profile`
- `obsh_clear_profile`
- `obsh_search`
- `obsh_logs`
- `obsh_trace`
- `obsh_span`
- `obsh_metrics`

Query tools reject when no profile is active.
The model or the user must activate a configured profile first.
This prevents an unrelated task from silently querying the wrong backend.

Default install path:

```bash
pi install /absolute/path/to/obsharness
```

Profile configuration lives under the `obsh` key in `~/.pi/agent/settings.json` or the nearest `.pi/settings.json`.
See `config/pi-extension-obsh.settings.example.json` for a minimal example.

Example:

```json
{
  "packages": ["/absolute/path/to/obsharness"],
  "obsh": {
    "profiles": {
      "mint": {
        "provider": "mint-victoria",
        "routingHints": true
      }
    },
    "providers": {
      "mint-victoria": {
        "type": "victoria",
        "logsUrl": "http://192.168.4.70:9428",
        "tracesUrl": "http://192.168.4.70:10428",
        "metricsUrl": "http://192.168.4.70:8428"
      }
    }
  }
}
```

Session flow:

```text
1. obsh_list_profiles
2. obsh_use_profile name=mint
3. obsh_search query="request_id or error text"
```

The extension resolves the backend command in this order:

1. `obsh.command` from settings
2. `obsh` from `PATH`
3. `go run ./cmd/obsh` from this package root

## Project layout

- `cmd/obsh/` - CLI entrypoint
- `internal/cli/` - command wiring and flag parsing
- `internal/model/` - shared result types
- `internal/search/` - search entry, provider contracts, provider router
- `docs/` - design notes and command contracts

## Open-source hygiene

- `.gitignore` excludes local artifacts and env files
- `config/example.env` documents environment keys without real credentials
- no committed runtime credentials or private tokens

## Near-term roadmap

- tighten Victoria query translation for logs and traces
- expand trace and span rendering for large trees
- keep JSON output contracts stable during the rewrite
- add more backend adapters behind the profile and provider config model
