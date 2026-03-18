package cli

import "testing"

func TestRunQSearchJSON(t *testing.T) {
	if err := Run([]string{"q", "search", "--json", "checkout timeout"}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
}

func TestRunQSearchRequiresQuery(t *testing.T) {
	if err := Run([]string{"q", "search"}); err == nil {
		t.Fatal("expected error for missing query")
	}
}
