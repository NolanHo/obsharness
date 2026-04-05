package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/NolanHou/obsharness/internal/search"
)

const defaultProvider = "victoria"

var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// Run is the top-level command entry for the Go rewrite.
func Run(args []string) error {
	if len(args) == 0 {
		printRootUsage(stderr)
		return fmt.Errorf("missing command")
	}

	switch args[0] {
	case "search":
		return runSearch(args[1:], false)
	case "logs":
		return runLogs(args[1:])
	case "trace":
		return runTrace(args[1:])
	case "span":
		return runSpan(args[1:])
	case "metrics":
		return runMetrics(args[1:])
	case "q":
		return runQ(args[1:])
	case "help", "-h", "--help":
		printRootUsage(stdout)
		return nil
	default:
		printRootUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runQ(args []string) error {
	if len(args) == 0 {
		printQUsage(stderr)
		return fmt.Errorf("missing q subcommand")
	}

	switch args[0] {
	case "search":
		return runSearch(args[1:], true)
	case "help", "-h", "--help":
		printQUsage(stdout)
		return nil
	default:
		printQUsage(stderr)
		return fmt.Errorf("unknown q subcommand %q", args[0])
	}
}

func runSearch(args []string, compat bool) error {
	fs := newFlagSet("search")
	provider := fs.String("provider", searchProviderFromEnv(), "search provider")
	limit := fs.Int("limit", 20, "max number of hits")
	asJSON := fs.Bool("json", false, "emit JSON")
	since := fs.String("since", "30m", "lookback window")
	start := fs.String("start", "", "start time")
	end := fs.String("end", "", "end time")
	if err := fs.Parse(args); err != nil {
		printSearchUsage(stderr, compat)
		return err
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		printSearchUsage(stderr, compat)
		return fmt.Errorf("query text is required")
	}
	if *limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}

	p, err := providerRouter().Provider(*provider)
	if err != nil {
		return err
	}
	result, err := p.Search(context.Background(), search.Query{Text: query, Since: *since, Start: *start, End: *end, Limit: *limit})
	if err != nil {
		return err
	}
	result.Provider = strings.ToLower(*provider)
	result.Limit = *limit
	if result.Start == "" {
		result.Start = *start
	}
	if result.End == "" {
		result.End = *end
	}
	if result.Start == "" && result.End == "" && *since != "" {
		result.Start = "-" + *since
	}

	if *asJSON {
		return writeJSON(stdout, result)
	}
	renderSearch(stdout, result)
	return nil
}

func runLogs(args []string) error {
	fs := newFlagSet("logs")
	provider := fs.String("provider", searchProviderFromEnv(), "search provider")
	limit := fs.Int("limit", 200, "max number of log lines")
	asJSON := fs.Bool("json", false, "emit JSON")
	since := fs.String("since", "30m", "lookback window")
	start := fs.String("start", "", "start time")
	end := fs.String("end", "", "end time")
	service := fs.String("service", "", "service filter")
	operation := fs.String("operation", "", "operation filter")
	traceID := fs.String("trace-id", "", "trace id filter")
	requestID := fs.String("request-id", "", "request id filter")
	_ = fs.Bool("full", false, "show full multiline payloads when available")
	if err := fs.Parse(args); err != nil {
		printLogsUsage(stderr)
		return err
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}

	p, err := providerRouter().Provider(*provider)
	if err != nil {
		return err
	}
	result, err := p.Logs(context.Background(), search.LogsQuery{Text: query, Since: *since, Start: *start, End: *end, Service: *service, Operation: *operation, TraceID: *traceID, RequestID: *requestID, Limit: *limit})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	renderLogs(stdout, result)
	return nil
}

func runTrace(args []string) error {
	fs := newFlagSet("trace")
	provider := fs.String("provider", searchProviderFromEnv(), "search provider")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		printTraceUsage(stderr)
		return err
	}
	traceID := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if traceID == "" {
		printTraceUsage(stderr)
		return fmt.Errorf("trace id is required")
	}

	p, err := providerRouter().Provider(*provider)
	if err != nil {
		return err
	}
	result, err := p.Trace(context.Background(), traceID)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	renderTrace(stdout, result)
	return nil
}

func runSpan(args []string) error {
	fs := newFlagSet("span")
	provider := fs.String("provider", searchProviderFromEnv(), "search provider")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		printSpanUsage(stderr)
		return err
	}
	spanID := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if spanID == "" {
		printSpanUsage(stderr)
		return fmt.Errorf("span id is required")
	}

	p, err := providerRouter().Provider(*provider)
	if err != nil {
		return err
	}
	result, err := p.Span(context.Background(), spanID)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	renderSpan(stdout, result)
	return nil
}

func runMetrics(args []string) error {
	fs := newFlagSet("metrics")
	provider := fs.String("provider", searchProviderFromEnv(), "search provider")
	asJSON := fs.Bool("json", false, "emit JSON")
	since := fs.String("since", "30m", "lookback window")
	start := fs.String("start", "", "start time")
	end := fs.String("end", "", "end time")
	step := fs.String("step", "60s", "range query step")
	if err := fs.Parse(args); err != nil {
		printMetricsUsage(stderr)
		return err
	}
	expr := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if expr == "" {
		printMetricsUsage(stderr)
		return fmt.Errorf("metric expression is required")
	}

	p, err := providerRouter().Provider(*provider)
	if err != nil {
		return err
	}
	result, err := p.Metrics(context.Background(), search.MetricsQuery{Expr: expr, Since: *since, Start: *start, End: *end, Step: *step})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	renderMetrics(stdout, result)
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func providerRouter() *search.Router {
	return search.NewRouter(map[string]search.Provider{
		"mock":     search.MockProvider{},
		"victoria": search.NewVictoriaProvider(),
	})
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh search [--provider NAME] [--since DUR] [--limit N] [--json] <query>")
	fmt.Fprintln(w, "  obsh logs [--provider NAME] [filters] [--json] [query]")
	fmt.Fprintln(w, "  obsh trace [--provider NAME] [--json] <trace_id>")
	fmt.Fprintln(w, "  obsh span [--provider NAME] [--json] <span_id>")
	fmt.Fprintln(w, "  obsh metrics [--provider NAME] [--since DUR|--start T --end T] [--step DUR] [--json] <expr>")
	fmt.Fprintln(w, "  obsh q search [--provider NAME] [--since DUR] [--limit N] [--json] <query>")
}

func printQUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh q search [--provider NAME] [--since DUR] [--limit N] [--json] <query>")
}

func printSearchUsage(w io.Writer, compat bool) {
	fmt.Fprintln(w, "Usage:")
	if compat {
		fmt.Fprintln(w, "  obsh q search [--provider NAME] [--since DUR] [--limit N] [--json] <query>")
		return
	}
	fmt.Fprintln(w, "  obsh search [--provider NAME] [--since DUR] [--limit N] [--json] <query>")
}

func printLogsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh logs [--provider NAME] [--since DUR] [--service NAME] [--operation NAME] [--trace-id ID] [--request-id ID] [--limit N] [--json] [query]")
}

func printTraceUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh trace [--provider NAME] [--json] <trace_id>")
}

func printSpanUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh span [--provider NAME] [--json] <span_id>")
}

func printMetricsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh metrics [--provider NAME] [--since DUR|--start T --end T] [--step DUR] [--json] <expr>")
}

func searchProviderFromEnv() string {
	v := strings.TrimSpace(os.Getenv("OBSH_SEARCH_PROVIDER"))
	if v == "" {
		return defaultProvider
	}
	return v
}
