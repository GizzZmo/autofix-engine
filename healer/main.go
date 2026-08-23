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
	ch   chan string
	seen map[string]struct{}
	mu   sync.Mutex
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

// ---------------------------------------------------------------------------
// Wayback Machine (protected by circuit breaker)
// ---------------------------------------------------------------------------

type ArchiveResponse struct {
	ArchivedSnapshots struct {
		Closest struct {
			Available bool   `json:"available"`
			URL       string `json:"url"`
		} `json:"closest"`
	} `json:"archived_snapshots"`
}

var waybackBreaker = NewCircuitBreaker(5, 60*time.Second, 1)

func CheckArchive(ctx context.Context, client *http.Client, url string, tel *Telemetry) (string, error) {
	ctx, span := tel.Tracer.Start(ctx, "autofix.wayback",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("autofix.url", url)),
	)
	defer span.End()

	start := time.Now()
	outcome := "miss"
	var result string

	err := waybackBreaker.Execute(func() error {
		apiURL := fmt.Sprintf("https://archive.org/wayback/available?url=%s", url)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "AutoFix-Healer/1.0")

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return fmt.Errorf("wayback http %d", resp.StatusCode)
		}

		var res ArchiveResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			return err
		}
		if res.ArchivedSnapshots.Closest.Available && res.ArchivedSnapshots.Closest.URL != "" {
			result = res.ArchivedSnapshots.Closest.URL
			outcome = "hit"
			return nil
		}
		return nil
	})

	dur := time.Since(start).Seconds()
	if err != nil {
		if err == ErrCircuitOpen {
			outcome = "circuit_open"
			span.SetAttributes(attribute.String("autofix.circuit.state", "open"))
		} else {
			outcome = "error"
			recordSpanError(span, err)
		}
		tel.WaybackDuration.Record(ctx, dur, metric.WithAttributes(attribute.String("outcome", outcome)))
		logEvent(ctx, slog.LevelWarn, "heal.archive",
			"url", url, "error", err.Error(), "duration_ms", time.Since(start).Milliseconds(),
			"circuit", waybackBreaker.State(), "circuit_name", "healer_wayback")
		return "", err
	}
	tel.WaybackDuration.Record(ctx, dur, metric.WithAttributes(attribute.String("outcome", outcome)))
	if result == "" {
		err = fmt.Errorf("no archive found")
		span.SetStatus(codes.Ok, "no archive")
		logEvent(ctx, slog.LevelInfo, "heal.archive",
			"url", url, "reason", "no_archive", "duration_ms", time.Since(start).Milliseconds())
		return "", err
	}
	span.SetAttributes(attribute.String("autofix.resolved_url", result))
	logEvent(ctx, slog.LevelInfo, "heal.archive",
		"url", url, "resolved_url", result, "duration_ms", time.Since(start).Milliseconds())
	return result, nil
}

// ---------------------------------------------------------------------------
// Soft-404 heuristics
// ---------------------------------------------------------------------------

var (
	soft404TitleRe = regexp.MustCompile(`(?i)(404|not\s+found|page\s+(not\s+found|does\s+not\s+exist|missing)|error\s*404|page\s+moved|gone)`)
	soft404BodyRe  = regexp.MustCompile(`(?i)(404\s*(not\s+found|error)|this\s+page\s+(could\s+not\s+be\s+found|does\s+not\s+exist)|the\s+requested\s+url\s+was\s+not\s+found)`)
)

func looksLikeSoft404(body []byte, contentType string) bool {
	if !strings.Contains(strings.ToLower(contentType), "text/html") {
		return false
	}
	text := string(body)
	if start := strings.Index(strings.ToLower(text), "<title"); start >= 0 {
		if gt := strings.Index(text[start:], ">"); gt >= 0 {
			end := strings.Index(strings.ToLower(text[start+gt:]), "</title>")
			if end >= 0 {
				title := text[start+gt+1 : start+gt+end]
				if soft404TitleRe.MatchString(title) {
					return true
				}
			}
		}
	}
	snippet := text
	if len(snippet) > 8192 {
		snippet = snippet[:8192]
	}
	return soft404BodyRe.MatchString(snippet)
}

