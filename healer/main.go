package main

import (
	"bytes"
	"context"
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

	"github.com/GizzZmo/autofix-engine/healer/types"
	"github.com/joho/godotenv"
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
		logEvent(context.Background(), slog.LevelWarn, "heal.defer",
			"url", url,
			"reason", "queue_full",
		)
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

func CheckArchive(ctx context.Context, client *http.Client, url string) (string, error) {
	var result string
	t0 := time.Now()
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
			return nil
		}
		// "no archive" is not a dependency failure — do not trip the breaker
		return nil
	})
	dur := time.Since(t0).Milliseconds()
	if err != nil {
		logEvent(ctx, slog.LevelWarn, "heal.archive",
			"url", url,
			"error", err.Error(),
			"duration_ms", dur,
			"circuit", waybackBreaker.State(),
			"circuit_name", "healer_wayback",
		)
		return "", err
	}
	if result == "" {
		logEvent(ctx, slog.LevelInfo, "heal.archive",
			"url", url,
			"reason", "miss",
			"duration_ms", dur,
		)
		return "", fmt.Errorf("no archive found")
	}
	logEvent(ctx, slog.LevelInfo, "heal.archive",
		"url", url,
		"resolved_url", result,
		"reason", "hit",
		"duration_ms", dur,
	)
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

func CheckLink(ctx context.Context, client *http.Client, url string) (bool, string) {
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "invalid_url", "duration_ms", time.Since(t0).Milliseconds())
		return true, "invalid_url"
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoFix-Healer/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "network_error", "duration_ms", time.Since(t0).Milliseconds())
		return true, "network_error"
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		reason := fmt.Sprintf("http_%d", resp.StatusCode)
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", reason, "duration_ms", time.Since(t0).Milliseconds())
		return true, reason
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, ""
	}
	req2.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoFix-Healer/1.0)")
	req2.Header.Set("Range", "bytes=0-8191")

	resp2, err := client.Do(req2)
	if err != nil {
		return false, ""
	}
	defer resp2.Body.Close()

	if resp2.StatusCode >= 400 {
		reason := fmt.Sprintf("http_%d", resp2.StatusCode)
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", reason, "duration_ms", time.Since(t0).Milliseconds())
		return true, reason
	}

	body, _ := io.ReadAll(io.LimitReader(resp2.Body, 8192))
	if looksLikeSoft404(body, resp2.Header.Get("Content-Type")) {
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "soft_404", "duration_ms", time.Since(t0).Milliseconds())
		return true, "soft_404"
	}
	logEvent(ctx, slog.LevelDebug, "heal.check", "url", url, "status", types.StatusHealthy, "duration_ms", time.Since(t0).Milliseconds())
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

