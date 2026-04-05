package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/NolanHou/obsharness/internal/search"
)

func renderSearch(w io.Writer, res search.Result) {
	fmt.Fprintf(w, "# source=%s", res.Provider)
	if res.Start != "" {
		fmt.Fprintf(w, " start=%s", res.Start)
	}
	if res.End != "" {
		fmt.Fprintf(w, " end=%s", res.End)
	}
	if res.Limit > 0 {
		fmt.Fprintf(w, " limit=%d", res.Limit)
	}
	fmt.Fprintf(w, " truncated=%t\n", res.Truncated)
	for _, hit := range res.Hits {
		ts := firstField(hit.Fields, "time")
		switch hit.Kind {
		case "log":
			fmt.Fprintf(w, "log    %s", ts)
			writeKV(w, "service", firstField(hit.Fields, "service", "service.name"))
			writeKV(w, "trace_id", hit.Fields["trace_id"])
			writeKV(w, "request_id", hit.Fields["request_id"])
			writeKVQuoted(w, "msg", hit.Title)
			fmt.Fprintln(w)
		case "trace":
			fmt.Fprintf(w, "trace  %s", ts)
			writeKV(w, "trace_id", firstField(hit.Fields, "trace_id", "id"))
			writeKV(w, "root_span", hit.Fields["root_span"])
			writeKVQuoted(w, "root", hit.Title)
			if dur := hit.Fields["duration_ms"]; dur != "" {
				writeKV(w, "dur", dur+"ms")
			}
			writeKV(w, "status", hit.Fields["status"])
			fmt.Fprintln(w)
		case "metric":
			fmt.Fprintf(w, "metric %s %s", ts, firstField(hit.Fields, "metric", "series", "name", hit.Title))
			if labels := hit.Fields["labels"]; labels != "" {
				fmt.Fprintf(w, "{%s}", labels)
			}
			if value := hit.Fields["value"]; value != "" {
				fmt.Fprintf(w, " %s", value)
			}
			fmt.Fprintln(w)
		default:
			fmt.Fprintf(w, "%s %s %s\n", hit.Kind, ts, hit.Title)
		}
	}
}

func renderLogs(w io.Writer, res search.LogsResult) {
	fmt.Fprintf(w, "# source=%s", res.Source)
	if res.Start != "" {
		fmt.Fprintf(w, " start=%s", res.Start)
	}
	if res.End != "" {
		fmt.Fprintf(w, " end=%s", res.End)
	}
	if res.Limit > 0 {
		fmt.Fprintf(w, " limit=%d", res.Limit)
	}
	fmt.Fprintf(w, " truncated=%t\n", res.Truncated)
	for _, record := range res.Records {
		fmt.Fprint(w, record.Time)
		writeKV(w, "level", record.Level)
		writeKV(w, "service", record.Service)
		writeKVQuoted(w, "op", record.Operation)
		writeKV(w, "trace_id", record.TraceID)
		writeKV(w, "span_id", record.SpanID)
		writeKV(w, "request_id", record.RequestID)
		writeKVQuoted(w, "msg", record.Message)
		fmt.Fprintln(w)
	}
}

func renderTrace(w io.Writer, res search.TraceResult) {
	fmt.Fprintf(w, "# source=%s trace_id=%s", res.Source, res.TraceID)
	writeKV(w, "root_span", res.RootSpanID)
	if res.SpanCount > 0 {
		writeKV(w, "spans", fmt.Sprintf("%d", res.SpanCount))
	}
	if res.ErrorCount > 0 {
		writeKV(w, "errors", fmt.Sprintf("%d", res.ErrorCount))
	}
	fmt.Fprintln(w)

	children := map[string][]search.TraceSpan{}
	byID := map[string]search.TraceSpan{}
	for _, span := range res.Spans {
		byID[span.SpanID] = span
		children[span.ParentSpanID] = append(children[span.ParentSpanID], span)
	}
	roots := children[""]
	if res.RootSpanID != "" {
		if root, ok := byID[res.RootSpanID]; ok {
			roots = []search.TraceSpan{root}
		}
	}
	for i, root := range roots {
		renderTraceNode(w, root, children, "", i == len(roots)-1, true)
	}
	if traceHasHidden(res.Spans) {
		fmt.Fprintln(w, "# attrs and events are hidden by default; inspect one span with: obsh span <span_id>")
	}
}

