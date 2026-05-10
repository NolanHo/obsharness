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
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultVictoriaLogsURL    = "http://127.0.0.1:9428"
	defaultVictoriaMetricsURL = "http://127.0.0.1:8428"
	defaultVictoriaTracesURL  = "http://127.0.0.1:10428"
)

// VictoriaProvider runs native queries against a local Victoria stack.
type VictoriaProvider struct {
	runSearch     func(context.Context, string, int) ([]byte, error)
	fetchSearch   func(context.Context, Query) ([]byte, error)
	fetchLogs     func(context.Context, LogsQuery) ([]byte, error)
	fetchTrace    func(context.Context, string) ([]byte, error)
	fetchMetrics  func(context.Context, MetricsQuery) ([]byte, error)
	fetchServices func(context.Context) ([]string, error)
	fetchURL      func(context.Context, string) ([]byte, error)
	lookup        func(string) (string, error)
}

func NewVictoriaProvider() VictoriaProvider {
	return VictoriaProvider{
		runSearch:     runVictoriaQ,
		fetchSearch:   fetchVictoriaSearch,
		fetchLogs:     fetchVictoriaLogsQuery,
		fetchTrace:    fetchVictoriaTrace,
		fetchMetrics:  fetchVictoriaMetrics,
		fetchServices: fetchVictoriaTraceServices,
		fetchURL:      fetchHTTP,
		lookup:        exec.LookPath,
	}
}

func (p VictoriaProvider) Search(ctx context.Context, in Query) (Result, error) {
	q := strings.TrimSpace(in.Text)
	if q == "" {
		return Result{}, fmt.Errorf("query cannot be empty")
	}
	if in.Limit <= 0 {
		in.Limit = 20
	}
	if p.lookup == nil {
		p.lookup = exec.LookPath
	}
	if parsed, ok := parseStableIDQuery(q); ok && p.shouldUseHTTPTracePivot() {
		return p.searchByStableID(ctx, parsed, in)
	}
	payload, err := p.searchPayload(ctx, in)
	if err != nil {
		return Result{}, err
	}
	hits := normalizeVictoriaHits(payload, in.Limit)
	return Result{Provider: "victoria", Query: q, Start: in.Start, End: in.End, Limit: in.Limit, Truncated: len(hits) >= in.Limit, Total: len(hits), Hits: hits}, nil
}

func (p VictoriaProvider) Logs(ctx context.Context, in LogsQuery) (LogsResult, error) {
	if in.Limit <= 0 {
		in.Limit = 200
	}
	fetcher := p.fetchLogs
	if fetcher == nil {
		fetcher = fetchVictoriaLogsQuery
	}
	payload, err := fetcher(ctx, in)
	if err != nil {
		return LogsResult{}, err
	}
	records := normalizeVictoriaLogs(payload, in.Limit)
	return LogsResult{Provider: "victoria", Source: "victorialogs", Start: in.Start, End: in.End, Limit: in.Limit, Truncated: len(records) >= in.Limit, Records: records}, nil
}

func (p VictoriaProvider) Trace(ctx context.Context, in TraceQuery) (TraceResult, error) {
	traceID := strings.TrimSpace(in.TraceID)
	if traceID == "" {
		return TraceResult{}, fmt.Errorf("trace id is required")
	}
	payload, err := p.fetchTracePayload(ctx, traceID)
	if err != nil {
		return TraceResult{}, err
	}
	res, err := parseVictoriaTrace(payload, traceID, true)
	if err != nil {
		return TraceResult{}, err
	}
	res.Provider = "victoria"
	return res, nil
}

func (p VictoriaProvider) Span(ctx context.Context, in SpanQuery) (SpanResult, error) {
	spanID := strings.TrimSpace(in.SpanID)
	if spanID == "" {
		return SpanResult{}, fmt.Errorf("span id is required")
	}
	if in.Limit <= 0 {
		in.Limit = 100
	}

	if traceID := strings.TrimSpace(in.TraceID); traceID != "" {
		payload, err := p.fetchTracePayload(ctx, traceID)
		if err != nil {
			return SpanResult{}, err
		}
		res, err := parseVictoriaSpan(payload, spanID)
		if err != nil {
			return SpanResult{}, err
		}
		res.Provider = "victoria"
		return res, nil
	}

	var lastErr error
	if traceID, err := p.lookupTraceIDForSpan(ctx, in); err == nil && traceID != "" {
		payload, err := p.fetchTracePayload(ctx, traceID)
		if err != nil {
			return SpanResult{}, err
		}
		res, err := parseVictoriaSpan(payload, spanID)
		if err != nil {
			return SpanResult{}, err
		}
		res.Provider = "victoria"
		return res, nil
	} else if err != nil {
		lastErr = err
	}

	services, err := p.spanLookupServices(ctx, in.Service)
	if err != nil {
		if lastErr != nil {
			return SpanResult{}, fmt.Errorf("span %q not found via logs (%v); pass --trace-id or --service, or increase --since/--limit", spanID, lastErr)
		}
		return SpanResult{}, err
	}
	for _, service := range services {
		payload, err := p.fetchTraceWindow(ctx, service, in, in.Limit)
		if err != nil {
			lastErr = err
			continue
		}
		res, found, err := parseVictoriaSpanFromTraceSearch(payload, spanID)
		if err != nil {
			lastErr = err
			continue
		}
		if found {
			res.Provider = "victoria"
			return res, nil
		}
	}
	if lastErr != nil {
		return SpanResult{}, fmt.Errorf("span %q not found after logs pivot failed (%v) and scanning %d trace service(s); pass --trace-id for exact lookup or widen --since/--limit", spanID, lastErr, len(services))
	}
	return SpanResult{}, fmt.Errorf("span %q not found; pass --trace-id for exact lookup or increase --since/--limit with --service", spanID)
}

