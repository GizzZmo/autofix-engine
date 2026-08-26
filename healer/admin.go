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
