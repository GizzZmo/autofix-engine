# AutoFix Command Centre (Phase 7A observe + 7B supervise)

Minimal HCI for the Go healer. Contracts:
[polyglot COMMAND_CENTRE.md](https://github.com/GizzZmo/autofix-polyglot/blob/main/docs/COMMAND_CENTRE.md)
· [ADR-009](https://github.com/GizzZmo/autofix-polyglot/blob/main/docs/adr/009-supervised-admin.md).

## What it shows

| Panel | Source |
|-------|--------|
| Health + Wayback circuit + pause | `GET /healthz` and/or `GET /v1/admin/stats` |
| Queue depth | admin stats / `autofix_queue_depth` |
| Discover requests | admin stats / `autofix_discover_requests_total` |
| Links written by status | admin stats / `autofix_links_total` |
| Circuit state / trips | admin stats / Prometheus circuit metrics |
| Heal latency (avg) | admin stats / histogram count+sum |
| Audit ring | `GET /v1/admin/audit` (Bearer token) |

## Supervised writes (Phase 7B)

All mutations go through `POST /v1/admin/actions` with:

```
Authorization: Bearer <ADMIN_TOKEN>
```

| Action | Fields |
|--------|--------|
| `link.requeue` | `urls` (one per line) |
| `link.override` | `urls`, `status`, optional `resolved_url` |
| `circuit.reset` | `circuit_name=healer_wayback` |
| `discovery.pause` / `discovery.resume` | — |

`actor` and `reason` are required on every request (never inferred from the token).
The token is kept in `sessionStorage` only — never sent to Cloudflare.

If `ADMIN_TOKEN` is unset on the healer, writes return **503**.

## Run locally

1. Start the healer with a token:

```bash
cd healer
export ADMIN_TOKEN=dev-only-change-me
go run .
# listens on :8080 by default
```

2. Serve the UI:

```bash
cd command-centre
python3 -m http.server 8090
# → http://localhost:8090
```

3. Set **Healer URL** to `http://localhost:8080`, paste the same token, actor, and a reason, then use the supervise panel.

The UI **prefers** `GET /v1/admin/stats` (JSON). If that fails, it falls back to parsing `GET /metrics` Prometheus text.

### CORS

The healer enables `Access-Control-Allow-Origin: *` and allows
`Authorization, Content-Type, traceparent, tracestate` via `withCORS` (local/dev).
For production, terminate CORS at a reverse proxy and restrict origins.

### Security

- Do **not** expose `/metrics`, `/v1/admin/*`, or this UI on the public internet without auth.
- Never put Cloudflare API tokens in the browser.
- Treat `ADMIN_TOKEN` like a password; rotate if leaked.
