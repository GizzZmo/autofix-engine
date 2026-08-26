-- AutoFix heal event log (Turso / libSQL)
-- System of record for analytics; edge rewrite contract remains Cloudflare KV.
--
-- Apply:
--   turso db shell <dbname> < healer/schema/heal_events.sql
-- Or let the healer auto-migrate on startup when TURSO_* is set.

CREATE TABLE IF NOT EXISTS heal_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  url_key TEXT NOT NULL,
  original_url TEXT NOT NULL,
  resolved_url TEXT,
  status TEXT NOT NULL CHECK (status IN ('HEALED', 'DEAD', 'HEALTHY', 'PENDING')),
  reason TEXT,
  healed_at TEXT NOT NULL,
  duration_ms INTEGER,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_heal_events_status ON heal_events (status);
CREATE INDEX IF NOT EXISTS idx_heal_events_original ON heal_events (original_url);
CREATE INDEX IF NOT EXISTS idx_heal_events_healed_at ON heal_events (healed_at);
CREATE INDEX IF NOT EXISTS idx_heal_events_url_key ON heal_events (url_key);

-- Example queries:
-- SELECT status, COUNT(*) FROM heal_events GROUP BY status;
-- SELECT original_url, resolved_url, reason FROM heal_events WHERE status = 'HEALED' ORDER BY healed_at DESC LIMIT 50;
