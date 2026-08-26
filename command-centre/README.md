# AutoFix Command Centre (Phase 7A — stats only)

Minimal **observe** UI for the Go healer. Contracts:
[polyglot COMMAND_CENTRE.md](https://github.com/GizzZmo/autofix-polyglot/blob/main/docs/COMMAND_CENTRE.md).

## What it shows

| Panel | Source |
|-------|--------|
| Health + Wayback circuit | `GET /healthz` (fallback) or `GET /v1/admin/stats` |
| Queue depth | admin stats / `autofix_queue_depth` |
| Discover requests | admin stats / `autofix_discover_requests_total` |
| Links written by status | admin stats / `autofix_links_total` |
| Circuit state / trips | admin stats / Prometheus circuit metrics |
| Heal latency (avg) | admin stats / histogram count+sum |

**No write actions** in 7A (no override, no circuit reset).

## Run locally

1. Start the healer:

```bash
cd healer && go run .
# listens on :8080 by default
```

2. Open the UI (any static server, or open the file):

```bash
cd command-centre
python3 -m http.server 8090
# → http://localhost:8090
```

3. Set **Healer base URL** to `http://localhost:8080` and click **Refresh**.

The UI **prefers** `GET /v1/admin/stats` (JSON). If that fails, it falls back to parsing `GET /metrics` Prometheus text.

### CORS

The healer enables `Access-Control-Allow-Origin: *` on all routes via `withCORS` middleware (local/dev). For production, terminate CORS at a reverse proxy and restrict origins.

### Security

- Do **not** expose `/metrics`, `/v1/admin/stats`, or this UI on the public internet without auth.
- Never put Cloudflare API tokens in the browser.
