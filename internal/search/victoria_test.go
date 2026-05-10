package search

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestVictoriaProviderSearchUsesCLIResult(t *testing.T) {
	provider := VictoriaProvider{
		lookup: func(string) (string, error) { return "victoriaq", nil },
		runSearch: func(_ context.Context, query string, limit int) ([]byte, error) {
			if query != "timeout" {
				t.Fatalf("unexpected query: %s", query)
			}
			if limit != 2 {
				t.Fatalf("unexpected limit: %d", limit)
			}
			return []byte(`{"logs":[{"_msg":"gateway timeout","trace_id":"t-1","service.name":"checkout","severity":"error"}],"traces":[{"operation":"POST /checkout","trace_id":"tr-2"}]}`), nil
		},
		fetchSearch: func(context.Context, Query) ([]byte, error) {
			t.Fatal("fetchSearch should not be called when victoriaq is available")
			return nil, nil
		},
	}

	res, err := provider.Search(context.Background(), Query{Text: "timeout", Limit: 2})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if res.Provider != "victoria" {
		t.Fatalf("unexpected provider: %s", res.Provider)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("unexpected hit count: %d", len(res.Hits))
	}
	if res.Hits[0].Kind != "log" || res.Hits[0].ID != "t-1" {
		t.Fatalf("unexpected first hit: %+v", res.Hits[0])
	}
	if res.Hits[1].Kind != "trace" || res.Hits[1].ID != "tr-2" {
		t.Fatalf("unexpected second hit: %+v", res.Hits[1])
	}
}

func TestVictoriaProviderSearchFallsBackToHTTP(t *testing.T) {
	provider := VictoriaProvider{
		lookup: func(string) (string, error) { return "", errors.New("not found") },
		runSearch: func(context.Context, string, int) ([]byte, error) {
			t.Fatal("runSearch should not be called when victoriaq is unavailable")
			return nil, nil
		},
		fetchSearch: func(_ context.Context, in Query) ([]byte, error) {
			if in.Text != "panic" || in.Limit != 1 {
				t.Fatalf("unexpected fallback args: %+v", in)
			}
			return []byte(`{"logs":[{"_msg":"panic in worker","request_id":"r-1"}]}`), nil
		},
	}

	res, err := provider.Search(context.Background(), Query{Text: "panic", Limit: 1})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != "r-1" {
		t.Fatalf("unexpected hits: %+v", res.Hits)
	}
}

func TestVictoriaProviderSearchRequiresQuery(t *testing.T) {
	provider := NewVictoriaProvider()
	_, err := provider.Search(context.Background(), Query{Text: "   "})
	if err == nil || !strings.Contains(err.Error(), "query cannot be empty") {
		t.Fatalf("expected empty query error, got %v", err)
	}
}

func TestVictoriaProviderSearchPivotsTraceIDToTraces(t *testing.T) {
	t.Setenv("VICTORIA_TRACES_URL", "http://traces.example:10428")
	payload := []byte(`{"data":[{"traceID":"tr-9","spans":[{"traceID":"tr-9","spanID":"s1","operationName":"POST /checkout","duration":2413000,"processID":"p1","tags":[{"key":"service.name","value":"api"}]}],"processes":{"p1":{"serviceName":"api"}}}]}`)
	provider := VictoriaProvider{
		lookup: func(string) (string, error) { return "victoriaq", nil },
		fetchTrace: func(_ context.Context, traceID string) ([]byte, error) {
			if traceID != "tr-9" {
				t.Fatalf("unexpected trace id: %s", traceID)
			}
			return payload, nil
		},
		runSearch: func(context.Context, string, int) ([]byte, error) {
			t.Fatal("runSearch should not be called for trace_id pivot")
			return nil, nil
		},
		fetchSearch: func(context.Context, Query) ([]byte, error) {
			t.Fatal("fetchSearch should not be called for trace_id pivot")
			return nil, nil
		},
	}

	res, err := provider.Search(context.Background(), Query{Text: "trace_id=tr-9", Limit: 5})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Kind != "trace" || res.Hits[0].Fields["trace_id"] != "tr-9" {
		t.Fatalf("unexpected trace pivot hits: %+v", res.Hits)
	}
}

