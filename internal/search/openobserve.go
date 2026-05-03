package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOpenObserveBaseURL = "http://127.0.0.1:5080"
	defaultOpenObserveOrg     = "default"
	defaultOpenObserveStream  = "default"
)

// OpenObserveProvider queries an OpenObserve instance using its HTTP APIs.
//
// Observed public docs endpoints (2026-05):
// - Search: POST /api/{org}/_search (SQL)
// - Around: GET /api/{org}/{stream}/_around?key={timestamp}&size=N
// - Values: GET /api/{org}/{stream}/_values?... (not used here)
// - Traces latest: GET /api/{org}/{stream}/traces/latest?start_time=...&end_time=...&from=0&size=...
//
// Config via env:
// - OPENOBSERVE_URL (base URL, e.g. http://localhost:5080)
// - OPENOBSERVE_ORG (organization, default "default")
// - OPENOBSERVE_LOGS_STREAM (default "default")
// - OPENOBSERVE_TRACES_STREAM (default "default")
// - OPENOBSERVE_USERNAME / OPENOBSERVE_PASSWORD (basic auth)
// - OPENOBSERVE_AUTH_HEADER (verbatim Authorization header; overrides username/password)
//
// Note: OpenObserve search uses SQL over a stream. Metrics access is mapped onto SQL
// against a metrics stream, not PromQL.
// obsh CLI still accepts PromQL expressions, so Metrics() will return an error until
// the contract is changed or a PromQL-compatible endpoint is configured.
type OpenObserveProvider struct {
	client *http.Client
}

func NewOpenObserveProvider() OpenObserveProvider {
	return OpenObserveProvider{client: &http.Client{Timeout: 15 * time.Second}}
}

func (p OpenObserveProvider) Search(ctx context.Context, in Query) (Result, error) {
	q := strings.TrimSpace(in.Text)
	if q == "" {
		return Result{}, fmt.Errorf("query cannot be empty")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}

	org := openObserveOrg()
	stream := openObserveLogsStream()

	startUS, endUS, err := resolveTimeRangeMicros(in.Since, in.Start, in.End)
	if err != nil {
		return Result{}, err
	}

	// Very conservative translation: treat Search text as a full-text match against log column.
	// Users can switch to `obsh logs` for structured filters.
	//
	// OpenObserve docs show the raw log line often under a `log` field.
	sql := fmt.Sprintf("SELECT * FROM %s WHERE %s ORDER BY _timestamp DESC", stream, buildO2ContainsClause(q))

	payload, err := o2Search(ctx, p.httpClient(), org, sql, startUS, endUS, 0, limit)
	if err != nil {
		return Result{}, err
	}

	hits, total := normalizeOpenObserveSearchHits(payload, limit)
	return Result{Provider: "openobserve", Query: q, Start: in.Start, End: in.End, Limit: limit, Truncated: len(hits) >= limit, Total: total, Hits: hits}, nil
}

func (p OpenObserveProvider) Logs(ctx context.Context, in LogsQuery) (LogsResult, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}

	org := openObserveOrg()
	stream := openObserveLogsStream()

	startUS, endUS, err := resolveTimeRangeMicros(in.Since, in.Start, in.End)
	if err != nil {
		return LogsResult{}, err
	}

	conds := make([]string, 0, 8)
	if strings.TrimSpace(in.Text) != "" {
		conds = append(conds, buildO2ContainsClause(in.Text))
	}
	if strings.TrimSpace(in.Service) != "" {
		conds = append(conds, fmt.Sprintf("service_name = %s", o2Quote(in.Service)))
	}
	if strings.TrimSpace(in.Operation) != "" {
		conds = append(conds, fmt.Sprintf("operation_name = %s", o2Quote(in.Operation)))
	}
	if strings.TrimSpace(in.TraceID) != "" {
		conds = append(conds, fmt.Sprintf("trace_id = %s", o2Quote(in.TraceID)))
	}
	if strings.TrimSpace(in.RequestID) != "" {
		// Not sure which field name exists. Keep broad matching.
		conds = append(conds, fmt.Sprintf("(%s)", strings.Join([]string{
			fmt.Sprintf("request_id = %s", o2Quote(in.RequestID)),
			fmt.Sprintf("req_id = %s", o2Quote(in.RequestID)),
			fmt.Sprintf("http.request_id = %s", o2Quote(in.RequestID)),
		}, " OR ")))
	}

	where := "1=1"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}

	sql := fmt.Sprintf("SELECT * FROM %s WHERE %s ORDER BY _timestamp DESC", stream, where)

	payload, err := o2Search(ctx, p.httpClient(), org, sql, startUS, endUS, 0, limit)
	if err != nil {
		return LogsResult{}, err
	}

	records, total := normalizeOpenObserveLogRecords(payload, limit)
	return LogsResult{Provider: "openobserve", Source: "openobserve", Start: formatMicrosAsRFC3339(startUS), End: formatMicrosAsRFC3339(endUS), Limit: limit, Truncated: total > limit, Records: records}, nil
}