func CheckLink(ctx context.Context, client *http.Client, url string, tel *Telemetry) (bool, string) {
	ctx, span := tel.Tracer.Start(ctx, "autofix.check",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("autofix.url", url)),
	)
	defer span.End()

	start := time.Now()
	outcome := "alive"

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", "error")))
		recordSpanError(span, err)
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "invalid_url", "duration_ms", time.Since(start).Milliseconds())
		return true, "invalid_url"
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoFix-Healer/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", "error")))
		span.SetAttributes(attribute.String("autofix.reason", "network_error"))
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "network_error", "duration_ms", time.Since(start).Milliseconds())
		return true, "network_error"
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		outcome = "dead"
		reason := fmt.Sprintf("http_%d", resp.StatusCode)
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", outcome)))
		span.SetAttributes(attribute.String("autofix.reason", reason))
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", reason, "duration_ms", time.Since(start).Milliseconds())
		return true, reason
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", "alive")))
		return false, ""
	}
	req2.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoFix-Healer/1.0)")
	req2.Header.Set("Range", "bytes=0-8191")

	resp2, err := client.Do(req2)
	if err != nil {
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", "alive")))
		return false, ""
	}
	defer resp2.Body.Close()

	if resp2.StatusCode >= 400 {
		outcome = "dead"
		reason := fmt.Sprintf("http_%d", resp2.StatusCode)
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", outcome)))
		span.SetAttributes(attribute.String("autofix.reason", reason))
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", reason, "duration_ms", time.Since(start).Milliseconds())
		return true, reason
	}

	body, _ := io.ReadAll(io.LimitReader(resp2.Body, 8192))
	if looksLikeSoft404(body, resp2.Header.Get("Content-Type")) {
		outcome = "dead"
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", outcome)))
		span.SetAttributes(attribute.String("autofix.reason", "soft_404"))
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "soft_404", "duration_ms", time.Since(start).Milliseconds())
		return true, "soft_404"
	}

	tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("outcome", outcome)))
	logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "status", "HEALTHY", "duration_ms", time.Since(start).Milliseconds())
	return false, ""
}

// ---------------------------------------------------------------------------
// Cloudflare KV API writer
// ---------------------------------------------------------------------------

type CloudflareKV struct {
	AccountID   string
	NamespaceID string
	Token       string
	Client      *http.Client
}

func (c *CloudflareKV) Put(ctx context.Context, key string, record LinkRecord) error {
	if c.AccountID == "" || c.NamespaceID == "" || c.Token == "" {
		return fmt.Errorf("Cloudflare credentials not configured")
	}

	body, err := json.Marshal(record)
	if err != nil {
		return err
	}

	url := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/%s",
		c.AccountID, c.NamespaceID, key,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("CF KV put failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

func keyFor(url string) string {
	return base64.StdEncoding.EncodeToString([]byte(url))
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

func runWorker(id int, q *DiscoveryQueue, kv *CloudflareKV, client *http.Client, wg *sync.WaitGroup, tel *Telemetry) {
	defer wg.Done()
	for url := range q.Chan() {
		healOne(context.Background(), id, url, kv, client, tel)
	}
}

func healOne(ctx context.Context, id int, url string, kv *CloudflareKV, client *http.Client, tel *Telemetry) {
	ctx, span := tel.Tracer.Start(ctx, "autofix.heal",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("autofix.url", url),
			attribute.Int("worker.id", id),
		),
	)
	defer span.End()

	start := time.Now()
	result := "healthy"

	dead, reason := CheckLink(ctx, client, url, tel)
	if !dead {
		_ = kv.Put(ctx, keyFor(url), LinkRecord{
			Status:      "HEALTHY",
			OriginalURL: url,
			HealedAt:    time.Now().UTC().Format(time.RFC3339),
		})
		tel.LinksTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "HEALTHY")))
		tel.HealDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("result", result)))
		span.SetAttributes(attribute.String("autofix.status", "HEALTHY"))
		logEvent(ctx, slog.LevelInfo, "heal.write",
			"url", url, "status", "HEALTHY", "duration_ms", time.Since(start).Milliseconds())
		return
	}

	healed, err := CheckArchive(ctx, client, url, tel)
	if err != nil {
		if err == ErrCircuitOpen {
			result = "deferred"
			_ = kv.Put(ctx, keyFor(url), LinkRecord{
				Status:      "PENDING",
				OriginalURL: url,
				HealedAt:    time.Now().UTC().Format(time.RFC3339),
				Reason:      "circuit_open",
			})
			tel.LinksTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "PENDING")))
			tel.HealDuration.Record(ctx, time.Since(start).Seconds(),
				metric.WithAttributes(attribute.String("result", result)))
			span.SetAttributes(
				attribute.String("autofix.status", "PENDING"),
				attribute.String("autofix.reason", "circuit_open"),
				attribute.String("autofix.circuit.name", "healer_wayback"),
				attribute.String("autofix.circuit.state", "open"),
			)
			logEvent(ctx, slog.LevelWarn, "heal.defer",
				"url", url, "reason", "circuit_open", "circuit_name", "healer_wayback",
				"duration_ms", time.Since(start).Milliseconds())
			return
		}
		result = "dead"
		_ = kv.Put(ctx, keyFor(url), LinkRecord{
			Status:      "DEAD",
			OriginalURL: url,
			HealedAt:    time.Now().UTC().Format(time.RFC3339),
			Reason:      reason,
		})
		tel.LinksTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "DEAD")))
		tel.HealDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("result", result)))
		span.SetAttributes(attribute.String("autofix.status", "DEAD"), attribute.String("autofix.reason", reason))
		logEvent(ctx, slog.LevelInfo, "heal.write",
			"url", url, "status", "DEAD", "reason", reason, "duration_ms", time.Since(start).Milliseconds())
		return
	}

	result = "healed"
	err = kv.Put(ctx, keyFor(url), LinkRecord{
		Status:      "HEALED",
		OriginalURL: url,
		ResolvedURL: healed,
		HealedAt:    time.Now().UTC().Format(time.RFC3339),
		Reason:      reason,
	})
	tel.LinksTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "HEALED")))
	tel.HealDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("result", result)))
	span.SetAttributes(
		attribute.String("autofix.status", "HEALED"),
		attribute.String("autofix.reason", reason),
	)
	if err != nil {
		recordSpanError(span, err)
		logEvent(ctx, slog.LevelError, "heal.write",
			"url", url, "status", "HEALED", "error", err.Error(), "duration_ms", time.Since(start).Milliseconds())
		return
	}
	logEvent(ctx, slog.LevelInfo, "heal.write",
		"url", url, "status", "HEALED", "reason", reason, "resolved_url", healed,
		"duration_ms", time.Since(start).Milliseconds())
}

