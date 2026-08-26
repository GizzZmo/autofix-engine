package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type Config struct {
	CFAccountID     string
	CFAPIToken      string
	CFKVNamespaceID string
	ListenAddr      string
	WorkerCount     int
	UserAgent       string
	AdminToken      string
}

func loadConfig() Config {
	_ = godotenv.Load()

	return Config{
		CFAccountID:     os.Getenv("CF_ACCOUNT_ID"),
		CFAPIToken:      os.Getenv("CF_API_TOKEN"),
		CFKVNamespaceID: os.Getenv("CF_KV_NAMESPACE_ID"),
		ListenAddr:      envOr("HEALER_LISTEN", ":8080"),
		WorkerCount:     4,
		UserAgent:       "AutoFix-Healer/1.0 (+https://github.com/GizzZmo/autofix-engine)",
		AdminToken:      os.Getenv("ADMIN_TOKEN"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Link record
// ---------------------------------------------------------------------------

type LinkRecord struct {
	Status       string `json:"status"`
	OriginalURL  string `json:"original_url"`
	ResolvedURL  string `json:"resolved_url,omitempty"`
	DiscoveredAt string `json:"discovered_at,omitempty"`
	HealedAt     string `json:"healed_at,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// Discovery queue
// ---------------------------------------------------------------------------

type DiscoveryQueue struct {
	ch     chan string
	seen   map[string]struct{}
	mu     sync.Mutex
	paused bool
}

func NewDiscoveryQueue(buffer int) *DiscoveryQueue {
	return &DiscoveryQueue{
		ch:   make(chan string, buffer),
		seen: make(map[string]struct{}),
	}
}

func (q *DiscoveryQueue) Enqueue(url string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.seen[url]; ok {
		return
	}
	q.seen[url] = struct{}{}
	select {
	case q.ch <- url:
	default:
		slog.Warn("queue full, dropping", "event", "heal.defer", "component", "healer", "url", url)
	}
}

func (q *DiscoveryQueue) Chan() <-chan string { return q.ch }

func (q *DiscoveryQueue) Depth() int64 {
	return int64(len(q.ch))
}

func (q *DiscoveryQueue) EnqueueForce(url string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.seen, url)
	q.seen[url] = struct{}{}
	select {
	case q.ch <- url:
	default:
		slog.Warn("queue full, dropping", "event", "heal.defer", "component", "healer", "url", url)
	}
}

func (q *DiscoveryQueue) SetPaused(p bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.paused = p
}

func (q *DiscoveryQueue) Paused() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.paused
}
