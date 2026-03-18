package search

import (
	"fmt"
	"strings"
)

// Router picks a provider by name.
type Router struct {
	providers map[string]Provider
}

// NewRouter registers provider implementations by their canonical name.
func NewRouter(providers map[string]Provider) *Router {
	copyMap := make(map[string]Provider, len(providers))
	for name, p := range providers {
		copyMap[strings.ToLower(name)] = p
	}
	return &Router{providers: copyMap}
}

// Provider resolves a provider by name.
func (r *Router) Provider(name string) (Provider, error) {
	p, ok := r.providers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return p, nil
}