func (p VictoriaProvider) Metrics(ctx context.Context, in MetricsQuery) (MetricsResult, error) {
	lang := strings.ToLower(strings.TrimSpace(in.Lang))
	if lang == "" {
		lang = "promql"
	}
	if lang != "promql" {
		return MetricsResult{}, fmt.Errorf("victoria metrics only supports lang=promql")
	}
	if strings.TrimSpace(in.Expr) == "" {
		return MetricsResult{}, fmt.Errorf("metric expression is required")
	}
	fetcher := p.fetchMetrics
	if fetcher == nil {
		fetcher = fetchVictoriaMetrics
	}
	payload, err := fetcher(ctx, in)
	if err != nil {
		return MetricsResult{}, err
	}
	res, err := parseVictoriaMetrics(payload, in)
	if err != nil {
		return MetricsResult{}, err
	}
	res.Provider = "victoria"
	return res, nil
}

func (p VictoriaProvider) searchPayload(ctx context.Context, in Query) ([]byte, error) {
	runner := p.runSearch
	if runner == nil {
		runner = runVictoriaQ
	}
	fetcher := p.fetchSearch
	if fetcher == nil {
		fetcher = fetchVictoriaSearch
	}
	if _, err := p.lookup("victoriaq"); err == nil {
		return runner(ctx, in.Text, in.Limit)
	}
	if envBin := strings.TrimSpace(os.Getenv("OBSH_VICTORIAQ_BIN")); envBin != "" {
		if _, err := os.Stat(envBin); err == nil {
			return runVictoriaQWithBin(ctx, envBin, in.Text, in.Limit)
		}
	}
	if _, err := os.Stat(filepath.Clean("bin/victoriaq")); err == nil {
		return runVictoriaQWithBin(ctx, "bin/victoriaq", in.Text, in.Limit)
	}
	return fetcher(ctx, in)
}

func (p VictoriaProvider) shouldUseHTTPTracePivot() bool {
	if strings.TrimSpace(os.Getenv("OBSH_FORCE_VICTORIAQ")) == "1" {
		return false
	}
	if strings.TrimSpace(os.Getenv("VICTORIA_TRACES_URL")) != "" {
		return true
	}
	if _, err := p.lookup("victoriaq"); err == nil {
		return false
	}
	return true
}

type stableIDQuery struct {
	Field string
	Value string
}

