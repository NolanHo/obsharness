package model

// SearchHit is a single raw evidence record returned by a provider.
type SearchHit struct {
	Kind   string            `json:"kind"`
	Title  string            `json:"title"`
	Source string            `json:"source"`
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields,omitempty"`
}

// SearchResult is the stable contract for search entry output.
type SearchResult struct {
	Provider  string      `json:"provider"`
	Query     string      `json:"query"`
	Start     string      `json:"start,omitempty"`
	End       string      `json:"end,omitempty"`
	Limit     int         `json:"limit,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
	Total     int         `json:"total"`
	Hits      []SearchHit `json:"hits"`
}
