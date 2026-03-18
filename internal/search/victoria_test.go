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
		run: func(_ context.Context, query string, limit int) ([]byte, error) {
			if query != "timeout" {
				t.Fatalf("unexpected query: %s", query)
			}
			if limit != 2 {
				t.Fatalf("unexpected limit: %d", limit)
			}
			return []byte(`{"logs":[{"_msg":"gateway timeout","trace_id":"t-1","service.name":"checkout","severity":"error"}],"traces":[{"operation":"POST /checkout","trace_id":"tr-2"}]}`), nil
		},
		fetch: func(context.Context, string, int) ([]byte, error) {
			t.Fatal("fetch should not be called when victoriaq is available")
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
	if res.Total != 2 {
		t.Fatalf("unexpected total: %d", res.Total)
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
		run: func(context.Context, string, int) ([]byte, error) {
			t.Fatal("run should not be called when victoriaq is unavailable")
			return nil, nil
		},
		fetch: func(_ context.Context, query string, limit int) ([]byte, error) {
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
	if len(res.Hits) != 1 {
		t.Fatalf("unexpected hit count: %d", len(res.Hits))
	}
	if got := res.Hits[0].ID; got != "r-1" {
		t.Fatalf("unexpected fallback id: %s", got)
	}
}

func TestVictoriaProviderSearchRequiresQuery(t *testing.T) {
	provider := NewVictoriaProvider()
	_, err := provider.Search(context.Background(), Query{Text: "   "})
	if err == nil || !strings.Contains(err.Error(), "query cannot be empty") {
		t.Fatalf("expected empty query error, got %v", err)
	}
}