func parseStableIDQuery(query string) (stableIDQuery, bool) {
	query = strings.TrimSpace(query)
	if query == "" || strings.ContainsAny(query, " \t\r\n") {
		return stableIDQuery{}, false
	}
	key, value, ok := strings.Cut(query, "=")
	if !ok {
		key, value, ok = strings.Cut(query, ":")
	}
	if !ok {
		return stableIDQuery{}, false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	switch key {
	case "trace_id", "traceid":
		key = "trace_id"
	case "span_id", "spanid":
		key = "span_id"
	case "request_id", "requestid", "req_id":
		key = "request_id"
	default:
		return stableIDQuery{}, false
	}
	if value == "" {
		return stableIDQuery{}, false
	}
	return stableIDQuery{Field: key, Value: value}, true
}

func (p VictoriaProvider) searchByStableID(ctx context.Context, parsed stableIDQuery, in Query) (Result, error) {
	switch parsed.Field {
	case "trace_id":
		trace, err := p.Trace(ctx, TraceQuery{TraceID: parsed.Value, Since: in.Since, Start: in.Start, End: in.End})
		if err != nil {
			return Result{}, err
		}
		hit := traceResultToHit(trace)
		return Result{Provider: "victoria", Query: in.Text, Start: in.Start, End: in.End, Limit: in.Limit, Total: 1, Hits: []Hit{hit}}, nil
	case "span_id":
		span, err := p.Span(ctx, SpanQuery{SpanID: parsed.Value, Since: in.Since, Start: in.Start, End: in.End, Limit: in.Limit})
		if err != nil {
			return Result{}, err
		}
		hit := spanResultToHit(span)
		return Result{Provider: "victoria", Query: in.Text, Start: in.Start, End: in.End, Limit: in.Limit, Total: 1, Hits: []Hit{hit}}, nil
	case "request_id":
		payload, err := p.searchTracesByTag(ctx, "request_id", parsed.Value, in)
		if err != nil {
			return Result{}, err
		}
		hits := normalizeVictoriaHits(payload, in.Limit)
		return Result{Provider: "victoria", Query: in.Text, Start: in.Start, End: in.End, Limit: in.Limit, Truncated: len(hits) >= in.Limit, Total: len(hits), Hits: hits}, nil
	default:
		return Result{}, fmt.Errorf("unsupported stable id field %q", parsed.Field)
	}
}

func traceResultToHit(trace TraceResult) Hit {
	root := firstTraceRoot(trace)
	title := root.Name
	if title == "" {
		title = "trace " + trace.TraceID
	}
	return Hit{Kind: "trace", Title: title, Source: trace.Source, ID: trace.TraceID, Fields: map[string]string{"trace_id": trace.TraceID, "root_span": trace.RootSpanID, "span_id": root.SpanID, "service": root.Service, "duration_ms": strconv.FormatInt(root.DurationMS, 10), "status": root.Status}}
}

func firstTraceRoot(trace TraceResult) TraceSpan {
	var first TraceSpan
	for _, span := range trace.Spans {
		if first.SpanID == "" {
			first = span
		}
		if trace.RootSpanID != "" && span.SpanID == trace.RootSpanID {
			return span
		}
		if trace.RootSpanID == "" && span.ParentSpanID == "" {
			return span
		}
	}
	return first
}

func spanResultToHit(span SpanResult) Hit {
	return Hit{Kind: "trace", Title: span.Span.Name, Source: span.Source, ID: firstNonEmpty(span.TraceID, span.Span.SpanID), Fields: map[string]string{"trace_id": span.TraceID, "span_id": span.Span.SpanID, "root_span": span.Span.ParentSpanID, "service": span.Span.Service, "duration_ms": strconv.FormatInt(span.Span.DurationMS, 10), "status": span.Span.Status}}
}

func (p VictoriaProvider) fetchTracePayload(ctx context.Context, traceID string) ([]byte, error) {
	fetcher := p.fetchTrace
	if fetcher == nil {
		fetcher = fetchVictoriaTrace
	}
	return fetcher(ctx, traceID)
}

func (p VictoriaProvider) spanLookupServices(ctx context.Context, service string) ([]string, error) {
	if service = strings.TrimSpace(service); service != "" {
		return []string{service}, nil
	}
	services := victoriaTraceServicesFromEnv()
	if len(services) > 0 {
		return services, nil
	}
	fetcher := p.fetchServices
	if fetcher == nil {
		fetcher = fetchVictoriaTraceServices
	}
	services, err := fetcher(ctx)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("victoria span lookup requires a service; pass --service or set OBSH_TRACE_SERVICE")
	}
	return services, nil
}

func (p VictoriaProvider) lookupTraceIDForSpan(ctx context.Context, in SpanQuery) (string, error) {
	fetcher := p.fetchLogs
	if fetcher == nil {
		fetcher = fetchVictoriaLogsQuery
	}
	payload, err := fetcher(ctx, LogsQuery{
		Text:    "span_id=" + strings.TrimSpace(in.SpanID),
		Since:   in.Since,
		Start:   in.Start,
		End:     in.End,
		Service: in.Service,
		Limit:   5,
	})
	if err != nil {
		return "", err
	}
	for _, record := range normalizeVictoriaLogs(payload, 5) {
		if traceID := strings.TrimSpace(record.TraceID); traceID != "" {
			return traceID, nil
		}
	}
	return "", nil
}

func (p VictoriaProvider) searchTracesByTag(ctx context.Context, key, value string, in Query) ([]byte, error) {
	services := victoriaTraceServicesFromEnv()
	if len(services) == 0 {
		fetcher := p.fetchServices
		if fetcher == nil {
			fetcher = fetchVictoriaTraceServices
		}
		discovered, err := fetcher(ctx)
		if err != nil {
			return nil, err
		}
		services = discovered
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("victoria trace search requires a service; set OBSH_TRACE_SERVICE or VICTORIA_TRACE_SERVICE")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	var traces []map[string]any
	var lastErr error
	for _, service := range services {
		payload, err := p.fetchTraceSearch(ctx, service, key, value, in, limit-len(traces))
		if err != nil {
			lastErr = err
			continue
		}
		parsed := parseJaegerTraces(payload)
		traces = append(traces, parsed...)
		if len(traces) >= limit {
			break
		}
	}
	if len(traces) == 0 && lastErr != nil {
		return nil, lastErr
	}
	if len(traces) > limit {
		traces = traces[:limit]
	}
	return json.Marshal(map[string]any{"traces": summarizeTraceHits(traces)})
}

func (p VictoriaProvider) fetchTraceWindow(ctx context.Context, service string, in SpanQuery, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = 100
	}
	u, err := url.Parse(victoriaJaegerAPIBase() + "/traces")
	if err != nil {
		return nil, fmt.Errorf("invalid VICTORIA_TRACES_URL: %w", err)
	}
	params := u.Query()
	params.Set("service", service)
	params.Set("limit", strconv.Itoa(limit))
	applyJaegerTimeParams(params, Query{Since: in.Since, Start: in.Start, End: in.End})
	u.RawQuery = params.Encode()
	fetcher := p.fetchURL
	if fetcher == nil {
		fetcher = fetchHTTP
	}
	return fetcher(ctx, u.String())
}