func (p OpenObserveProvider) Trace(ctx context.Context, traceID string) (TraceResult, error) {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return TraceResult{}, fmt.Errorf("trace id is required")
	}

	org := openObserveOrg()
	stream := openObserveTracesStream()

	// Use a bounded lookback. Prefer explicit inputs in the CLI; here we do a short window.
	endUS := time.Now().UnixMicro()
	startUS := endUS - int64(30*time.Minute/time.Microsecond)

	u := strings.TrimRight(openObserveBaseURL(), "/") + "/api/" + url.PathEscape(org) + "/" + url.PathEscape(stream) + "/traces/latest"
	params := url.Values{}
	params.Set("filter", fmt.Sprintf("trace_id=%s", o2Quote(traceID)))
	params.Set("start_time", strconv.FormatInt(startUS, 10))
	params.Set("end_time", strconv.FormatInt(endUS, 10))
	params.Set("from", "0")
	params.Set("size", "25")
	u = u + "?" + params.Encode()

	payload, err := o2GET(ctx, p.httpClient(), u)
	if err != nil {
		return TraceResult{}, err
	}

	// OpenObserve doesn't expose a simple trace-tree endpoint in the docs page.
	// We return a minimal TraceResult with a single pseudo-span summarizing the trace.
	meta, err := parseOpenObserveTraceLatest(payload)
	if err != nil {
		return TraceResult{}, err
	}

	spans := []TraceSpan{{
		SpanID:     meta.TraceID,
		Name:       firstNonEmpty(meta.OperationName, "trace"),
		Service:    meta.ServiceName,
		Status:     meta.Status,
		DurationMS: meta.DurationMS,
		Attrs:      map[string]string{"note": "openobserve trace latest summary only"},
	}}
	spanCount := meta.SpanCount
	if spanCount == 0 {
		spanCount = 1
	}
	return TraceResult{Provider: "openobserve", Source: "openobserve", TraceID: meta.TraceID, RootSpanID: meta.TraceID, SpanCount: spanCount, ErrorCount: 0, Spans: spans}, nil
}

func (p OpenObserveProvider) Span(ctx context.Context, spanID string) (SpanResult, error) {
	spanID = strings.TrimSpace(spanID)
	if spanID == "" {
		return SpanResult{}, fmt.Errorf("span id is required")
	}
	return SpanResult{}, fmt.Errorf("openobserve provider does not implement span lookup; use logs search or trace latest")
}

