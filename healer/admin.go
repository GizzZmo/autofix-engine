package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const auditRingSize = 200

type AdminActionRequest struct {
	Action         string   `json:"action"`
	Actor          string   `json:"actor"`
	Reason         string   `json:"reason"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	URLs           []string `json:"urls,omitempty"`
	Status         string   `json:"status,omitempty"`
	ResolvedURL    string   `json:"resolved_url,omitempty"`
	CircuitName    string   `json:"circuit_name,omitempty"`
}

type AdminActionResponse struct {
	OK      bool           `json:"ok"`
	Action  string         `json:"action"`
	AuditID string         `json:"audit_id"`
	Ts      string         `json:"ts"`
	Error   string         `json:"error,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
}

type AdminAuditEvent struct {
	AuditID     string         `json:"audit_id"`
	Ts          string         `json:"ts"`
	Actor       string         `json:"actor"`
	Action      string         `json:"action"`
	Reason      string         `json:"reason"`
	OK          bool           `json:"ok"`
	URLs        []string       `json:"urls,omitempty"`
	CircuitName string         `json:"circuit_name,omitempty"`
	Before      map[string]any `json:"before,omitempty"`
	After       map[string]any `json:"after,omitempty"`
	Error       string         `json:"error,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
}

type AuditLog struct {
	mu   sync.Mutex
	ring []AdminAuditEvent
}

func (a *AuditLog) Append(ev AdminAuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.ring) >= auditRingSize {
		a.ring = a.ring[1:]
	}
	a.ring = append(a.ring, ev)
}

func (a *AuditLog) List() []AdminAuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AdminAuditEvent, len(a.ring))
	copy(out, a.ring)
	return out
}

func newAuditID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func recordAdminMetric(ctx context.Context, tel *Telemetry, action string, ok bool) {
	if tel == nil || tel.AdminActions == nil {
		return
	}
	okLabel := "false"
	if ok {
		okLabel = "true"
	}
	tel.AdminActions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("action", action),
		attribute.String("ok", okLabel),
	))
}

func requireAdminToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(token) == "" {
			http.Error(w, `{"error":"admin writes disabled (ADMIN_TOKEN unset)"}`, http.StatusServiceUnavailable)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		got = strings.TrimSpace(got)
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func registerAdminRoutes(mux *http.ServeMux, q *DiscoveryQueue, kv *CloudflareKV, tel *Telemetry, audit *AuditLog, adminToken string) {
	mux.HandleFunc("GET /v1/admin/audit", requireAdminToken(adminToken, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": audit.List()})
	}))

	mux.HandleFunc("POST /v1/admin/actions", requireAdminToken(adminToken, func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx, span := tel.Tracer.Start(ctx, "autofix.admin",
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		var req AdminActionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		ts := time.Now().UTC().Format(time.RFC3339)
		ev := AdminAuditEvent{
			AuditID:     newAuditID(),
			Ts:          ts,
			Actor:       req.Actor,
			Action:      req.Action,
			Reason:      req.Reason,
			URLs:        req.URLs,
			CircuitName: req.CircuitName,
		}
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			ev.TraceID = sc.TraceID().String()
		}

		resp := AdminActionResponse{Action: req.Action, AuditID: ev.AuditID, Ts: ts}

		if req.Actor == "" || req.Reason == "" || req.Action == "" {
			ev.OK = false
			ev.Error = "actor, action, and reason are required"
			resp.Error = ev.Error
			audit.Append(ev)
			recordAdminMetric(ctx, tel, req.Action, false)
			logEvent(ctx, slog.LevelWarn, "admin.action",
				"actor", req.Actor, "action", req.Action, "audit_id", ev.AuditID, "ok", false, "error", ev.Error)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		span.SetAttributes(
			attribute.String("autofix.admin.action", req.Action),
			attribute.String("autofix.admin.actor", req.Actor),
		)

		switch req.Action {
		case "link.requeue":
			if len(req.URLs) == 0 {
				ev.OK = false
				ev.Error = "urls required"
			} else {
				n := 0
				for _, u := range req.URLs {
					if u == "" {
						continue
					}
					q.EnqueueForce(u)
					n++
				}
				ev.OK = true
				resp.OK = true
				resp.Result = map[string]any{"enqueued": n}
				ev.After = resp.Result
			}
		case "link.override":
			if kv == nil {
				ev.OK = false
				ev.Error = "kv client unavailable"
			} else if len(req.URLs) == 0 || req.Status == "" {
				ev.OK = false
				ev.Error = "urls and status required"
			} else {
				written := 0
				for _, u := range req.URLs {
					if u == "" {
						continue
					}
					rec := LinkRecord{
						Status:      req.Status,
						OriginalURL: u,
						ResolvedURL: req.ResolvedURL,
						HealedAt:    ts,
						Reason:      "admin_override:" + req.Reason,
					}
					if err := kv.Put(ctx, keyFor(u), rec); err != nil {
						ev.OK = false
						ev.Error = err.Error()
						break
					}
					tel.RecordLink(ctx, req.Status)
					written++
				}
				if ev.Error == "" {
					ev.OK = true
					resp.OK = true
					resp.Result = map[string]any{"written": written, "status": req.Status}
					ev.After = resp.Result
				}
			}
		case "circuit.reset":
			if req.CircuitName != "" && req.CircuitName != "healer_wayback" {
				ev.OK = false
				ev.Error = "unknown circuit_name"
			} else {
				before := waybackBreaker.State()
				waybackBreaker.Reset()
				ev.OK = true
				resp.OK = true
				ev.Before = map[string]any{"state": before}
				ev.After = map[string]any{"state": waybackBreaker.State()}
				resp.Result = map[string]any{"circuit_name": "healer_wayback", "state": waybackBreaker.State()}
			}
		case "discovery.pause":
			q.SetPaused(true)
			ev.OK = true
			resp.OK = true
			resp.Result = map[string]any{"paused": true}
			ev.After = resp.Result
		case "discovery.resume":
			q.SetPaused(false)
			ev.OK = true
			resp.OK = true
			resp.Result = map[string]any{"paused": false}
			ev.After = resp.Result
		default:
			ev.OK = false
			ev.Error = "unknown action"
		}

		if ev.Error != "" {
			resp.Error = ev.Error
		}
		audit.Append(ev)
		recordAdminMetric(ctx, tel, req.Action, ev.OK)
		lvl := slog.LevelInfo
		if !ev.OK {
			lvl = slog.LevelWarn
		}
		logEvent(ctx, lvl, "admin.action",
			"actor", req.Actor, "action", req.Action, "audit_id", ev.AuditID,
			"ok", ev.OK, "reason", req.Reason)

		code := http.StatusOK
		if !ev.OK {
			code = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}))
}
