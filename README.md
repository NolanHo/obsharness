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
obsh q search [--provider NAME] [--limit N] [--json] <query>
```

Example:

```bash
obsh q search --provider mock --limit 3 "checkout timeout"
```

Output stays concise and evidence-first. It lists direct hits and avoids long-form summaries.

## Search providers

- `mock` (default): deterministic local provider used while wiring CLI contracts

A provider router is in place under `internal/search/` so real backends can be added without changing CLI shape.

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

- add Victoria provider behind the same `q search` interface
- port `related`, `services`, `operations`, `trace-fields`, `trace-values`
- keep JSON output contracts stable during the rewrite