func (c *CloudflareKV) Put(ctx context.Context, key string, record types.LinkRecord) error {
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

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

func runWorker(id int, q *DiscoveryQueue, kv *CloudflareKV, client *http.Client, wg *sync.WaitGroup) {
	defer wg.Done()
	for url := range q.Chan() {
		ctx := context.Background()
		t0 := time.Now()

		dead, reason := CheckLink(ctx, client, url)
		if !dead {
			rec := types.LinkRecord{
				Status:      types.StatusHealthy,
				OriginalURL: url,
				HealedAt:    time.Now().UTC().Format(time.RFC3339),
			}
			if err := kv.Put(ctx, types.KeyFor(url), rec); err != nil {
				logEvent(ctx, slog.LevelError, "heal.write",
					"url", url, "status", types.StatusHealthy, "error", err.Error(),
				)
			} else {
				logEvent(ctx, slog.LevelInfo, "heal.write",
					"url", url, "url_key", types.KeyFor(url), "status", types.StatusHealthy,
					"duration_ms", time.Since(t0).Milliseconds(),
				)
			}
			continue
		}

		healed, err := CheckArchive(ctx, client, url)
		if err != nil {
			if err == ErrCircuitOpen {
				rec := types.LinkRecord{
					Status:      types.StatusPending,
					OriginalURL: url,
					HealedAt:    time.Now().UTC().Format(time.RFC3339),
					Reason:      "circuit_open",
				}
				_ = kv.Put(ctx, types.KeyFor(url), rec)
				logEvent(ctx, slog.LevelWarn, "heal.defer",
					"url", url,
					"reason", "circuit_open",
					"circuit", waybackBreaker.State(),
					"circuit_name", "healer_wayback",
					"duration_ms", time.Since(t0).Milliseconds(),
				)
				continue
			}
			rec := types.LinkRecord{
				Status:      types.StatusDead,
				OriginalURL: url,
				HealedAt:    time.Now().UTC().Format(time.RFC3339),
				Reason:      reason,
			}
			_ = kv.Put(ctx, types.KeyFor(url), rec)
			logEvent(ctx, slog.LevelInfo, "heal.write",
				"url", url, "status", types.StatusDead, "reason", reason,
				"duration_ms", time.Since(t0).Milliseconds(),
			)
			continue
		}

		rec := types.LinkRecord{
			Status:      types.StatusHealed,
			OriginalURL: url,
			ResolvedURL: healed,
			HealedAt:    time.Now().UTC().Format(time.RFC3339),
			Reason:      reason,
		}
		if err := kv.Put(ctx, types.KeyFor(url), rec); err != nil {
			logEvent(ctx, slog.LevelError, "heal.write",
				"url", url, "status", types.StatusHealed, "error", err.Error(),
			)
		} else {
			logEvent(ctx, slog.LevelInfo, "heal.write",
				"url", url,
				"url_key", types.KeyFor(url),
				"status", types.StatusHealed,
				"reason", reason,
				"resolved_url", healed,
				"duration_ms", time.Since(t0).Milliseconds(),
			)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP API
// ---------------------------------------------------------------------------

func startHTTPServer(addr string, q *DiscoveryQueue) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":          "ok",
			"wayback_circuit": waybackBreaker.State(),
		})
	})

	mux.HandleFunc("/v1/discover", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req types.DiscoverRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		logEvent(r.Context(), slog.LevelInfo, "heal.check",
			"event_note", "discover_enqueued",
			"count", count,
		)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.DiscoverResponse{Enqueued: count})
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		logEvent(context.Background(), slog.LevelInfo, "heal.check",
			"event_note", "http_listen",
			"reason", addr,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logEvent(context.Background(), slog.LevelError, "heal.check",
				"error", err.Error(),
			)
			os.Exit(1)
		}
	}()
	return srv
}

func main() {
	setupSlog()
	cfg := loadConfig()
	logEvent(context.Background(), slog.LevelInfo, "heal.check",
		"event_note", "startup",
		"component", "healer",
	)

	if cfg.CFAccountID == "" || cfg.CFAPIToken == "" || cfg.CFKVNamespaceID == "" {
		logEvent(context.Background(), slog.LevelWarn, "heal.write",
			"reason", "cf_credentials_missing",
		)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	kv := &CloudflareKV{
		AccountID:   cfg.CFAccountID,
		NamespaceID: cfg.CFKVNamespaceID,
		Token:       cfg.CFAPIToken,
		Client:      client,
	}

	q := NewDiscoveryQueue(10_000)
	q.Enqueue("https://dead-example.com/missing-page")
	q.Enqueue("https://httpstat.us/404")

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerCount; i++ {
		wg.Add(1)
		go runWorker(i, q, kv, client, &wg)
	}

	srv := startHTTPServer(cfg.ListenAddr, q)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	<-ctx.Done()
	stop()

	logEvent(context.Background(), slog.LevelInfo, "heal.check", "event_note", "shutdown")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	close(q.ch)
	wg.Wait()
	logEvent(context.Background(), slog.LevelInfo, "heal.check", "event_note", "stopped")
}