func renderTraceNode(w io.Writer, span search.TraceSpan, children map[string][]search.TraceSpan, prefix string, isLast, isRoot bool) {
	linePrefix := prefix
	if !isRoot {
		if isLast {
			linePrefix += "\\- "
		} else {
			linePrefix += "|- "
		}
	}
	fmt.Fprint(w, linePrefix)
	fmt.Fprint(w, span.Name)
	writeKV(w, "service", span.Service)
	if span.DurationMS > 0 {
		writeKV(w, "dur", fmt.Sprintf("%dms", span.DurationMS))
	}
	writeKV(w, "status", span.Status)
	writeKV(w, "span_id", span.SpanID)
	if span.AttrsHidden {
		writeKV(w, "attrs", "hidden")
	}
	if span.EventsHidden {
		writeKV(w, "events", "hidden")
	}
	fmt.Fprintln(w)

	nextPrefix := prefix
	if !isRoot {
		if isLast {
			nextPrefix += "   "
		} else {
			nextPrefix += "|  "
		}
	}
	childList := children[span.SpanID]
	for i, child := range childList {
		renderTraceNode(w, child, children, nextPrefix, i == len(childList)-1, false)
	}
}

func renderSpan(w io.Writer, res search.SpanResult) {
	fmt.Fprintf(w, "# source=%s", res.Source)
	writeKV(w, "trace_id", res.TraceID)
	writeKV(w, "span_id", res.Span.SpanID)
	writeKV(w, "parent_span_id", res.Span.ParentSpanID)
	fmt.Fprintln(w)
	fmt.Fprint(w, quoteIfNeeded(res.Span.Name))
	writeKV(w, "service", res.Span.Service)
	if res.Span.DurationMS > 0 {
		writeKV(w, "dur", fmt.Sprintf("%dms", res.Span.DurationMS))
	}
	writeKV(w, "status", res.Span.Status)
	fmt.Fprintln(w)

	keys := make([]string, 0, len(res.Span.Attrs))
	for key := range res.Span.Attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(w, "attr %s=%s\n", key, quoteIfNeeded(res.Span.Attrs[key]))
	}
	for _, event := range res.Span.Events {
		fmt.Fprintf(w, "event")
		if event.Time != "" {
			fmt.Fprintf(w, " %s", event.Time)
		}
		fmt.Fprintf(w, " name=%s", quoteIfNeeded(event.Name))
		keys = keys[:0]
		for key := range event.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(w, " %s=%s", key, quoteIfNeeded(event.Fields[key]))
		}
		fmt.Fprintln(w)
	}
}

func renderMetrics(w io.Writer, res search.MetricsResult) {
	fmt.Fprintf(w, "# expr=%s", quoteIfNeeded(res.Expr))
	if res.Start != "" || res.End != "" {
		if res.Start != "" {
			fmt.Fprintf(w, " start=%s", res.Start)
		}
		if res.End != "" {
			fmt.Fprintf(w, " end=%s", res.End)
		}
		if res.Step != "" {
			fmt.Fprintf(w, " step=%s", res.Step)
		}
		fmt.Fprintf(w, " truncated=%t", res.Truncated)
	} else if len(res.Samples) > 0 {
		fmt.Fprintf(w, " time=%d", res.Samples[len(res.Samples)-1].Timestamp/1000)
	}
	fmt.Fprintln(w)
	for _, sample := range res.Samples {
		fmt.Fprintf(w, "%s%s %s %d\n", sample.Metric, formatLabelSet(sample.Labels), sample.Value, sample.Timestamp)
	}
}

func traceHasHidden(spans []search.TraceSpan) bool {
	for _, span := range spans {
		if span.AttrsHidden || span.EventsHidden {
			return true
		}
	}
	return false
}

func firstField(fields map[string]string, keys ...string) string {
	for _, key := range keys {
		if fields[key] != "" {
			return fields[key]
		}
	}
	return ""
}

func writeKV(w io.Writer, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(w, " %s=%s", key, quoteIfNeeded(value))
}

func writeKVQuoted(w io.Writer, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(w, " %s=%s", key, quoteAlways(value))
}

func quoteIfNeeded(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t\n\"=/") {
		return quoteAlways(value)
	}
	return value
}

func quoteAlways(value string) string {
	return fmt.Sprintf("%q", value)
}

func formatLabelSet(labels map[string]string) string {
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
		parts = append(parts, fmt.Sprintf("%s=%q", key, labels[key]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
