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
		{Kind: "log", Title: "timeout while calling checkout", Source: "victorialogs", ID: "log-1001", Fields: map[string]string{"service": "checkout", "level": "error"}},
		{Kind: "trace", Title: "checkout request exceeded SLO", Source: "victoriatraces", ID: "trace-2001", Fields: map[string]string{"operation": "POST /checkout", "status": "deadline_exceeded"}},
		{Kind: "metric", Title: "checkout error ratio elevated", Source: "victoriametrics", ID: "metric-3001", Fields: map[string]string{"metric": "http_errors_total", "window": "5m"}},
	}

	if in.Limit < len(hits) {
		hits = hits[:in.Limit]
	}

	return Result{Provider: "mock", Query: q, Total: len(hits), Hits: hits}, nil
}
