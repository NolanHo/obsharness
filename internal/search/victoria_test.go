package search

import (
	"context"
	"errors"
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
		fetchSearch: func(context.Context, string, int) ([]byte, error) {
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
		fetchSearch: func(_ context.Context, query string, limit int) ([]byte, error) {
			if query != "panic" || limit != 1 {
				t.Fatalf("unexpected fallback args: %s %d", query, limit)
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

func TestVictoriaProviderTraceAndSpan(t *testing.T) {
	payload := []byte(`{"data":[{"traceID":"tr-9","spans":[{"traceID":"tr-9","spanID":"s1","operationName":"POST /checkout","duration":2413000,"processID":"p1","tags":[{"key":"service.name","value":"api"},{"key":"otel.status_code","value":"ERROR"}]},{"traceID":"tr-9","spanID":"s2","operationName":"charge_card","duration":2100000,"processID":"p2","references":[{"refType":"CHILD_OF","spanID":"s1"}],"tags":[{"key":"service.name","value":"payments"}],"logs":[{"timestamp":1711872000,"fields":[{"key":"event","value":"retry"},{"key":"attempt","value":"1"}]}]}],"processes":{"p1":{"serviceName":"api"},"p2":{"serviceName":"payments"}}}]}`)
	provider := VictoriaProvider{
		fetchTrace: func(context.Context, string) ([]byte, error) { return payload, nil },
		fetchSpan:  func(context.Context, string) ([]byte, error) { return payload, nil },
	}

	traceRes, err := provider.Trace(context.Background(), "tr-9")
	if err != nil {
		t.Fatalf("trace failed: %v", err)
	}
	if traceRes.RootSpanID != "s1" || len(traceRes.Spans) != 2 {
		t.Fatalf("unexpected trace result: %+v", traceRes)
	}
	if !traceRes.Spans[1].EventsHidden {
		t.Fatalf("expected hidden events in trace view: %+v", traceRes.Spans[1])
	}

	spanRes, err := provider.Span(context.Background(), "s2")
	if err != nil {
		t.Fatalf("span failed: %v", err)
	}
	if spanRes.Span.SpanID != "s2" || len(spanRes.Span.Events) != 1 {
		t.Fatalf("unexpected span result: %+v", spanRes)
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
