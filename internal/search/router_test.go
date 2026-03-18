package search

import "testing"

func TestRouterProviderCaseInsensitive(t *testing.T) {
	r := NewRouter(map[string]Provider{"mock": MockProvider{}})
	if _, err := r.Provider("MoCk"); err != nil {
		t.Fatalf("expected provider, got err: %v", err)
	}
}

func TestRouterProviderUnknown(t *testing.T) {
	r := NewRouter(map[string]Provider{"mock": MockProvider{}})
	if _, err := r.Provider("missing"); err == nil {
		t.Fatal("expected unknown provider error")
	}
}
