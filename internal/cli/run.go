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

const defaultProvider = "mock"

// Run is the top-level command entry for the Go rewrite.
func Run(args []string) error {
	if len(args) == 0 {
		printRootUsage(os.Stderr)
		return fmt.Errorf("missing command")
	}

	switch args[0] {
	case "q":
		return runQ(args[1:])
	case "help", "-h", "--help":
		printRootUsage(os.Stdout)
		return nil
	default:
		printRootUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runQ(args []string) error {
	if len(args) == 0 {
		printQUsage(os.Stderr)
		return fmt.Errorf("missing q subcommand")
	}

	switch args[0] {
	case "search":
		return runQSearch(args[1:])
	case "help", "-h", "--help":
		printQUsage(os.Stdout)
		return nil
	default:
		printQUsage(os.Stderr)
		return fmt.Errorf("unknown q subcommand %q", args[0])
	}
}

func runQSearch(args []string) error {
	fs := flag.NewFlagSet("q search", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	provider := fs.String("provider", searchProviderFromEnv(), "search provider")
	limit := fs.Int("limit", 10, "max number of hits")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		printQSearchUsage(os.Stderr)
		return err
	}

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		printQSearchUsage(os.Stderr)
		return fmt.Errorf("query text is required")
	}
	if *limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}

	router := search.NewRouter(map[string]search.Provider{
		"mock": search.MockProvider{},
	})
	p, err := router.Provider(*provider)
	if err != nil {
		return err
	}

	result, err := p.Search(context.Background(), search.Query{Text: query, Limit: *limit})
	if err != nil {
		return err
	}
	result.Provider = strings.ToLower(*provider)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	fmt.Printf("provider=%s query=%q total=%d\n", result.Provider, result.Query, result.Total)
	for i, hit := range result.Hits {
		fmt.Printf("%d. [%s] %s (%s:%s)\n", i+1, hit.Kind, hit.Title, hit.Source, hit.ID)
	}
	return nil
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh q search [--provider NAME] [--limit N] [--json] <query>")
}

func printQUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh q search [--provider NAME] [--limit N] [--json] <query>")
}

func printQSearchUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  obsh q search [--provider NAME] [--limit N] [--json] <query>")
}

func searchProviderFromEnv() string {
	v := strings.TrimSpace(os.Getenv("OBSH_SEARCH_PROVIDER"))
	if v == "" {
		return defaultProvider
	}
	return v
}
