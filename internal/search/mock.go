package search

import (
	"context"
	"fmt"
	"strings"
)

// MockProvider is a deterministic local provider for CLI development.
type MockProvider struct{}

// Search returns small synthetic evidence records.
func (MockProvider) Search(_ context.Context, in Query) (Result, error) {
	q := strings.TrimSpace(in.Text)
	if q == "" {
		return Result{}, fmt.Errorf("query cannot be empty")
	}
	if in.Limit <= 0 {
		in.Limit = 10
	}

	hits := []Hit{
		{
			Kind:   "log",
			Title:  "gateway timeout",
			Source: "victorialogs",
			ID:     "log-1001",
			Fields: map[string]string{"time": "2026-03-31T10:02:14Z", "service": "checkout", "trace_id": "tr-1", "span_id": "s6", "request_id": "req-9"},
		},
		{
			Kind:   "trace",
			Title:  "POST /checkout",
			Source: "victoriatraces",
			ID:     "tr-1",
			Fields: map[string]string{"time": "2026-03-31T10:02:14Z", "trace_id": "tr-1", "root_span": "s1", "duration_ms": "2413", "status": "error"},
		},
		{
			Kind:   "metric",
			Title:  "http_request_errors_ratio",
			Source: "victoriametrics",
			ID:     "metric-3001",
			Fields: map[string]string{"time": "2026-03-31T10:03:00Z", "metric": "http_request_errors_ratio", "labels": "service=checkout", "value": "0.083"},
		},
	}

	if in.Limit < len(hits) {
		hits = hits[:in.Limit]
	}

	return Result{Provider: "mock", Query: q, Total: len(hits), Hits: hits}, nil
}

// Logs returns deterministic native log records.
func (MockProvider) Logs(_ context.Context, in LogsQuery) (LogsResult, error) {
	if in.Limit <= 0 {
		in.Limit = 200
	}
	records := []LogRecord{
		{Time: "2026-03-31T10:02:14Z", Level: "error", Service: "checkout", Operation: "POST /checkout", TraceID: "tr-1", SpanID: "s4", RequestID: "req-9", Message: "gateway timeout"},
		{Time: "2026-03-31T10:02:14Z", Level: "error", Service: "payments", Operation: "POST /capture", TraceID: "tr-1", SpanID: "s6", RequestID: "req-9", Message: "capture deadline exceeded"},
	}
	if in.TraceID != "" {
		filtered := make([]LogRecord, 0, len(records))
		for _, record := range records {
			if record.TraceID == in.TraceID {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	if in.Service != "" {
		filtered := make([]LogRecord, 0, len(records))
		for _, record := range records {
			if record.Service == in.Service {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	if len(records) > in.Limit {
		records = records[:in.Limit]
	}
	return LogsResult{
		Provider:  "mock",
		Source:    "victorialogs",
		Start:     firstNonEmpty(in.Start, "2026-03-31T09:30:00Z"),
		End:       firstNonEmpty(in.End, "2026-03-31T10:00:00Z"),
		Limit:     in.Limit,
		Truncated: false,
		Records:   records,
	}, nil
}

// Trace returns a deterministic trace tree.
func (MockProvider) Trace(_ context.Context, traceID string) (TraceResult, error) {
	if strings.TrimSpace(traceID) == "" {
		return TraceResult{}, fmt.Errorf("trace id is required")
	}
	if traceID != "tr-1" {
		return TraceResult{}, fmt.Errorf("trace %q not found", traceID)
	}
	spans := []TraceSpan{
		{SpanID: "s1", Name: "POST /checkout", Service: "api", Status: "error", DurationMS: 2413},
		{SpanID: "s2", ParentSpanID: "s1", Name: "validate_cart", Service: "checkout", Status: "ok", DurationMS: 31},
		{SpanID: "s3", ParentSpanID: "s1", Name: "reserve_inventory", Service: "inventory", Status: "ok", DurationMS: 184},
		{SpanID: "s4", ParentSpanID: "s1", Name: "charge_card", Service: "payments", Status: "deadline_exceeded", DurationMS: 2176, AttrsHidden: true, EventsHidden: true},
		{SpanID: "s5", ParentSpanID: "s4", Name: "POST /auth", Service: "payments", Status: "ok", DurationMS: 41},
		{SpanID: "s6", ParentSpanID: "s4", Name: "POST /capture", Service: "payments", Status: "deadline_exceeded", DurationMS: 2129, AttrsHidden: true, EventsHidden: true},
	}
	return TraceResult{Provider: "mock", Source: "victoriatraces", TraceID: traceID, RootSpanID: "s1", SpanCount: len(spans), ErrorCount: 1, Spans: spans}, nil
}

// Span returns detailed span attributes and events.
func (MockProvider) Span(_ context.Context, spanID string) (SpanResult, error) {
	if strings.TrimSpace(spanID) == "" {
		return SpanResult{}, fmt.Errorf("span id is required")
	}
	if spanID != "s6" {
		return SpanResult{}, fmt.Errorf("span %q not found", spanID)
	}
	span := TraceSpan{
		SpanID:       "s6",
		ParentSpanID: "s4",
		Name:         "POST /capture",
		Service:      "payments",
		Status:       "deadline_exceeded",
		DurationMS:   2129,
		Attrs: map[string]string{
			"http.method":  "POST",
			"http.route":   "/capture",
			"peer.service": "stripe",
		},
		Events: []SpanEvent{{Time: "2026-03-31T10:02:14.120Z", Name: "retry", Fields: map[string]string{"attempt": "1"}}},
	}
	return SpanResult{Provider: "mock", Source: "victoriatraces", TraceID: "tr-1", Span: span}, nil
}

// Metrics returns deterministic Prometheus-style samples.
func (MockProvider) Metrics(_ context.Context, in MetricsQuery) (MetricsResult, error) {
	expr := strings.TrimSpace(in.Expr)
	if expr == "" {
		return MetricsResult{}, fmt.Errorf("metric expression is required")
	}
	samples := []MetricSample{
		{Metric: expr, Labels: map[string]string{"service": "checkout"}, Value: "0.21", Timestamp: 1711871700000},
		{Metric: expr, Labels: map[string]string{"service": "checkout"}, Value: "0.37", Timestamp: 1711871760000},
		{Metric: expr, Labels: map[string]string{"service": "checkout"}, Value: "0.42", Timestamp: 1711871820000},
	}
	return MetricsResult{Provider: "mock", Expr: expr, Start: firstNonEmpty(in.Start, "1711871700"), End: firstNonEmpty(in.End, "1711872000"), Step: firstNonEmpty(in.Step, "60s"), Samples: samples}, nil
}