func (p OpenObserveProvider) Metrics(ctx context.Context, in MetricsQuery) (MetricsResult, error) {
	lang := strings.ToLower(strings.TrimSpace(in.Lang))
	if lang == "" {
		lang = "promql"
	}

	expr := strings.TrimSpace(in.Expr)
	if expr == "" {
		return MetricsResult{}, fmt.Errorf("metric expression is required")
	}

	org := openObserveOrg()
	startUS, endUS, err := resolveTimeRangeMicros(in.Since, in.Start, in.End)
	if err != nil {
		return MetricsResult{}, err
	}

	if lang == "" || lang == "promql" {
		payload, err := o2PromQueryRange(ctx, p.httpClient(), org, expr, startUS, endUS, in.Step)
		if err != nil {
			return MetricsResult{}, err
		}
		samples, truncated, err := normalizeOpenObservePromSamples(payload)
		if err != nil {
			return MetricsResult{}, err
		}
		return MetricsResult{Provider: "openobserve", Expr: expr, Start: formatMicrosAsRFC3339(startUS), End: formatMicrosAsRFC3339(endUS), Step: in.Step, Truncated: truncated, Samples: samples}, nil
	}

	if lang != "sql" {
		return MetricsResult{}, fmt.Errorf("unsupported openobserve metrics language %q", lang)
	}

	stream := strings.TrimSpace(in.Stream)
	if stream == "" {
		stream = strings.TrimSpace(os.Getenv("OPENOBSERVE_METRICS_STREAM"))
	}
	if stream == "" {
		return MetricsResult{}, fmt.Errorf("openobserve metrics requires stream name (set --stream or OPENOBSERVE_METRICS_STREAM)")
	}

	payload, err := o2Search(ctx, p.httpClient(), org, expr, startUS, endUS, 0, 5000)
	if err != nil {
		return MetricsResult{}, err
	}

	samples, truncated := normalizeOpenObserveMetricSamples(payload)
	return MetricsResult{Provider: "openobserve", Expr: expr, Start: formatMicrosAsRFC3339(startUS), End: formatMicrosAsRFC3339(endUS), Step: in.Step, Truncated: truncated, Samples: samples}, nil
}

func (p OpenObserveProvider) httpClient() *http.Client {
	if p.client != nil {
		return p.client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

type o2SearchRequest struct {
	Query struct {
		SQL       string `json:"sql"`
		StartTime int64  `json:"start_time"`
		EndTime   int64  `json:"end_time"`
		From      int    `json:"from"`
		Size      int    `json:"size"`
	} `json:"query"`
	SearchType string `json:"search_type"`
	Timeout    int    `json:"timeout"`
}

type o2SearchResponse struct {
	Took int              `json:"took"`
	Hits []map[string]any `json:"hits"`
	// Some deployments may include total in different shapes. Keep raw.
	Total any `json:"total"`
}

func o2Search(ctx context.Context, client *http.Client, org, sql string, startUS, endUS int64, from, size int) ([]byte, error) {
	reqBody := o2SearchRequest{}
	reqBody.Query.SQL = sql
	reqBody.Query.StartTime = startUS
	reqBody.Query.EndTime = endUS
	reqBody.Query.From = from
	reqBody.Query.Size = size
	reqBody.SearchType = "ui"
	reqBody.Timeout = 0

	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode openobserve search request: %w", err)
	}

	u := strings.TrimRight(openObserveBaseURL(), "/") + "/api/" + url.PathEscape(org) + "/_search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	applyOpenObserveAuth(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed: status %d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func o2GET(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	applyOpenObserveAuth(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed: status %d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func o2PromQueryRange(ctx context.Context, client *http.Client, org, expr string, startUS, endUS int64, step string) ([]byte, error) {
	step = strings.TrimSpace(step)
	if step == "" {
		step = "60s"
	}
	start := fmt.Sprintf("%.6f", float64(startUS)/1_000_000)
	end := fmt.Sprintf("%.6f", float64(endUS)/1_000_000)
	u := strings.TrimRight(openObserveBaseURL(), "/") + "/api/" + url.PathEscape(org) + "/prometheus/api/v1/query_range"
	params := url.Values{}
	params.Set("query", expr)
	params.Set("start", start)
	params.Set("end", end)
	params.Set("step", step)
	return o2GET(ctx, client, u+"?"+params.Encode())
}

type o2PromResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func normalizeOpenObservePromSamples(payload []byte) ([]MetricSample, bool, error) {
	var parsed o2PromResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, false, fmt.Errorf("decode openobserve prometheus response: %w", err)
	}
	if parsed.Status != "success" {
		if parsed.Error != "" {
			return nil, false, fmt.Errorf("openobserve prometheus query failed: %s", parsed.Error)
		}
		return nil, false, fmt.Errorf("openobserve prometheus query failed: status=%s", parsed.Status)
	}

	samples := []MetricSample{}
	for _, result := range parsed.Data.Result {
		metric := firstNonEmpty(result.Metric["__name__"], result.Metric["name"])
		labels := map[string]string{}
		for k, v := range result.Metric {
			if k == "__name__" || strings.TrimSpace(v) == "" {
				continue
			}
			labels[k] = v
		}
		add := func(pair []any) {
			if len(pair) < 2 {
				return
			}
			ts, ok := promTimestampMillis(pair[0])
			if !ok {
				return
			}
			samples = append(samples, MetricSample{Metric: metric, Labels: labels, Value: fmt.Sprint(pair[1]), Timestamp: ts})
		}
		if len(result.Values) > 0 {
			for _, pair := range result.Values {
				add(pair)
			}
		} else if len(result.Value) > 0 {
			add(result.Value)
		}
	}
	return samples, false, nil
}

