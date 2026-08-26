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

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ---------------------------------------------------------------------------
// HTTP API
// ---------------------------------------------------------------------------

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

func startHTTPServer(addr string, q *DiscoveryQueue, kv *CloudflareKV, tel *Telemetry, adminToken string) *http.Server {
	mux := http.NewServeMux()
	audit := &AuditLog{}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		paused := "false"
		if q.Paused() {
			paused = "true"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":           "ok",
			"wayback_circuit":  waybackBreaker.State(),
			"discovery_paused": paused,
		})
	})

	mux.Handle("/metrics", tel.PromHandler())

	// Phase 7A — JSON snapshot for command-centre (polyglot admin-stats-response)
	mux.HandleFunc("/v1/admin/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		stats := tel.Snapshot(q.Depth(), "healer_wayback", waybackBreaker.State(), waybackBreaker.Trips())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	})

	registerAdminRoutes(mux, q, kv, tel, audit, adminToken)

	// otelhttp extracts W3C traceparent via the global TextMapPropagator set in InitTelemetry.
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

	q.Enqueue("https://dead-example.com/missing-page")
	q.Enqueue("https://httpstat.us/404")

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerCount; i++ {
		wg.Add(1)
		go runWorker(i, q, kv, client, &wg, tel)
	}

	srv := startHTTPServer(cfg.ListenAddr, q, kv, tel, cfg.AdminToken)

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