func (p VictoriaProvider) fetchTraceSearch(ctx context.Context, service, key, value string, in Query, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = 1
	}
	u, err := url.Parse(victoriaJaegerAPIBase() + "/traces")
	if err != nil {
		return nil, fmt.Errorf("invalid VICTORIA_TRACES_URL: %w", err)
	}
	tags, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		return nil, fmt.Errorf("encode trace tags: %w", err)
	}
	params := u.Query()
	params.Set("service", service)
	params.Set("tags", string(tags))
	params.Set("limit", strconv.Itoa(limit))
	applyJaegerTimeParams(params, in)
	u.RawQuery = params.Encode()
	fetcher := p.fetchURL
	if fetcher == nil {
		fetcher = fetchHTTP
	}
	return fetcher(ctx, u.String())
}

func victoriaTraceServicesFromEnv() []string {
	values := []string{
		os.Getenv("OBSH_TRACE_SERVICE"),
		os.Getenv("OBSH_TRACE_SERVICES"),
		os.Getenv("VICTORIA_TRACE_SERVICE"),
		os.Getenv("VICTORIA_TRACE_SERVICES"),
	}
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			service := strings.TrimSpace(part)
			if service == "" || seen[service] {
				continue
			}
			seen[service] = true
			out = append(out, service)
		}
	}
	return out
}

func applyJaegerTimeParams(params url.Values, in Query) {
	startText := strings.TrimSpace(in.Start)
	endText := strings.TrimSpace(in.End)
	if startText != "" || endText != "" {
		if start, ok := parseJaegerMicros(startText); ok {
			params.Set("start", strconv.FormatInt(start, 10))
		}
		if end, ok := parseJaegerMicros(endText); ok {
			params.Set("end", strconv.FormatInt(end, 10))
		}
		return
	}
	since := strings.TrimSpace(in.Since)
	if since == "" {
		return
	}
	d, err := time.ParseDuration(since)
	if err != nil {
		return
	}
	end := time.Now().UTC()
	params.Set("start", strconv.FormatInt(end.Add(-d).UnixMicro(), 10))
	params.Set("end", strconv.FormatInt(end.UnixMicro(), 10))
}

func applyVictoriaLogsTimeParams(params url.Values, in LogsQuery) {
	startText := strings.TrimSpace(in.Start)
	endText := strings.TrimSpace(in.End)
	if startText != "" || endText != "" {
		if startText != "" {
			params.Set("start", startText)
		}
		if endText != "" {
			params.Set("end", endText)
		}
		return
	}
	since := strings.TrimSpace(in.Since)
	if since == "" {
		return
	}
	d, err := time.ParseDuration(since)
	if err != nil {
		return
	}
	end := time.Now().UTC()
	params.Set("start", end.Add(-d).Format(time.RFC3339Nano))
	params.Set("end", end.Format(time.RFC3339Nano))
}

func parseJaegerMicros(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC().UnixMicro(), true
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	switch {
	case n > 1e17:
		return n / 1_000, true
	case n > 1e14:
		return n, true
	case n > 1e11:
		return n * 1_000, true
	default:
		return n * 1_000_000, true
	}
}

func fetchVictoriaTraceServices(ctx context.Context) ([]string, error) {
	payload, err := fetchHTTP(ctx, victoriaJaegerAPIBase()+"/services")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("decode trace services response: %w", err)
	}
	out := make([]string, 0, len(parsed.Data))
	for _, service := range parsed.Data {
		service = strings.TrimSpace(service)
		if service != "" {
			out = append(out, service)
		}
	}
	return out, nil
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
		if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
			return nil, fmt.Errorf("victoriaq search failed: %s", stderr)
		}
	}
	return nil, fmt.Errorf("victoriaq search failed: %w", err)
}

func fetchVictoriaSearch(ctx context.Context, in Query) ([]byte, error) {
	payload, err := fetchVictoriaLogsQuery(ctx, LogsQuery{Text: in.Text, Since: in.Since, Start: in.Start, End: in.End, Limit: in.Limit})
	if err != nil {
		return nil, err
	}
	wrapped, err := json.Marshal(map[string]any{"logs": parseLogLines(payload)})
	if err != nil {
		return nil, fmt.Errorf("encode victoria search response: %w", err)
	}
	return wrapped, nil
}

func fetchVictoriaLogsQuery(ctx context.Context, in LogsQuery) ([]byte, error) {
	base := strings.TrimSpace(os.Getenv("VICTORIA_LOGS_URL"))
	if base == "" {
		base = defaultVictoriaLogsURL
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + "/select/logsql/query")
	if err != nil {
		return nil, fmt.Errorf("invalid VICTORIA_LOGS_URL: %w", err)
	}
	params := u.Query()
	params.Set("query", buildVictoriaLogsQuery(in))
	if in.Limit > 0 {
		params.Set("limit", strconv.Itoa(in.Limit))
	}
	applyVictoriaLogsTimeParams(params, in)
	u.RawQuery = params.Encode()
	return fetchHTTP(ctx, u.String())
}

