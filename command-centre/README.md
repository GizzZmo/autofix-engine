# AutoFix Command Centre (Phase 7A — stats only)

Minimal **observe** UI for the Go healer. Contracts:
[polyglot COMMAND_CENTRE.md](https://github.com/GizzZmo/autofix-polyglot/blob/main/docs/COMMAND_CENTRE.md).

## What it shows

| Panel | Source |
|-------|--------|
| Health + Wayback circuit | `GET /healthz` |
| Queue depth | `autofix_queue_depth` |
| Discover requests | `autofix_discover_requests_total` |
| Links written by status | `autofix_links_total` |
| Circuit state / trips | `autofix_circuit_state`, `autofix_circuit_trips_total` |
| Heal latency (avg) | `autofix_heal_duration_seconds` count/sum |

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

### CORS

Browsers block cross-origin fetches unless the healer sends CORS headers.
For local dev either:

- Serve the UI from the same origin as the healer later, or
- Open Chrome with disabled web security (dev only), or
- Add CORS middleware on the healer (follow-up).

If `/metrics` is blocked, the UI shows the error and still tries `/healthz`.

## Optional: JSON stats

When the healer implements `GET /v1/admin/stats` (schema in polyglot), the UI can prefer that over Prometheus parsing. Not required for 7A.

## Security

- Do **not** expose `/metrics` or this UI on the public internet without auth.
- Never put Cloudflare API tokens in the browser.
