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
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ---------------------------------------------------------------------------
// Wayback Machine (protected by circuit breaker)
// ---------------------------------------------------------------------------

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
		recordSpanError(span, err)
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "network_error", "duration_ms", time.Since(start).Milliseconds())
		return true, "network_error"
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 400:
		// soft-404 check on GET for HTML
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") || resp.StatusCode == 200 {
			getReq, gerr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if gerr == nil {
				getReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoFix-Healer/1.0)")
				getResp, gerr2 := client.Do(getReq)
				if gerr2 == nil {
					defer getResp.Body.Close()
					body, _ := io.ReadAll(io.LimitReader(getResp.Body, 64*1024))
					if looksLikeSoft404(body, getResp.Header.Get("Content-Type")) {
						outcome = "soft404"
						tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
							metric.WithAttributes(attribute.String("outcome", outcome)))
						span.SetAttributes(attribute.String("autofix.check_outcome", outcome))
						logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "reason", "soft404", "duration_ms", time.Since(start).Milliseconds())
						return true, "soft404"
					}
				}
			}
		}
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", outcome)))
		span.SetAttributes(attribute.String("autofix.check_outcome", outcome))
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "status_code", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
		return false, ""
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		outcome = "dead"
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", outcome)))
		span.SetAttributes(attribute.String("autofix.check_outcome", outcome), attribute.Int("http.status_code", resp.StatusCode))
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "status_code", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
		return true, fmt.Sprintf("http_%d", resp.StatusCode)
	default:
		outcome = "other"
		tel.CheckDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", outcome)))
		span.SetAttributes(attribute.String("autofix.check_outcome", outcome), attribute.Int("http.status_code", resp.StatusCode))
		logEvent(ctx, slog.LevelInfo, "heal.check", "url", url, "status_code", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
		return true, fmt.Sprintf("http_%d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Cloudflare KV
// ---------------------------------------------------------------------------

type CloudflareKV struct {
	AccountID   string
	NamespaceID string
	Token       string
	Client      *http.Client
}

func keyFor(url string) string {
	return base64.StdEncoding.EncodeToString([]byte(url))
}

func (kv *CloudflareKV) Put(ctx context.Context, key string, rec LinkRecord) error {
	if kv.AccountID == "" || kv.Token == "" || kv.NamespaceID == "" {
		return nil
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	apiURL := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/%s",
		kv.AccountID, kv.NamespaceID, key,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+kv.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := kv.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("kv put %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

func runWorker(id int, q *DiscoveryQueue, kv *CloudflareKV, client *http.Client, wg *sync.WaitGroup, tel *Telemetry) {
	defer wg.Done()
	for url := range q.Chan() {
		for q.Paused() {
			time.Sleep(200 * time.Millisecond)
		}
		healOne(context.Background(), url, kv, client, tel)
	}
}

func healOne(ctx context.Context, url string, kv *CloudflareKV, client *http.Client, tel *Telemetry) {
	ctx, span := tel.Tracer.Start(ctx, "autofix.heal",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("autofix.url", url)),
	)
	defer span.End()

	start := time.Now()
	result := "unknown"

	dead, reason := CheckLink(ctx, client, url, tel)
	if !dead {
		result = "healthy"
		_ = kv.Put(ctx, keyFor(url), LinkRecord{
			Status:      "HEALTHY",
			OriginalURL: url,
			HealedAt:    time.Now().UTC().Format(time.RFC3339),
		})
		tel.RecordLink(ctx, "HEALTHY")
		tel.RecordHeal(ctx, time.Since(start).Seconds(), result)
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
			tel.RecordLink(ctx, "PENDING")
			tel.RecordHeal(ctx, time.Since(start).Seconds(), result)
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
		tel.RecordLink(ctx, "DEAD")
		tel.RecordHeal(ctx, time.Since(start).Seconds(), result)
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
	tel.RecordLink(ctx, "HEALED")
	tel.RecordHeal(ctx, time.Since(start).Seconds(), result)
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