func promTimestampMillis(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x * 1000), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return int64(f * 1000), true
	default:
		return 0, false
	}
}

func normalizeOpenObserveSearchHits(payload []byte, limit int) ([]Hit, int) {
	var parsed o2SearchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return []Hit{{Kind: "log", Title: strings.TrimSpace(string(payload)), Source: "openobserve", ID: "raw-1"}}, 1
	}

	total := len(parsed.Hits)
	// best-effort total normalization
	switch v := parsed.Total.(type) {
	case float64:
		total = int(v)
	case int:
		total = v
	case map[string]any:
		if x, ok := v["value"].(float64); ok {
			total = int(x)
		}
	}

	hits := make([]Hit, 0, minInt(limit, len(parsed.Hits)))
	for i, item := range parsed.Hits {
		if len(hits) >= limit {
			break
		}
		fields := map[string]string{}
		fillField(fields, "time", firstNonEmpty(getString(item, "_time"), getString(item, "time"), getString(item, "timestamp")))
		fillField(fields, "service", firstNonEmpty(getString(item, "service"), getString(item, "service_name"), getString(item, "service.name")))
		fillField(fields, "trace_id", firstNonEmpty(getString(item, "trace_id"), getString(item, "traceId")))
		fillField(fields, "span_id", firstNonEmpty(getString(item, "span_id"), getString(item, "spanId")))
		fillField(fields, "request_id", firstNonEmpty(getString(item, "request_id"), getString(item, "req_id")))
		title := firstNonEmpty(getString(item, "message"), getString(item, "log"), getString(item, "body"), fmt.Sprintf("openobserve hit %d", i))
		id := firstNonEmpty(getString(item, "span_id"), getString(item, "trace_id"), fmt.Sprintf("hit-%d", i))
		hits = append(hits, Hit{Kind: "log", Title: title, Source: "openobserve", ID: id, Fields: fields})
	}
	return hits, total
}

func normalizeOpenObserveLogRecords(payload []byte, limit int) ([]LogRecord, int) {
	var parsed o2SearchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return []LogRecord{{Time: "", Message: strings.TrimSpace(string(payload))}}, 1
	}

	total := len(parsed.Hits)
	records := make([]LogRecord, 0, minInt(limit, len(parsed.Hits)))
	for _, item := range parsed.Hits {
		if len(records) >= limit {
			break
		}
		records = append(records, LogRecord{
			Time:      firstNonEmpty(getString(item, "_time"), getString(item, "time"), getString(item, "timestamp")),
			Level:     firstNonEmpty(getString(item, "severity"), getString(item, "level")),
			Service:   firstNonEmpty(getString(item, "service"), getString(item, "service_name"), getString(item, "service.name")),
			Operation: firstNonEmpty(getString(item, "operation"), getString(item, "operation_name"), getString(item, "name")),
			TraceID:   firstNonEmpty(getString(item, "trace_id"), getString(item, "traceId")),
			SpanID:    firstNonEmpty(getString(item, "span_id"), getString(item, "spanId")),
			RequestID: firstNonEmpty(getString(item, "request_id"), getString(item, "req_id")),
			Message:   firstNonEmpty(getString(item, "message"), getString(item, "log"), getString(item, "body")),
			Raw:       item,
		})
	}
	return records, total
}

