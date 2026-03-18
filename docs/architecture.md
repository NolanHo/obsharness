# Architecture

`obsharness` is intentionally agent-first.

## Core principles

- Search is the front door.
- Time and stable ids beat fuzzy text.
- Results stay close to source evidence.
- Pagination metadata is part of the public contract.
- Backends are adapters, not the product.

## Current search entry

- CLI entry: `obsh q search [--provider NAME] [--limit N] [--json] <query>`
- Router: `internal/search/router.go`
- Provider contract: `internal/search/provider.go`
- Default provider: `mock` for deterministic local development

The entrypoint intentionally returns compact hits and avoids long summarization.

## Initial backend target

The first real backend remains the local Victoria stack:

- VictoriaMetrics
- VictoriaLogs
- VictoriaTraces

## Migration plan

1. keep the Python prototype working in the workspace
2. port subcommands incrementally into Go
3. keep JSON output contracts stable during the rewrite
4. split backend-specific code under `internal/victoria/`