func TestVictoriaProviderSearchPivotsRequestIDToJaegerTags(t *testing.T) {
	t.Setenv("VICTORIA_TRACES_URL", "http://traces.example:10428")
	provider := VictoriaProvider{
		lookup: func(string) (string, error) { return "", errors.New("not found") },
		fetchServices: func(context.Context) ([]string, error) {
			return []string{"checkout"}, nil
		},
		fetchURL: func(_ context.Context, rawURL string) ([]byte, error) {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				t.Fatalf("bad url: %v", err)
			}
			if parsed.Path != "/select/jaeger/api/traces" {
				t.Fatalf("unexpected path: %s", parsed.Path)
			}
			if got := parsed.Query().Get("service"); got != "checkout" {
				t.Fatalf("unexpected service: %s", got)
			}
			if got := parsed.Query().Get("tags"); got != `{"request_id":"req-1"}` {
				t.Fatalf("unexpected tags: %s", got)
			}
			return []byte(`{"data":[{"traceID":"tr-1","spans":[{"traceID":"tr-1","spanID":"s1","operationName":"POST /checkout","duration":2500000,"references":[]}]}]}`), nil
		},
		fetchSearch: func(context.Context, Query) ([]byte, error) {
			t.Fatal("fetchSearch should not be called for request_id pivot")
			return nil, nil
		},
	}

	res, err := provider.Search(context.Background(), Query{Text: "request_id=req-1", Limit: 5})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != "tr-1" || res.Hits[0].Fields["duration_ms"] != "2500" {
		t.Fatalf("unexpected request pivot hits: %+v", res.Hits)
	}
}

func TestVictoriaProviderTraceAndSpan(t *testing.T) {
	payload := []byte(`{"data":[{"traceID":"tr-9","spans":[{"traceID":"tr-9","spanID":"s1","operationName":"POST /checkout","duration":2413000,"processID":"p1","tags":[{"key":"service.name","value":"api"},{"key":"otel.status_code","value":"ERROR"}]},{"traceID":"tr-9","spanID":"s2","operationName":"charge_card","duration":2100000,"processID":"p2","references":[{"refType":"CHILD_OF","spanID":"s1"}],"tags":[{"key":"service.name","value":"payments"}],"logs":[{"timestamp":1711872000,"fields":[{"key":"event","value":"retry"},{"key":"attempt","value":"1"}]}]}],"processes":{"p1":{"serviceName":"api"},"p2":{"serviceName":"payments"}}}]}`)
	provider := VictoriaProvider{
		fetchTrace: func(context.Context, string) ([]byte, error) { return payload, nil },
	}

	traceRes, err := provider.Trace(context.Background(), TraceQuery{TraceID: "tr-9"})
	if err != nil {
		t.Fatalf("trace failed: %v", err)
	}
	if traceRes.RootSpanID != "s1" || len(traceRes.Spans) != 2 {
		t.Fatalf("unexpected trace result: %+v", traceRes)
	}
	if !traceRes.Spans[1].EventsHidden {
		t.Fatalf("expected hidden events in trace view: %+v", traceRes.Spans[1])
	}

	spanRes, err := provider.Span(context.Background(), SpanQuery{SpanID: "s2", TraceID: "tr-9"})
	if err != nil {
		t.Fatalf("span failed: %v", err)
	}
	if spanRes.Span.SpanID != "s2" || len(spanRes.Span.Events) != 1 {
		t.Fatalf("unexpected span result: %+v", spanRes)
	}
}

