package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
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
	TursoURL        string
	TursoToken      string
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
		TursoURL:        os.Getenv("TURSO_DATABASE_URL"),
		TursoToken:      os.Getenv("TURSO_AUTH_TOKEN"),
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

// ---------------------------------------------------------------------------
// HTTP API
// ---------------------------------------------------------------------------

type discoverRequest struct {
	URLs []string `json:"urls"`
	URL  string   `json:"url"`
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, traceparent, tracestate")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func startHTTPServer(addr string, q *DiscoveryQueue, kv *CloudflareKV, tel *Telemetry, audit *AuditLog, adminToken string) *http.Server {
	mux := http.NewServeMux()
	registerAdminRoutes(mux, q, kv, tel, audit, adminToken)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"wayback_circuit": waybackBreaker.State(),
			"queue_depth":     q.Depth(),
			"paused":          q.Paused(),
		})
	})
	mux.Handle("/metrics", tel.PrometheusHandler())
	mux.HandleFunc("GET /v1/admin/stats", func(w http.ResponseWriter, r *http.Request) {
		// Snapshot matches polyglot admin-stats-response (no extra fields).
		stats := tel.Snapshot(q.Depth(), "healer_wayback", waybackBreaker.State(), waybackBreaker.Trips())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	})
	mux.Handle("POST /v1/discover", otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx, span := tel.Tracer.Start(ctx, "autofix.discover.http", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		var req discoverRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		count := 0
		if req.URL != "" {
			q.Enqueue(req.URL)
			count++
		}
		for _, u := range req.URLs {
			if u == "" {
				continue
			}
			q.Enqueue(u)
			count++
		}
		tel.RecordDiscoverHTTP(ctx)
		span.SetAttributes(attribute.Int("autofix.enqueued", count))
		logEvent(ctx, slog.LevelInfo, "queue.consume", "count", count, "source", "http")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"enqueued": count})
	}), "autofix.discover.http"))

	srv := &http.Server{Addr: addr, Handler: withCORS(mux)}
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

	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	kv := &CloudflareKV{
		AccountID:   cfg.CFAccountID,
		NamespaceID: cfg.CFKVNamespaceID,
		Token:       cfg.CFAPIToken,
		Client:      client,
	}

	var events HealEventWriter = NopHealEvents{}
	if te, terr := OpenTurso(cfg.TursoURL, cfg.TursoToken, client); terr != nil {
		slog.Warn("Turso disabled", "event", "startup", "component", "healer", "error", terr.Error())
	} else {
		events = te
		if events.Enabled() {
			slog.Info("Turso heal_events enabled", "event", "startup", "component", "healer")
		} else {
			slog.Info("Turso not configured (set TURSO_DATABASE_URL + TURSO_AUTH_TOKEN)", "event", "startup", "component", "healer")
		}
	}

	audit := &AuditLog{}

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerCount; i++ {
		wg.Add(1)
		go runWorker(i, q, kv, client, events, &wg, tel)
	}

	srv := startHTTPServer(cfg.ListenAddr, q, kv, tel, audit, cfg.AdminToken)

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	<-sigCtx.Done()
	stop()

	slog.Info("shutting down", "event", "shutdown", "component", "healer")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	tel.Shutdown(shutdownCtx)
	_ = events.Close()
	close(q.ch)
	wg.Wait()
	slog.Info("healer stopped", "event", "shutdown", "component", "healer")
}
