package search

import "context"

// Provider is implemented by search backends.
type Provider interface {
	Search(ctx context.Context, in Query) (Result, error)
}

// Query is the backend-agnostic search input.
type Query struct {
	Text  string
	Limit int
}
