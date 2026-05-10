package search

import "context"

// Provider is implemented by observability backends.
type Provider interface {
	Search(ctx context.Context, in Query) (Result, error)
	Logs(ctx context.Context, in LogsQuery) (LogsResult, error)
	Trace(ctx context.Context, in TraceQuery) (TraceResult, error)
	Span(ctx context.Context, in SpanQuery) (SpanResult, error)
	Metrics(ctx context.Context, in MetricsQuery) (MetricsResult, error)
}

// Query is the backend-agnostic search input.
type Query struct {
	Text  string
	Since string
	Start string
	End   string
	Limit int
}