type o2TraceMeta struct {
	TraceID       string
	OperationName string
	ServiceName   string
	Status        string
	DurationMS    int64
	SpanCount     int
}

func parseOpenObserveTraceLatest(payload []byte) (o2TraceMeta, error) {
	var parsed struct {
		TraceID string           `json:"trace_id"`
		Hits    []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return o2TraceMeta{}, fmt.Errorf("decode openobserve trace latest response: %w", err)
	}
	if len(parsed.Hits) == 0 {
		if parsed.TraceID == "" {
			return o2TraceMeta{}, fmt.Errorf("trace not found")
		}
		return o2TraceMeta{TraceID: parsed.TraceID}, nil
	}

	h := parsed.Hits[0]
	firstEvent, _ := h["first_event"].(map[string]any)
	traceID := firstNonEmpty(getString(h, "trace_id"), parsed.TraceID)
	return o2TraceMeta{
		TraceID:       traceID,
		OperationName: getString(firstEvent, "operation_name"),
		ServiceName:   firstOpenObserveServiceName(h),
		Status:        getString(firstEvent, "span_status"),
		DurationMS:    getInt64(h, "duration"),
		SpanCount:     openObserveSpanCount(h),
	}, nil
}

func firstOpenObserveServiceName(h map[string]any) string {
	if firstEvent, ok := h["first_event"].(map[string]any); ok {
		if service := getString(firstEvent, "service_name"); service != "" {
			return service
		}
	}
	values, _ := h["service_name"].([]any)
	for _, value := range values {
		item, _ := value.(map[string]any)
		if service := getString(item, "service_name"); service != "" {
			return service
		}
	}
	return getString(h, "service_name")
}

func openObserveSpanCount(h map[string]any) int {
	values, _ := h["spans"].([]any)
	count := 0
	for _, value := range values {
		switch x := value.(type) {
		case float64:
			count += int(x)
		case int:
			count += x
		}
	}
	return count
}

func openObserveBaseURL() string {
	v := strings.TrimSpace(os.Getenv("OPENOBSERVE_URL"))
	if v == "" {
		return defaultOpenObserveBaseURL
	}
	return v
}

func openObserveOrg() string {
	v := strings.TrimSpace(os.Getenv("OPENOBSERVE_ORG"))
	if v == "" {
		return defaultOpenObserveOrg
	}
	return v
}

func openObserveLogsStream() string {
	v := strings.TrimSpace(os.Getenv("OPENOBSERVE_LOGS_STREAM"))
	if v == "" {
		return defaultOpenObserveStream
	}
	return v
}

func openObserveTracesStream() string {
	v := strings.TrimSpace(os.Getenv("OPENOBSERVE_TRACES_STREAM"))
	if v == "" {
		return defaultOpenObserveStream
	}
	return v
}

func applyOpenObserveAuth(req *http.Request) {
	if header := strings.TrimSpace(os.Getenv("OPENOBSERVE_AUTH_HEADER")); header != "" {
		req.Header.Set("Authorization", header)
		return
	}
	user := strings.TrimSpace(os.Getenv("OPENOBSERVE_USERNAME"))
	pass := os.Getenv("OPENOBSERVE_PASSWORD")
	if user == "" {
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	req.Header.Set("Authorization", "Basic "+encoded)
}

func buildO2ContainsClause(text string) string {
	q := strings.TrimSpace(text)
	if q == "" {
		return "1=1"
	}
	// Default to common log body fields produced by direct JSON and OTLP ingestion.
	// SQL dialect is documented as PostgreSQL-like; use ILIKE for case-insensitive match.
	pattern := o2Quote("%" + q + "%")
	// Note: using multiple fields keeps this provider usable across different stream schemas.
	return fmt.Sprintf("(log ILIKE %s OR message ILIKE %s OR body ILIKE %s)", pattern, pattern, pattern)
}

func o2Quote(s string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(s), "'", "''") + "'"
}