func fetchVictoriaTrace(ctx context.Context, traceID string) ([]byte, error) {
	return fetchHTTP(ctx, victoriaJaegerAPIBase()+"/traces/"+url.PathEscape(traceID))
}

func fetchVictoriaMetrics(ctx context.Context, in MetricsQuery) ([]byte, error) {
	base := strings.TrimSpace(os.Getenv("VICTORIA_METRICS_URL"))
	if base == "" {
		base = defaultVictoriaMetricsURL
	}
	base = strings.TrimRight(base, "/")
	useRange := strings.TrimSpace(in.Start) != "" || strings.TrimSpace(in.End) != "" || strings.TrimSpace(in.Since) != ""
	path := "/api/v1/query"
	if useRange {
		path = "/api/v1/query_range"
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return nil, fmt.Errorf("invalid VICTORIA_METRICS_URL: %w", err)
	}
	params := u.Query()
	params.Set("query", in.Expr)
	if useRange {
		start, end := resolveMetricsRange(in)
		params.Set("start", strconv.FormatInt(start.Unix(), 10))
		params.Set("end", strconv.FormatInt(end.Unix(), 10))
		params.Set("step", firstNonEmpty(strings.TrimSpace(in.Step), "60s"))
	}
	u.RawQuery = params.Encode()
	return fetchHTTP(ctx, u.String())
}

func victoriaJaegerAPIBase() string {
	base := strings.TrimSpace(os.Getenv("VICTORIA_TRACES_URL"))
	if base == "" {
		base = defaultVictoriaTracesURL
	}
	base = strings.TrimRight(base, "/")
	switch {
	case strings.HasSuffix(base, "/select/jaeger/api"):
		return base
	case strings.HasSuffix(base, "/select/jaeger"):
		return base + "/api"
	case strings.HasSuffix(base, "/api"):
		return base
	default:
		return base + "/select/jaeger/api"
	}
}

