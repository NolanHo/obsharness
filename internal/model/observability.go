package model

// LogsQuery selects log records from a backend.
type LogsQuery struct {
	Text      string `json:"text,omitempty"`
	Since     string `json:"since,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	Service   string `json:"service,omitempty"`
	Operation string `json:"operation,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// LogRecord is a single log line plus stable ids for pivots.
type LogRecord struct {
	Time      string         `json:"time"`
	Level     string         `json:"level,omitempty"`
	Service   string         `json:"service,omitempty"`
	Operation string         `json:"operation,omitempty"`
	TraceID   string         `json:"trace_id,omitempty"`
	SpanID    string         `json:"span_id,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Message   string         `json:"message"`
	Raw       map[string]any `json:"raw,omitempty"`
}

// LogsResult is the native log stream plus metadata.
type LogsResult struct {
	Provider  string      `json:"provider"`
	Source    string      `json:"source"`
	Start     string      `json:"start,omitempty"`
	End       string      `json:"end,omitempty"`
	Limit     int         `json:"limit,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
	Records   []LogRecord `json:"records"`
}

// TraceSpan is one span in a trace tree.
type TraceSpan struct {
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	Service      string            `json:"service,omitempty"`
	Status       string            `json:"status,omitempty"`
	DurationMS   int64             `json:"duration_ms,omitempty"`
	AttrsHidden  bool              `json:"attrs_hidden,omitempty"`
	EventsHidden bool              `json:"events_hidden,omitempty"`
	Attrs        map[string]string `json:"attrs,omitempty"`
	Events       []SpanEvent       `json:"events,omitempty"`
}

// SpanEvent is a timestamped event attached to one span.
type SpanEvent struct {
	Time   string            `json:"time,omitempty"`
	Name   string            `json:"name"`
	Fields map[string]string `json:"fields,omitempty"`
}

// TraceResult is a flattened trace tree. Parent ids define structure.
type TraceResult struct {
	Provider   string      `json:"provider"`
	Source     string      `json:"source"`
	TraceID    string      `json:"trace_id"`
	RootSpanID string      `json:"root_span_id,omitempty"`
	SpanCount  int         `json:"span_count,omitempty"`
	ErrorCount int         `json:"error_count,omitempty"`
	Spans      []TraceSpan `json:"spans"`
}

// SpanResult expands one span in detail.
type SpanResult struct {
	Provider string    `json:"provider"`
	Source   string    `json:"source"`
	TraceID  string    `json:"trace_id,omitempty"`
	Span     TraceSpan `json:"span"`
}

// MetricsQuery selects metric samples.
type MetricsQuery struct {
	Expr   string `json:"expr"`
	Since  string `json:"since,omitempty"`
	Start  string `json:"start,omitempty"`
	End    string `json:"end,omitempty"`
	Step   string `json:"step,omitempty"`
	Lang   string `json:"lang,omitempty"`   // promql (default) or sql
	Stream string `json:"stream,omitempty"` // for sql-backed metrics (e.g. OpenObserve)
}

// MetricSample is one Prometheus sample.
type MetricSample struct {
	Metric    string            `json:"metric"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     string            `json:"value"`
	Timestamp int64             `json:"timestamp"`
}

// MetricsResult is a Prometheus-style sample set.
type MetricsResult struct {
	Provider  string         `json:"provider"`
	Expr      string         `json:"expr"`
	Start     string         `json:"start,omitempty"`
	End       string         `json:"end,omitempty"`
	Step      string         `json:"step,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Samples   []MetricSample `json:"samples"`
}
