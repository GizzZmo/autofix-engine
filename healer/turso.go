package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HealEvent is an append-only audit row for SQL analytics (Turso).
// Edge contract remains Workers KV (LinkRecord); this is the portable SoR trail.
type HealEvent struct {
	OriginalURL string
	ResolvedURL string
	Status      string
	Reason      string
	HealedAt    string
	DurationMS  int64
	URLKey      string
}

// HealEventWriter persists heal outcomes for reporting. Optional: no-op when unset.
type HealEventWriter interface {
	Enabled() bool
	Insert(ctx context.Context, ev HealEvent) error
	Close() error
}

// NopHealEvents discards events (Turso not configured).
type NopHealEvents struct{}

func (NopHealEvents) Enabled() bool                           { return false }
func (NopHealEvents) Insert(context.Context, HealEvent) error { return nil }
func (NopHealEvents) Close() error                            { return nil }

// TursoClient writes via the libSQL HTTP pipeline API (no CGO).
type TursoClient struct {
	baseURL string
	token   string
	client  *http.Client
}

// OpenTurso returns a writer when TURSO_DATABASE_URL + TURSO_AUTH_TOKEN are set.
// URL forms: libsql://…, https://…, or host only.
func OpenTurso(databaseURL, authToken string, httpClient *http.Client) (HealEventWriter, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	authToken = strings.TrimSpace(authToken)
	if databaseURL == "" || authToken == "" {
		return NopHealEvents{}, nil
	}
	base := databaseURL
	base = strings.TrimPrefix(base, "libsql://")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	c := &TursoClient{baseURL: base, token: authToken, client: httpClient}
	if err := c.ensureSchema(context.Background()); err != nil {
		return nil, fmt.Errorf("turso schema: %w", err)
	}
	return c, nil
}

func (c *TursoClient) Enabled() bool { return c != nil && c.baseURL != "" }
func (c *TursoClient) Close() error  { return nil }

func (c *TursoClient) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS heal_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  url_key TEXT NOT NULL,
  original_url TEXT NOT NULL,
  resolved_url TEXT,
  status TEXT NOT NULL,
  reason TEXT,
  healed_at TEXT NOT NULL,
  duration_ms INTEGER,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`,
		`CREATE INDEX IF NOT EXISTS idx_heal_events_status ON heal_events(status)`,
		`CREATE INDEX IF NOT EXISTS idx_heal_events_original ON heal_events(original_url)`,
		`CREATE INDEX IF NOT EXISTS idx_heal_events_healed_at ON heal_events(healed_at)`,
	}
	for _, sql := range stmts {
		if err := c.exec(ctx, sql, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *TursoClient) Insert(ctx context.Context, ev HealEvent) error {
	healedAt := ev.HealedAt
	if healedAt == "" {
		healedAt = time.Now().UTC().Format(time.RFC3339)
	}
	args := []any{
		ev.URLKey,
		ev.OriginalURL,
		nullStr(ev.ResolvedURL),
		ev.Status,
		nullStr(ev.Reason),
		healedAt,
		ev.DurationMS,
	}
	const sql = `INSERT INTO heal_events (url_key, original_url, resolved_url, status, reason, healed_at, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	return c.exec(ctx, sql, args)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type pipelineReq struct {
	Requests []pipelineStmt `json:"requests"`
}

type pipelineStmt struct {
	Type string      `json:"type"`
	Stmt pipelineSQL `json:"stmt"`
}

type pipelineSQL struct {
	SQL  string        `json:"sql"`
	Args []pipelineArg `json:"args,omitempty"`
}

type pipelineArg struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

func (c *TursoClient) exec(ctx context.Context, sql string, args []any) error {
	stmt := pipelineSQL{SQL: sql}
	for _, a := range args {
		pa := pipelineArg{Value: a}
		switch a.(type) {
		case nil:
			pa.Type = "null"
			pa.Value = nil
		case int, int32, int64:
			pa.Type = "integer"
		default:
			pa.Type = "text"
		}
		stmt.Args = append(stmt.Args, pa)
	}
	body, err := json.Marshal(pipelineReq{
		Requests: []pipelineStmt{{Type: "execute", Stmt: stmt}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/pipeline", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("turso http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Results []struct {
			Type  string `json:"type"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		for _, r := range parsed.Results {
			if r.Error != nil && r.Error.Message != "" {
				return fmt.Errorf("turso: %s", r.Error.Message)
			}
		}
	}
	return nil
}