func fetchHTTP(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("request failed: status %d url=%s body=%s", resp.StatusCode, rawURL, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return body, nil
}

func normalizeVictoriaHits(payload []byte, limit int) []Hit {
	var parsed struct {
		Logs    []map[string]any `json:"logs"`
		Traces  []map[string]any `json:"traces"`
		Metrics []map[string]any `json:"metrics"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return []Hit{{Kind: "log", Title: strings.TrimSpace(string(payload)), Source: "victoria", ID: "raw-1"}}
	}
	hits := make([]Hit, 0, limit)
	appendHit := func(hit Hit) bool {
		hits = append(hits, hit)
		return len(hits) >= limit
	}
	for i, item := range parsed.Logs {
		if appendHit(mapVictoriaLogHit(item, i+1)) {
			return hits
		}
	}
	for i, item := range parsed.Traces {
		if appendHit(mapVictoriaTraceHit(item, len(hits)+i+1)) {
			return hits
		}
	}
	for i, item := range parsed.Metrics {
		if appendHit(mapVictoriaMetricHit(item, len(hits)+i+1)) {
			return hits
		}
	}
	return hits
}

func normalizeVictoriaLogs(payload []byte, limit int) []LogRecord {
	items := parseLogLines(payload)
	records := make([]LogRecord, 0, len(items))
	for _, item := range items {
		records = append(records, mapVictoriaLogRecord(item))
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return records
}

func parseJaegerTraces(payload []byte) []map[string]any {
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err == nil {
		return parsed.Data
	}
	return nil
}

func summarizeTraceHits(traces []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(traces))
	for _, trace := range traces {
		out = append(out, summarizeTraceHit(trace))
	}
	return out
}

func summarizeTraceHit(trace map[string]any) map[string]any {
	traceID := firstNonEmpty(getString(trace, "traceID"), getString(trace, "trace_id"))
	spans, _ := trace["spans"].([]any)
	summary := map[string]any{"trace_id": traceID}
	if len(spans) > 0 {
		root := firstRootSpan(spans)
		summary["operation"] = firstNonEmpty(getString(root, "operationName"), getString(root, "name"))
		summary["span_id"] = firstNonEmpty(getString(root, "spanID"), getString(root, "span_id"))
		summary["duration_ms"] = strconv.FormatInt(durationToMS(root["duration"]), 10)
		summary["status"] = statusFromSpan(root)
	}
	return summary
}

func firstRootSpan(spans []any) map[string]any {
	var first map[string]any
	for _, raw := range spans {
		span, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if first == nil {
			first = span
		}
		if parentSpanID(span) == "" {
			return span
		}
	}
	if first == nil {
		return map[string]any{}
	}
	return first
}

func mapVictoriaLogHit(item map[string]any, idx int) Hit {
	fields := map[string]string{}
	fillField(fields, "service", firstNonEmpty(getString(item, "service"), getString(item, "service.name")))
	fillField(fields, "trace_id", getString(item, "trace_id"))
	fillField(fields, "span_id", getString(item, "span_id"))
	fillField(fields, "request_id", getString(item, "request_id"))
	fillField(fields, "time", firstNonEmpty(getString(item, "_time"), getString(item, "time"), getString(item, "timestamp")))
	return Hit{Kind: "log", Title: firstNonEmpty(getString(item, "_msg"), getString(item, "message"), fmt.Sprintf("log hit %d", idx)), Source: "victorialogs", ID: firstNonEmpty(getString(item, "span_id"), getString(item, "trace_id"), getString(item, "request_id"), fmt.Sprintf("log-%d", idx)), Fields: fields}
}

func mapVictoriaTraceHit(item map[string]any, idx int) Hit {
	fields := map[string]string{}
	fillField(fields, "trace_id", getString(item, "trace_id"))
	fillField(fields, "root_span", firstNonEmpty(getString(item, "root_span"), getString(item, "span_id")))
	fillField(fields, "duration_ms", firstNonEmpty(getString(item, "duration_ms"), getString(item, "duration")))
	fillField(fields, "status", firstNonEmpty(getString(item, "status"), getString(item, "status.code")))
	fillField(fields, "time", firstNonEmpty(getString(item, "_time"), getString(item, "time"), getString(item, "timestamp")))
	return Hit{Kind: "trace", Title: firstNonEmpty(getString(item, "operation"), getString(item, "name"), fmt.Sprintf("trace hit %d", idx)), Source: "victoriatraces", ID: firstNonEmpty(getString(item, "trace_id"), getString(item, "span_id"), fmt.Sprintf("trace-%d", idx)), Fields: fields}
}

func mapVictoriaMetricHit(item map[string]any, idx int) Hit {
	labels := map[string]string{}
	for key, value := range item {
		if key == "metric" || key == "name" || key == "value" || key == "time" || key == "timestamp" {
			continue
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			labels[key] = strings.TrimSpace(s)
		}
	}
	return Hit{Kind: "metric", Title: firstNonEmpty(getString(item, "metric"), getString(item, "name"), fmt.Sprintf("metric hit %d", idx)), Source: "victoriametrics", ID: firstNonEmpty(getString(item, "metric"), getString(item, "name"), fmt.Sprintf("metric-%d", idx)), Fields: map[string]string{"metric": firstNonEmpty(getString(item, "metric"), getString(item, "name")), "labels": formatFlatLabels(labels), "value": firstNonEmpty(getString(item, "value"), getString(item, "sample")), "time": firstNonEmpty(getString(item, "time"), getString(item, "timestamp"))}}
}

func mapVictoriaLogRecord(item map[string]any) LogRecord {
	return LogRecord{Time: firstNonEmpty(getString(item, "_time"), getString(item, "time"), getString(item, "timestamp")), Level: firstNonEmpty(getString(item, "severity"), getString(item, "level")), Service: firstNonEmpty(getString(item, "service"), getString(item, "service.name")), Operation: firstNonEmpty(getString(item, "operation"), getString(item, "op")), TraceID: getString(item, "trace_id"), SpanID: getString(item, "span_id"), RequestID: getString(item, "request_id"), Message: firstNonEmpty(getString(item, "_msg"), getString(item, "message"), strings.TrimSpace(asJSON(item))), Raw: item}
}

func parseVictoriaTrace(payload []byte, traceID string, hide bool) (TraceResult, error) {
	trace, err := decodeTracePayload(payload)
	if err != nil {
		return TraceResult{}, err
	}
	if trace.TraceID == "" {
		trace.TraceID = traceID
	}
	res := TraceResult{Source: "victoriatraces", TraceID: trace.TraceID, RootSpanID: trace.RootSpanID}
	for _, span := range trace.Spans {
		if hide {
			span.AttrsHidden = len(span.Attrs) > 0
			span.EventsHidden = len(span.Events) > 0
			span.Attrs = nil
			span.Events = nil
		}
		if span.Status != "" && span.Status != "ok" {
			res.ErrorCount++
		}
		res.Spans = append(res.Spans, span)
	}
	res.SpanCount = len(res.Spans)
	return res, nil
}

func parseVictoriaSpan(payload []byte, spanID string) (SpanResult, error) {
	if span, traceID, ok := decodeDirectSpan(payload); ok {
		if span.SpanID == "" {
			span.SpanID = spanID
		}
		return SpanResult{Source: "victoriatraces", TraceID: traceID, Span: span}, nil
	}
	trace, err := decodeTracePayload(payload)
	if err != nil {
		return SpanResult{}, err
	}
	for _, span := range trace.Spans {
		if span.SpanID == spanID {
			return SpanResult{Source: "victoriatraces", TraceID: trace.TraceID, Span: span}, nil
		}
	}
	return SpanResult{}, fmt.Errorf("span %q not found", spanID)
}

func parseVictoriaSpanFromTraceSearch(payload []byte, spanID string) (SpanResult, bool, error) {
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return SpanResult{}, false, fmt.Errorf("decode trace search response: %w", err)
	}
	for _, raw := range parsed.Data {
		wrapped, err := json.Marshal(map[string]any{"data": []json.RawMessage{raw}})
		if err != nil {
			return SpanResult{}, false, fmt.Errorf("encode trace candidate: %w", err)
		}
		res, err := parseVictoriaSpan(wrapped, spanID)
		if err == nil {
			return res, true, nil
		}
		if !strings.Contains(err.Error(), "not found") {
			return SpanResult{}, false, err
		}
	}
	return SpanResult{}, false, nil
}

func parseVictoriaMetrics(payload []byte, in MetricsQuery) (MetricsResult, error) {
	var parsed struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return MetricsResult{}, fmt.Errorf("decode metrics response: %w", err)
	}
	res := MetricsResult{Expr: strings.TrimSpace(in.Expr), Start: strings.TrimSpace(in.Start), End: strings.TrimSpace(in.End), Step: strings.TrimSpace(in.Step)}
	for _, series := range parsed.Data.Result {
		name := firstNonEmpty(series.Metric["__name__"], res.Expr)
		labels := cloneLabels(series.Metric)
		delete(labels, "__name__")
		if len(series.Value) == 2 {
			res.Samples = append(res.Samples, MetricSample{Metric: name, Labels: labels, Value: fmt.Sprint(series.Value[1]), Timestamp: toUnixMillis(series.Value[0])})
		}
		for _, pair := range series.Values {
			if len(pair) == 2 {
				res.Samples = append(res.Samples, MetricSample{Metric: name, Labels: labels, Value: fmt.Sprint(pair[1]), Timestamp: toUnixMillis(pair[0])})
			}
		}
	}
	if res.Start == "" && strings.TrimSpace(in.Since) != "" {
		start, end := resolveMetricsRange(in)
		res.Start = strconv.FormatInt(start.Unix(), 10)
		res.End = strconv.FormatInt(end.Unix(), 10)
		if res.Step == "" {
			res.Step = firstNonEmpty(strings.TrimSpace(in.Step), "60s")
		}
	}
	return res, nil
}

type decodedTrace struct {
	TraceID    string
	RootSpanID string
	Spans      []TraceSpan
}

func decodeTracePayload(payload []byte) (decodedTrace, error) {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return decodedTrace{}, fmt.Errorf("decode trace response: %w", err)
	}
	if data, ok := root["data"].([]any); ok && len(data) > 0 {
		if first, ok := data[0].(map[string]any); ok {
			root = first
		}
	}
	spansRaw, ok := root["spans"].([]any)
	if !ok || len(spansRaw) == 0 {
		return decodedTrace{}, fmt.Errorf("decode trace response: unsupported payload")
	}
	processes := map[string]map[string]any{}
	if raw, ok := root["processes"].(map[string]any); ok {
		for key, value := range raw {
			if m, ok := value.(map[string]any); ok {
				processes[key] = m
			}
		}
	}
	trace := decodedTrace{TraceID: getString(root, "traceID")}
	for _, raw := range spansRaw {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		span := TraceSpan{SpanID: firstNonEmpty(getString(m, "spanID"), getString(m, "span_id")), Name: firstNonEmpty(getString(m, "operationName"), getString(m, "name")), DurationMS: durationToMS(m["duration"])}
		span.ParentSpanID = parentSpanID(m)
		span.Service = firstNonEmpty(serviceFromProcess(processes, getString(m, "processID")), tagValue(m, "service.name"), getString(m, "service"))
		span.Status = statusFromSpan(m)
		span.Attrs = tagsToMap(m)
		span.Events = logsToEvents(m)
		if trace.TraceID == "" {
			trace.TraceID = firstNonEmpty(getString(m, "traceID"), getString(m, "trace_id"))
		}
		if trace.RootSpanID == "" && span.ParentSpanID == "" {
			trace.RootSpanID = span.SpanID
		}
		trace.Spans = append(trace.Spans, span)
	}
	if trace.RootSpanID == "" && len(trace.Spans) > 0 {
		trace.RootSpanID = trace.Spans[0].SpanID
	}
	return trace, nil
}

func decodeDirectSpan(payload []byte) (TraceSpan, string, bool) {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return TraceSpan{}, "", false
	}
	if span, ok := root["span"].(map[string]any); ok {
		root = span
	}
	spanID := firstNonEmpty(getString(root, "spanID"), getString(root, "span_id"))
	if spanID == "" {
		return TraceSpan{}, "", false
	}
	span := TraceSpan{
		SpanID:       spanID,
		ParentSpanID: firstNonEmpty(getString(root, "parentSpanID"), getString(root, "parent_span_id")),
		Name:         firstNonEmpty(getString(root, "operationName"), getString(root, "name")),
		Service:      firstNonEmpty(getString(root, "service"), tagValue(root, "service.name")),
		Status:       statusFromSpan(root),
		DurationMS:   durationToMS(root["duration"]),
		Attrs:        tagsToMap(root),
		Events:       logsToEvents(root),
	}
	return span, firstNonEmpty(getString(root, "traceID"), getString(root, "trace_id")), true
}

func parentSpanID(span map[string]any) string {
	refs, ok := span["references"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range refs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if refType := strings.ToUpper(getString(m, "refType")); refType == "CHILD_OF" || refType == "" {
			return firstNonEmpty(getString(m, "spanID"), getString(m, "span_id"))
		}
	}
	return ""
}

func serviceFromProcess(processes map[string]map[string]any, processID string) string {
	if processID == "" {
		return ""
	}
	return getString(processes[processID], "serviceName")
}

func statusFromSpan(span map[string]any) string {
	if v := tagValue(span, "otel.status_code"); v != "" {
		return strings.ToLower(v)
	}
	if v := tagValue(span, "status.code"); v != "" {
		return strings.ToLower(v)
	}
	if v := tagValue(span, "error"); strings.EqualFold(v, "true") {
		return "error"
	}
	return "ok"
}

func tagsToMap(span map[string]any) map[string]string {
	tags, ok := span["tags"].([]any)
	if !ok || len(tags) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, raw := range tags {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := getString(m, "key")
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(fmt.Sprint(m["value"]))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func tagValue(span map[string]any, key string) string {
	return tagsToMap(span)[key]
}

func logsToEvents(span map[string]any) []SpanEvent {
	logs, ok := span["logs"].([]any)
	if !ok || len(logs) == 0 {
		return nil
	}
	out := make([]SpanEvent, 0, len(logs))
	for _, raw := range logs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		event := SpanEvent{Time: timeFromAny(m["timestamp"]), Name: "event"}
		if fields, ok := m["fields"].([]any); ok {
			event.Fields = map[string]string{}
			for _, fraw := range fields {
				f, ok := fraw.(map[string]any)
				if !ok {
					continue
				}
				key := getString(f, "key")
				if key == "" {
					continue
				}
				value := strings.TrimSpace(fmt.Sprint(f["value"]))
				event.Fields[key] = value
				if key == "event" || key == "name" {
					event.Name = value
				}
			}
		}
		out = append(out, event)
	}
	return out
}

func buildVictoriaLogsQuery(in LogsQuery) string {
	parts := []string{}
	appendPart := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if key == "" {
			parts = append(parts, normalizeVictoriaLogsFreeText(value))
			return
		}
		parts = append(parts, key+":"+quoteVictoriaLogsValue(value))
	}
	appendPart("", in.Text)
	appendPart("service.name", in.Service)
	appendPart("operation", in.Operation)
	appendPart("trace_id", in.TraceID)
	appendPart("request_id", in.RequestID)
	if len(parts) == 0 {
		return "*"
	}
	return strings.Join(parts, " ")
}

func normalizeVictoriaLogsFreeText(value string) string {
	if parsed, ok := parseStableIDQuery(value); ok {
		return parsed.Field + ":" + quoteVictoriaLogsValue(parsed.Value)
	}
	return value
}

func quoteVictoriaLogsValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value
	}
	return strconv.Quote(value)
}

func resolveMetricsRange(in MetricsQuery) (time.Time, time.Time) {
	end := time.Now().UTC()
	if strings.TrimSpace(in.End) != "" {
		if parsed, err := time.Parse(time.RFC3339, in.End); err == nil {
			end = parsed.UTC()
		}
	}
	if strings.TrimSpace(in.Start) != "" {
		if parsed, err := time.Parse(time.RFC3339, in.Start); err == nil {
			return parsed.UTC(), end
		}
	}
	d := 30 * time.Minute
	if strings.TrimSpace(in.Since) != "" {
		if parsed, err := time.ParseDuration(in.Since); err == nil {
			d = parsed
		}
	}
	return end.Add(-d), end
}

func durationToMS(v any) int64 {
	switch x := v.(type) {
	case float64:
		if x > 1_000_000 {
			return int64(x / 1000)
		}
		return int64(x)
	case int64:
		if x > 1_000_000 {
			return x / 1000
		}
		return x
	case json.Number:
		f, _ := x.Float64()
		return durationToMS(f)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return durationToMS(f)
		}
	}
	return 0
}

func timeFromAny(v any) string {
	ms := toUnixMillis(v)
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func toUnixMillis(v any) int64 {
	switch x := v.(type) {
	case float64:
		if x > 1e12 {
			return int64(x)
		}
		return int64(x * 1000)
	case int64:
		if x > 1e12 {
			return x
		}
		return x * 1000
	case json.Number:
		f, _ := x.Float64()
		return toUnixMillis(f)
	case string:
		x = strings.TrimSpace(x)
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t.UnixMilli()
		}
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return toUnixMillis(f)
		}
	}
	return 0
}

func cloneLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func formatFlatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

func fillField(dst map[string]string, key, value string) {
	if strings.TrimSpace(value) != "" {
		dst[key] = value
	}
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
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

func asJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
