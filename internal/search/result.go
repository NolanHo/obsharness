package search

import "github.com/NolanHou/obsharness/internal/model"

// Result aliases the public model contract for backend implementations.
type Result = model.SearchResult

// Hit aliases the public model contract for backend implementations.
type Hit = model.SearchHit

// LogsQuery aliases the public model contract for backend implementations.
type LogsQuery = model.LogsQuery

// LogsResult aliases the public model contract for backend implementations.
type LogsResult = model.LogsResult

// LogRecord aliases the public model contract for backend implementations.
type LogRecord = model.LogRecord

// TraceResult aliases the public model contract for backend implementations.
type TraceResult = model.TraceResult

// TraceSpan aliases the public model contract for backend implementations.
type TraceSpan = model.TraceSpan

// SpanResult aliases the public model contract for backend implementations.
type SpanResult = model.SpanResult

// SpanEvent aliases the public model contract for backend implementations.
type SpanEvent = model.SpanEvent

// MetricsQuery aliases the public model contract for backend implementations.
type MetricsQuery = model.MetricsQuery

// MetricsResult aliases the public model contract for backend implementations.
type MetricsResult = model.MetricsResult

// MetricSample aliases the public model contract for backend implementations.
type MetricSample = model.MetricSample
