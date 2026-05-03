# Architecture

`obsharness` is intentionally agent-first.

## Core principles

- Search is the front door.
- Time and stable ids beat fuzzy text.
- Results stay close to source evidence.
- Pagination metadata is part of the public contract.
- Backends are adapters, not the product.

## Current command surface

- CLI entry: `obsh search`, `obsh logs`, `obsh trace`, `obsh span`, `obsh metrics`
- Compatibility alias: `obsh q search`
- Router: `internal/search/router.go`
- Provider contract: `internal/search/provider.go`
- Default provider: `victoria` for incident-facing runs; `mock` remains fixture-only

Default output stays close to source evidence:

- search: compact index lines
- logs: native one-line records
- trace: ASCII tree with hidden attr/event markers
- span: expanded details for one span
- metrics: Prometheus text form

## Initial backend target

Backend adapters:

- Victoria stack: VictoriaMetrics, VictoriaLogs, VictoriaTraces
- OpenObserve: logs search (SQL) and trace latest summary (HTTP API)

## Migration plan

1. keep the Python prototype working in the workspace
2. port subcommands incrementally into Go
3. keep JSON output contracts stable during the rewrite
4. tighten backend-specific query translation as Victoria coverage improves