// ---------------------------------------------------------------------------
// HTTP API
// ---------------------------------------------------------------------------

type discoverRequest struct {
	URLs []string `json:"urls"`
	URL  string   `json:"url"`
}

func startHTTPServer(addr string, q *DiscoveryQueue, tel *Telemetry) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":          "ok",
			"wayback_circuit": waybackBreaker.State(),
		})
	})

	mux.Handle("/metrics", tel.PromHandler())

	mux.Handle("/v1/discover", otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx, span := tel.Tracer.Start(ctx, "autofix.discover",
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req discoverRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			recordSpanError(span, err)
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		count := 0
		for _, u := range req.URLs {
			if u != "" {
				q.Enqueue(u)
				count++
			}
		}
		if req.URL != "" {
			q.Enqueue(req.URL)
			count++
		}
		tel.DiscoverReqs.Add(ctx, 1, metric.WithAttributes(attribute.String("source", "http")))
		span.SetAttributes(attribute.Int("autofix.enqueued", count))
		logEvent(ctx, slog.LevelInfo, "queue.consume", "count", count, "source", "http")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"enqueued": count})
	}), "autofix.discover.http"))

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		slog.Info("discovery API listening", "event", "startup", "component", "healer", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err.Error())
			os.Exit(1)
		}
	}()
	return srv
}

func main() {
	setupSlog()
	cfg := loadConfig()
	slog.Info("AutoFix Healer Engine starting", "event", "startup", "component", "healer")

	if cfg.CFAccountID == "" || cfg.CFAPIToken == "" || cfg.CFKVNamespaceID == "" {
		slog.Warn("Cloudflare credentials missing — healed links will NOT be written to KV",
			"event", "startup", "component", "healer")
	}

	q := NewDiscoveryQueue(10_000)

	ctx := context.Background()
	tel, err := InitTelemetry(ctx, q.Depth, func() int64 {
		return circuitStateValue(waybackBreaker.State())
	})
	if err != nil {
		slog.Error("telemetry init failed", "error", err.Error())
		os.Exit(1)
	}
	waybackBreaker.onTrip = func() {
		tel.CircuitTrips.Add(context.Background(), 1,
			metric.WithAttributes(attribute.String("name", "healer_wayback")))
		logEvent(context.Background(), slog.LevelWarn, "circuit.open",
			"circuit_name", "healer_wayback", "circuit", "open")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	kv := &CloudflareKV{
		AccountID:   cfg.CFAccountID,
		NamespaceID: cfg.CFKVNamespaceID,
		Token:       cfg.CFAPIToken,
		Client:      client,
	}

	q.Enqueue("https://dead-example.com/missing-page")
	q.Enqueue("https://httpstat.us/404")

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerCount; i++ {
		wg.Add(1)
		go runWorker(i, q, kv, client, &wg, tel)
	}

	srv := startHTTPServer(cfg.ListenAddr, q, tel)

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	<-sigCtx.Done()
	stop()

	slog.Info("shutting down", "event", "shutdown", "component", "healer")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	tel.Shutdown(shutdownCtx)
	close(q.ch)
	wg.Wait()
	slog.Info("healer stopped", "event", "shutdown", "component", "healer")
}
