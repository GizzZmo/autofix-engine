package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

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
	_ = godotenv.Load() // optional .env

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
// Link record (shared schema with the Edge Worker)
// ---------------------------------------------------------------------------

type LinkRecord struct {
	Status       string `json:"status"` // PENDING | HEALED | DEAD | HEALTHY
	OriginalURL  string `json:"original_url"`
	ResolvedURL  string `json:"resolved_url,omitempty"`
	DiscoveredAt string `json:"discovered_at,omitempty"`
	HealedAt     string `json:"healed_at,omitempty"`
	Reason       string `json:"reason,omitempty"` // e.g. http_404, soft_404
}

// ---------------------------------------------------------------------------
// In-memory + channel based discovery queue
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
		log.Printf("queue full, dropping: %s", url)
	}
}

func (q *DiscoveryQueue) Chan() <-chan string { return q.ch }

// ---------------------------------------------------------------------------
// Wayback Machine
// ---------------------------------------------------------------------------

type ArchiveResponse struct {
	ArchivedSnapshots struct {
		Closest struct {
			Available bool   `json:"available"`
			URL       string `json:"url"`
		} `json:"closest"`
	} `json:"archived_snapshots"`
}

func CheckArchive(client *http.Client, url string) (string, error) {
	apiURL := fmt.Sprintf("https://archive.org/wayback/available?url=%s", url)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AutoFix-Healer/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var res ArchiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.ArchivedSnapshots.Closest.Available && res.ArchivedSnapshots.Closest.URL != "" {
		return res.ArchivedSnapshots.Closest.URL, nil
	}
	return "", fmt.Errorf("no archive found")
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
	// Extract <title> if present
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
	// Body snippet (first ~8KB is enough)
	snippet := text
	if len(snippet) > 8192 {
		snippet = snippet[:8192]
	}
	return soft404BodyRe.MatchString(snippet)
}

// ---------------------------------------------------------------------------
// Link health check (HEAD + GET + soft-404)
// ---------------------------------------------------------------------------

// CheckLink returns (isDead, reason).
func CheckLink(client *http.Client, url string) (bool, string) {
	// Primary: HEAD
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return true, "invalid_url"
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoFix-Healer/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return true, "network_error"
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		return true, fmt.Sprintf("http_%d", resp.StatusCode)
	}

	// Secondary: small GET for soft-404 inspection (and servers that reject HEAD)
	req2, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false, ""
	}
	req2.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoFix-Healer/1.0)")
	req2.Header.Set("Range", "bytes=0-8191")

	resp2, err := client.Do(req2)
	if err != nil {
		// HEAD succeeded; treat as healthy
		return false, ""
	}
	defer resp2.Body.Close()

	if resp2.StatusCode >= 400 {
		return true, fmt.Sprintf("http_%d", resp2.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp2.Body, 8192))
	ct := resp2.Header.Get("Content-Type")
	if looksLikeSoft404(body, ct) {
		return true, "soft_404"
	}
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

func (c *CloudflareKV) Put(key string, record LinkRecord) error {
	if c.AccountID == "" || c.NamespaceID == "" || c.Token == "" {
		return fmt.Errorf("Cloudflare credentials not configured (CF_ACCOUNT_ID / CF_API_TOKEN / CF_KV_NAMESPACE_ID)")
	}

	body, err := json.Marshal(record)
	if err != nil {
		return err
	}

	url := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/%s",
		c.AccountID, c.NamespaceID, key,
	)

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
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
// Worker that processes the queue
// ---------------------------------------------------------------------------

func runWorker(id int, q *DiscoveryQueue, kv *CloudflareKV, client *http.Client, wg *sync.WaitGroup) {
	defer wg.Done()
	for url := range q.Chan() {
		log.Printf("[worker-%d] verifying %s", id, url)

		dead, reason := CheckLink(client, url)
		if !dead {
			log.Printf("[worker-%d] healthy: %s", id, url)
			_ = kv.Put(keyFor(url), LinkRecord{
				Status:      "HEALTHY",
				OriginalURL: url,
				HealedAt:    time.Now().UTC().Format(time.RFC3339),
			})
			continue
		}

		log.Printf("[worker-%d] dead (%s) — searching archive for %s", id, reason, url)
		healed, err := CheckArchive(client, url)
		if err != nil {
			log.Printf("[worker-%d] no archive: %v", id, err)
			_ = kv.Put(keyFor(url), LinkRecord{
				Status:      "DEAD",
				OriginalURL: url,
				HealedAt:    time.Now().UTC().Format(time.RFC3339),
				Reason:      reason,
			})
			continue
		}

		log.Printf("[worker-%d] HEALED %s → %s", id, url, healed)
		err = kv.Put(keyFor(url), LinkRecord{
			Status:      "HEALED",
			OriginalURL: url,
			ResolvedURL: healed,
			HealedAt:    time.Now().UTC().Format(time.RFC3339),
			Reason:      reason,
		})
		if err != nil {
			log.Printf("[worker-%d] failed to write KV: %v", id, err)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP discovery API
// ---------------------------------------------------------------------------

type discoverRequest struct {
	URLs []string `json:"urls"`
	URL  string   `json:"url"` // single-link convenience
}

func startHTTPServer(addr string, q *DiscoveryQueue) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	mux.HandleFunc("/v1/discover", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req discoverRequest
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"enqueued": count})
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Printf("discovery API listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()
	return srv
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()
	log.Println("🚀 AutoFix Healer Engine starting...")

	if cfg.CFAccountID == "" || cfg.CFAPIToken == "" || cfg.CFKVNamespaceID == "" {
		log.Println("⚠️  Cloudflare credentials missing — healed links will NOT be written to KV.")
		log.Println("   Set CF_ACCOUNT_ID, CF_API_TOKEN, CF_KV_NAMESPACE_ID (see .env.example)")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	kv := &CloudflareKV{
		AccountID:   cfg.CFAccountID,
		NamespaceID: cfg.CFKVNamespaceID,
		Token:       cfg.CFAPIToken,
		Client:      client,
	}

	q := NewDiscoveryQueue(10_000)

	// Seed a couple of demo links so the process is immediately useful
	q.Enqueue("https://dead-example.com/missing-page")
	q.Enqueue("https://httpstat.us/404")

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerCount; i++ {
		wg.Add(1)
		go runWorker(i, q, kv, client, &wg)
	}

	// HTTP discovery endpoint (Edge Worker can POST here)
	srv := startHTTPServer(cfg.ListenAddr, q)

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	<-ctx.Done()
	stop()

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	close(q.ch)
	wg.Wait()
	log.Println("healer stopped")
}
