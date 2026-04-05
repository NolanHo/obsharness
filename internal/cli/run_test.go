package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSearchText(t *testing.T) {
	out, errOut, err := runWithCapture([]string{"search", "--provider", "mock", "checkout timeout"})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "log    2026-03-31T10:02:14Z") {
		t.Fatalf("missing log hit: %s", out)
	}
	if !strings.Contains(out, "trace  2026-03-31T10:02:14Z trace_id=tr-1") {
		t.Fatalf("missing trace hit: %s", out)
	}
	if !strings.Contains(out, "metric 2026-03-31T10:03:00Z http_request_errors_ratio{service=checkout} 0.083") {
		t.Fatalf("missing metric hit: %s", out)
	}
}

func TestRunLogsText(t *testing.T) {
	out, errOut, err := runWithCapture([]string{"logs", "--provider", "mock", "--trace-id", "tr-1"})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "# source=victorialogs") {
		t.Fatalf("missing header: %s", out)
	}
	if !strings.Contains(out, "trace_id=tr-1 span_id=s4") {
		t.Fatalf("missing log record ids: %s", out)
	}
}

func TestRunTraceText(t *testing.T) {
	out, errOut, err := runWithCapture([]string{"trace", "--provider", "mock", "tr-1"})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "POST /checkout service=api dur=2413ms status=error span_id=s1") {
		t.Fatalf("missing root span: %s", out)
	}
	if !strings.Contains(out, "attrs and events are hidden by default; inspect one span with: obsh span <span_id>") {
		t.Fatalf("missing expansion hint: %s", out)
	}
}

func TestRunSpanText(t *testing.T) {
	out, errOut, err := runWithCapture([]string{"span", "--provider", "mock", "s6"})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "attr http.route=\"/capture\"") {
		t.Fatalf("missing attr line: %s", out)
	}
	if !strings.Contains(out, "event 2026-03-31T10:02:14.120Z name=retry attempt=1") {
		t.Fatalf("missing event line: %s", out)
	}
}

func TestRunMetricsText(t *testing.T) {
	out, errOut, err := runWithCapture([]string{"metrics", "--provider", "mock", "rate(http_requests_total[5m])"})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "rate(http_requests_total[5m]){service=\"checkout\"} 0.42 1711871820000") {
		t.Fatalf("missing sample line: %s", out)
	}
}

func TestRunQSearchJSON(t *testing.T) {
	out, errOut, err := runWithCapture([]string{"q", "search", "--provider", "mock", "--json", "checkout timeout"})
	if err != nil {
		t.Fatalf("run failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "\"provider\": \"mock\"") {
		t.Fatalf("missing provider in json: %s", out)
	}
}

func TestRunSearchRequiresQuery(t *testing.T) {
	_, _, err := runWithCapture([]string{"search"})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func runWithCapture(args []string) (string, string, error) {
	oldStdout, oldStderr := stdout, stderr
	var outBuf, errBuf bytes.Buffer
	stdout = &outBuf
	stderr = &errBuf
	defer func() {
		stdout = oldStdout
		stderr = oldStderr
	}()
	err := Run(args)
	return outBuf.String(), errBuf.String(), err
}