func TestVictoriaProviderSpanPivotsThroughLogs(t *testing.T) {
	tracePayload := []byte(`{"data":[{"traceID":"tr-9","spans":[{"traceID":"tr-9","spanID":"s2","operationName":"charge_card","duration":2100000,"processID":"p1"}],"processes":{"p1":{"serviceName":"payments"}}}]}`)
	provider := VictoriaProvider{
		fetchLogs: func(_ context.Context, in LogsQuery) ([]byte, error) {
			if got := buildVictoriaLogsQuery(in); got != `span_id:"s2"` {
				t.Fatalf("unexpected logs query: %s", got)
			}
			if in.Start != "2026-05-09T10:00:00Z" || in.End != "2026-05-09T13:00:00Z" {
				t.Fatalf("time window not passed to logs lookup: %+v", in)
			}
			return []byte(`{"span_id":"s2","trace_id":"tr-9"}`), nil
		},
		fetchTrace: func(_ context.Context, traceID string) ([]byte, error) {
			if traceID != "tr-9" {
				t.Fatalf("unexpected trace id: %s", traceID)
			}
			return tracePayload, nil
		},
	}

	res, err := provider.Span(context.Background(), SpanQuery{SpanID: "s2", Start: "2026-05-09T10:00:00Z", End: "2026-05-09T13:00:00Z"})
	if err != nil {
		t.Fatalf("span failed: %v", err)
	}
	if res.TraceID != "tr-9" || res.Span.SpanID != "s2" {
		t.Fatalf("unexpected span result: %+v", res)
	}
}

func TestVictoriaProviderSpanScansServiceWindow(t *testing.T) {
	t.Setenv("VICTORIA_TRACES_URL", "http://traces.example:10428")
	provider := VictoriaProvider{
		fetchLogs: func(context.Context, LogsQuery) ([]byte, error) {
			return []byte(``), nil
		},
		fetchURL: func(_ context.Context, rawURL string) ([]byte, error) {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				t.Fatalf("bad url: %v", err)
			}
			if got := parsed.Query().Get("service"); got != "checkout" {
				t.Fatalf("unexpected service: %s", got)
			}
			if got := parsed.Query().Get("start"); got != "1778320800000000" {
				t.Fatalf("unexpected start: %s", got)
			}
			if got := parsed.Query().Get("end"); got != "1778328600000000" {
				t.Fatalf("unexpected end: %s", got)
			}
			return []byte(`{"data":[{"traceID":"tr-9","spans":[{"traceID":"tr-9","spanID":"s2","operationName":"charge_card","duration":2100000,"processID":"p1"}],"processes":{"p1":{"serviceName":"payments"}}}]}`), nil
		},
	}

	res, err := provider.Span(context.Background(), SpanQuery{SpanID: "s2", Service: "checkout", Start: "1778320800000000000", End: "1778328600000000000", Limit: 5})
	if err != nil {
		t.Fatalf("span failed: %v", err)
	}
	if res.TraceID != "tr-9" || res.Span.Name != "charge_card" {
		t.Fatalf("unexpected span result: %+v", res)
	}
}

func TestBuildVictoriaLogsQueryQuotesFilters(t *testing.T) {
	query := buildVictoriaLogsQuery(LogsQuery{Text: "request_id=req-1", Service: "checkout", TraceID: "tr-1"})
	want := `request_id:"req-1" service.name:"checkout" trace_id:"tr-1"`
	if query != want {
		t.Fatalf("unexpected query: %s", query)
	}
}

func TestVictoriaProviderMetrics(t *testing.T) {
	provider := VictoriaProvider{
		fetchMetrics: func(context.Context, MetricsQuery) ([]byte, error) {
			return []byte(`{"status":"success","data":{"result":[{"metric":{"__name__":"http_request_errors_ratio","service":"checkout"},"values":[[1711871700,"0.21"],[1711871760,"0.37"]]}]}}`), nil
		},
	}

	res, err := provider.Metrics(context.Background(), MetricsQuery{Expr: "http_request_errors_ratio", Since: "30m", Step: "60s"})
	if err != nil {
		t.Fatalf("metrics failed: %v", err)
	}
	if len(res.Samples) != 2 {
		t.Fatalf("unexpected sample count: %+v", res)
	}
	if res.Samples[0].Metric != "http_request_errors_ratio" {
		t.Fatalf("unexpected sample: %+v", res.Samples[0])
	}
}
