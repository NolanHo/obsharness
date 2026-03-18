package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultVictoriaURL = "http://127.0.0.1:9428"

// VictoriaProvider runs keyword search against a local Victoria setup.
type VictoriaProvider struct {
	run    func(ctx context.Context, query string, limit int) ([]byte, error)
	fetch  func(ctx context.Context, query string, limit int) ([]byte, error)
	lookup func(file string) (string, error)
}

// NewVictoriaProvider builds the default provider using local CLI and HTTP fallback.
func NewVictoriaProvider() VictoriaProvider {
	return VictoriaProvider{
		run:    runVictoriaQ,
		fetch:  fetchVictoriaLogs,
		lookup: exec.LookPath,
	}
}

// Search returns concise raw hits from Victoria logs/traces search output.
func (p VictoriaProvider) Search(ctx context.Context, in Query) (Result, error) {
	q := strings.TrimSpace(in.Text)
	if q == "" {
		return Result{}, fmt.Errorf("query cannot be empty")
	}
	if in.Limit <= 0 {
		in.Limit = 10
	}

	if p.lookup == nil {
		p.lookup = exec.LookPath
	}

	payload, err := p.searchPayload(ctx, q, in.Limit)
	if err != nil {
		return Result{}, err
	}

	hits := normalizeVictoriaHits(payload, in.Limit)
	return Result{Provider: "victoria", Query: q, Total: len(hits), Hits: hits}, nil
}

func (p VictoriaProvider) searchPayload(ctx context.Context, query string, limit int) ([]byte, error) {
	runner := p.run
	if runner == nil {
		runner = runVictoriaQ
	}
	fetcher := p.fetch
	if fetcher == nil {
		fetcher = fetchVictoriaLogs
	}

	if _, err := p.lookup("victoriaq"); err == nil {
		return runner(ctx, query, limit)
	}
	if envBin := strings.TrimSpace(os.Getenv("OBSH_VICTORIAQ_BIN")); envBin != "" {
		if _, err := os.Stat(envBin); err == nil {
			return runVictoriaQWithBin(ctx, envBin, query, limit)
		}
	}
	if _, err := os.Stat(filepath.Clean("bin/victoriaq")); err == nil {
		return runVictoriaQWithBin(ctx, "bin/victoriaq", query, limit)
	}

	return fetcher(ctx, query, limit)
}

func runVictoriaQ(ctx context.Context, query string, limit int) ([]byte, error) {
	return runVictoriaQWithBin(ctx, "victoriaq", query, limit)
}

func runVictoriaQWithBin(ctx context.Context, binPath, query string, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binPath, "q", query, "-k", strconv.Itoa(limit))
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}

	if exitErr, ok := err.(*exec.ExitError); ok {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			return nil, fmt.Errorf("victoriaq search failed: %s", stderr)
		}
	}
	return nil, fmt.Errorf("victoriaq search failed: %w", err)
}

func fetchVictoriaLogs(ctx context.Context, query string, limit int) ([]byte, error) {
	base := strings.TrimSpace(os.Getenv("VICTORIA_LOGS_URL"))
	if base == "" {
		base = defaultVictoriaURL
	}
	base = strings.TrimRight(base, "/")

	u, err := url.Parse(base + "/select/logsql/query")
	if err != nil {
		return nil, fmt.Errorf("invalid VICTORIA_LOGS_URL: %w", err)
	}
	params := u.Query()
	params.Set("query", query)
	params.Set("limit", strconv.Itoa(limit))
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build victoria logs request: %w", err)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("victoria logs request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("victoria logs request failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read victoria logs response: %w", err)
	}

	logs := parseLogLines(body)
	wrapped, err := json.Marshal(map[string]any{"logs": logs})
	if err != nil {
		return nil, fmt.Errorf("encode victoria logs response: %w", err)
	}
	return wrapped, nil
}

func normalizeVictoriaHits(payload []byte, limit int) []Hit {
	var parsed struct {
		Logs   []map[string]any `json:"logs"`
		Traces []map[string]any `json:"traces"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return []Hit{{Kind: "log", Title: strings.TrimSpace(string(payload)), Source: "victoria", ID: "raw-1"}}
	}

	hits := make([]Hit, 0, limit)
	for _, item := range parsed.Logs {
		hits = append(hits, mapVictoriaLogHit(item, len(hits)+1))
		if len(hits) >= limit {
			return hits
		}
	}
	for _, item := range parsed.Traces {
		hits = append(hits, mapVictoriaTraceHit(item, len(hits)+1))
		if len(hits) >= limit {
			return hits
		}
	}
	return hits
}

func mapVictoriaLogHit(item map[string]any, idx int) Hit {
	fields := map[string]string{}
	fillField(fields, "service.name", getString(item, "service.name"))
	fillField(fields, "severity", getString(item, "severity"))
	fillField(fields, "trace_id", getString(item, "trace_id"))
	fillField(fields, "request_id", getString(item, "request_id"))
	fillField(fields, "time", getString(item, "_time"))

	return Hit{
		Kind:   "log",
		Title:  firstNonEmpty(getString(item, "_msg"), getString(item, "message"), fmt.Sprintf("log hit %d", idx)),
		Source: "victorialogs",
		ID: firstNonEmpty(
			getString(item, "trace_id"),
			getString(item, "request_id"),
			getString(item, "_stream_id"),
			fmt.Sprintf("log-%d", idx),
		),
		Fields: fields,
	}
}

func mapVictoriaTraceHit(item map[string]any, idx int) Hit {
	fields := map[string]string{}
	fillField(fields, "service.name", getString(item, "service.name"))
	fillField(fields, "operation", getString(item, "operation"))
	fillField(fields, "trace_id", getString(item, "trace_id"))
	fillField(fields, "span_id", getString(item, "span_id"))
	fillField(fields, "time", getString(item, "_time"))

	return Hit{
		Kind:   "trace",
		Title:  firstNonEmpty(getString(item, "operation"), getString(item, "name"), fmt.Sprintf("trace hit %d", idx)),
		Source: "victoriatraces",
		ID: firstNonEmpty(
			getString(item, "trace_id"),
			getString(item, "span_id"),
			fmt.Sprintf("trace-%d", idx),
		),
		Fields: fields,
	}
}

func fillField(dst map[string]string, key, value string) {
	if strings.TrimSpace(value) != "" {
		dst[key] = value
	}
}

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func parseLogLines(payload []byte) []map[string]any {
	s := strings.TrimSpace(string(payload))
	if s == "" {
		return nil
	}

	if strings.HasPrefix(s, "[") {
		var logs []map[string]any
		if err := json.Unmarshal(payload, &logs); err == nil {
			return logs
		}
	}

	logs := make([]map[string]any, 0)
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err == nil {
			logs = append(logs, item)
		}
	}
	return logs
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

