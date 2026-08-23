// Package types holds shared AutoFix contracts.
// Sourced from autofix-polyglot types/go/linkrecord.go
// Keep structs aligned with JSON Schemas and TypeScript types.
package types

import (
	"encoding/base64"
)

// LinkStatus values mirror schemas/link-record.schema.json enum.
const (
	StatusPending = "PENDING"
	StatusHealed  = "HEALED"
	StatusDead    = "DEAD"
	StatusHealthy = "HEALTHY"
)

// LinkRecord is the shared KV value (L2).
type LinkRecord struct {
	Status       string `json:"status"`
	OriginalURL  string `json:"original_url"`
	ResolvedURL  string `json:"resolved_url,omitempty"`
	DiscoveredAt string `json:"discovered_at,omitempty"`
	HealedAt     string `json:"healed_at,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// DiscoveryMessage is the queue payload (Edge → Queue).
type DiscoveryMessage struct {
	URLs         []string `json:"urls"`
	DiscoveredAt string   `json:"discovered_at"`
}

// DiscoverRequest is the body of POST /v1/discover.
type DiscoverRequest struct {
	URLs []string `json:"urls,omitempty"`
	URL  string   `json:"url,omitempty"`
}

// DiscoverResponse is the success body of POST /v1/discover.
type DiscoverResponse struct {
	Enqueued int `json:"enqueued"`
}

// KeyFor returns the canonical KV key for an absolute URL.
// Must match TypeScript urlKey() (UTF-8 bytes → Std base64).
func KeyFor(url string) string {
	return base64.StdEncoding.EncodeToString([]byte(url))
}
