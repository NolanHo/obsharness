# Native agent interface

`obsharness` exposes native observability signals first. It does not wrap every response in JSON, and it does not force a rigid investigation state machine.

The interface has two goals:

- keep each screen close to the source signal shape
- preserve enough stable ids for an agent to pivot without guessing

## Principles

- Search finds entry points. It does not replace logs, traces, or metrics.
- Default output is native text. `--json` remains opt-in for tests and scripting.
- Time range is part of every query path. Unbounded scans overload both the backend and the agent.
- Stable ids stay visible in the default view: `trace_id`, `span_id`, `service`, `operation`, `request_id`.
- Default output shows the shortest useful view. Large payloads stay hidden until explicitly expanded.
- The tool fixes the front-door flow, but not the investigation path. Agents can pivot from any visible id.

## Command surface

Top-level commands:

```text
obsh search <text>
obsh logs [filters]
obsh trace <trace_id>
obsh span <span_id>
obsh metrics <expr>
```

Notes:

- `search` is the front door when the agent only has fuzzy text.
- `logs`, `trace`, and `metrics` return the signal itself.
- `span` is the expansion path for hidden span attributes and events.
- Current `q search` can remain as a compatibility layer during migration.

## Shared query shape

Each command accepts the same minimal filters when they make sense:

```text
--since 30m
--start 2026-03-31T10:00:00Z
--end 2026-03-31T10:30:00Z
--service checkout
--operation "POST /checkout"
--trace-id tr-1
--request-id req-9
--limit 200
```

Rules:

- `--since` is the default time constraint when neither `--start` nor `--end` is provided.
- Text search uses fuzzy matching. Everything else uses exact ids or backend-native filters.
- Output always exposes whether truncation happened.

## Search output

`search` returns compact index lines that point at the native signals.

Example:

```text
# source=victoria start=2026-03-31T09:30:00Z end=2026-03-31T10:00:00Z limit=20 truncated=false
log    2026-03-31T10:02:14Z service=checkout trace_id=tr-1 request_id=req-9 msg="gateway timeout"
trace  2026-03-31T10:02:14Z trace_id=tr-1 root_span=s1 root="POST /checkout" dur=2413ms status=error
metric 2026-03-31T10:03:00Z http_request_errors_ratio{service="checkout"} 0.083
```

Requirements:

- one line per hit
- signal type in column 1
- enough ids to pivot directly into `logs`, `trace`, `span`, or `metrics`
- no long summaries

## Logs output

Default logs output is one line per record. It should feel like reading a log file, not decoding an API payload.

Example:

```text
# source=victorialogs start=2026-03-31T09:30:00Z end=2026-03-31T10:00:00Z limit=200 truncated=true
2026-03-31T10:02:14Z level=error service=checkout op="POST /checkout" trace_id=tr-1 span_id=s4 request_id=req-9 msg="gateway timeout"
2026-03-31T10:02:14Z level=error service=payments op="POST /capture" trace_id=tr-1 span_id=s6 request_id=req-9 msg="capture deadline exceeded"
```

Format rules:

- timestamp first
- logfmt-style key/value prefix for stable fields
- message last as `msg="..."`
- raw provider-specific fields stay hidden by default
- multiline bodies require an explicit expansion flag such as `--full`

This keeps each line usable as a standalone evidence unit.

## Trace output

Default trace output is an ASCII tree. A tree preserves causality and latency concentration. Flat lists do not.

Example:

```text
# source=victoriatraces trace_id=tr-1 root_span=s1 spans=6 errors=1
POST /checkout service=api dur=2413ms status=error span_id=s1
|- validate_cart service=checkout dur=31ms status=ok span_id=s2
|- reserve_inventory service=inventory dur=184ms status=ok span_id=s3
\- charge_card service=payments dur=2176ms status=deadline_exceeded span_id=s4 attrs=hidden events=hidden
   |- POST /auth service=payments dur=41ms status=ok span_id=s5
   \- POST /capture service=payments dur=2129ms status=deadline_exceeded span_id=s6 attrs=hidden events=hidden
# attrs and events are hidden by default; inspect one span with: obsh span <span_id>
```

Format rules:

- root span first
- one line per span
- indentation encodes parent-child structure
- each line includes `service`, duration, status, and `span_id`
- hidden payloads are explicit: `attrs=hidden`, `events=hidden`
- long healthy subtrees may be collapsed with a stable omission marker such as `[14 sibling spans omitted]`

This gives the agent the trace shape first, then a deterministic expansion path.

## Span output

`span` expands one span in detail when the tree says attributes or events are hidden.

Example:

```text
# source=victoriatraces trace_id=tr-1 span_id=s6 parent_span_id=s4
name="POST /capture" service=payments dur=2129ms status=deadline_exceeded
attr http.method="POST"
attr http.route="/capture"
attr peer.service="stripe"
event 2026-03-31T10:02:14.120Z name="retry" attempt=1
```

Format rules:

- header line with owning `trace_id` and `span_id`
- summary line for the span itself
- one line per attribute or event
- no tree here; `span` is the detail view

This avoids flooding the trace tree while keeping all detail reachable by id.

## Metrics output

Default metrics output uses Prometheus text form.

Instant query example:

```text
# expr=rate(http_requests_total{service="checkout",status=~"5.."}[5m]) time=1711872000
rate(http_requests_total{service="checkout",status=~"5.."}[5m]){service="checkout"} 0.42 1711872000000
```

Range query example:

```text
# expr=rate(http_requests_total{service="checkout",status=~"5.."}[5m]) start=1711871700 end=1711872000 step=60s truncated=false
rate(http_requests_total{service="checkout",status=~"5.."}[5m]){service="checkout"} 0.21 1711871700000
rate(http_requests_total{service="checkout",status=~"5.."}[5m]){service="checkout"} 0.37 1711871760000
rate(http_requests_total{service="checkout",status=~"5.."}[5m]){service="checkout"} 0.42 1711871820000
```

Format rules:

- metadata lives in `#` comment lines
- sample lines use Prometheus label syntax and timestamps
- no wrapper JSON in the default path

## Native-first, not text-only

`--json` still matters for tests, reproducibility, and machine assertions. It is not the default interface.

Default path:

- humans and agents read native text views
- agents pivot using visible ids
- scripts opt into `--json` when they need exact structured contracts

This keeps the primary interface close to logs, traces, and metrics while preserving a structured escape hatch.