func resolveTimeRangeMicros(since, start, end string) (int64, int64, error) {
	// Use RFC3339 when provided; otherwise interpret since as Go duration.
	if strings.TrimSpace(start) != "" || strings.TrimSpace(end) != "" {
		var startT, endT time.Time
		var err error
		if strings.TrimSpace(end) != "" {
			endT, err = time.Parse(time.RFC3339, strings.TrimSpace(end))
			if err != nil {
				return 0, 0, fmt.Errorf("invalid end time %q: %w", end, err)
			}
		} else {
			endT = time.Now()
		}
		if strings.TrimSpace(start) != "" {
			startT, err = time.Parse(time.RFC3339, strings.TrimSpace(start))
			if err != nil {
				return 0, 0, fmt.Errorf("invalid start time %q: %w", start, err)
			}
		} else {
			d := 30 * time.Minute
			startT = endT.Add(-d)
		}
		return startT.UnixMicro(), endT.UnixMicro(), nil
	}

	d := 30 * time.Minute
	if strings.TrimSpace(since) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(since))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid since %q: %w", since, err)
		}
		d = parsed
	}
	endT := time.Now()
	startT := endT.Add(-d)
	return startT.UnixMicro(), endT.UnixMicro(), nil
}

func formatMicrosAsRFC3339(us int64) string {
	if us <= 0 {
		return ""
	}
	return time.UnixMicro(us).UTC().Format(time.RFC3339Nano)
}

func normalizeOpenObserveMetricSamples(payload []byte) ([]MetricSample, bool) {
	var parsed o2SearchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, false
	}

	// best-effort: accept fields
	// - timestamp: timestamp|_timestamp|time (int, in us/ms/ns)
	// - metric: metric|name
	// - value: value
	// - labels: labels (object)
	// Everything else is ignored.

	samples := make([]MetricSample, 0, len(parsed.Hits))
	for _, item := range parsed.Hits {
		metric := firstNonEmpty(getString(item, "metric"), getString(item, "name"), getString(item, "__name__"))
		if metric == "" {
			metric = "metric"
		}
		value := firstNonEmpty(getString(item, "value"), getString(item, "val"))
		ts := getInt64(item, "timestamp")
		if ts == 0 {
			ts = getInt64(item, "_timestamp")
		}
		if ts == 0 {
			ts = getInt64(item, "time")
		}

		// normalize to ms for MetricSample.Timestamp
		// If ts looks like microseconds or nanoseconds, reduce.
		ts = normalizeTimestampToMillis(ts)

		labels := map[string]string{}
		if rawLabels, ok := item["labels"].(map[string]any); ok {
			for k, v := range rawLabels {
				if s, ok := v.(string); ok {
					labels[k] = s
				} else {
					labels[k] = fmt.Sprint(v)
				}
			}
		}

		samples = append(samples, MetricSample{Metric: metric, Labels: labels, Value: value, Timestamp: ts})
	}
	truncated := false
	if len(samples) > 5000 {
		samples = samples[:5000]
		truncated = true
	}
	return samples, truncated
}

func normalizeTimestampToMillis(ts int64) int64 {
	if ts <= 0 {
		return 0
	}
	// heuristics: seconds(1e9..), millis(1e12..), micros(1e15..), nanos(1e18..)
	if ts > 1e17 {
		return ts / 1e6
	}
	if ts > 1e14 {
		return ts / 1e3
	}
	if ts > 1e11 {
		return ts
	}
	if ts > 1e8 {
		return ts * 1000
	}
	return ts
}

func getInt64(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		if strings.TrimSpace(x) == "" {
			return 0
		}
		// allow plain int
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
