# 🔧 AutoFix: The Self-Healing Web Layer

AutoFix eliminates 404s and broken external links without requiring database migrations.

### How it works
1. **Intercept** — The Edge Worker parses HTML as it leaves your origin.
2. **Lookup** — It checks a global Cloudflare KV registry for every external link.
3. **Resolve** — Dead links are hot-swapped with a Wayback Machine archive (`rel="nofollow archived"`).
4. **Discover** — Unknown links are written as `PENDING` into KV and enqueued on **Cloudflare Queues** (with HTTP fallback).
5. **Heal** — A queue consumer forwards batches to the Go healer, which verifies links (incl. soft-404), queries the Internet Archive, and writes results back into KV.

```
Browser → Cloudflare Worker (HTMLRewriter + KV)
                │
                ├─ PENDING → KV
                └─ send → Cloudflare Queue (autofix-discovery)
                              │
                              ▼
                     Queue consumer + circuit breaker
                              │
                              ▼ POST /v1/discover
                        Go Healer → soft-404 check
                              │
                              ▼ circuit breaker
                         Wayback Machine → KV (HEALED / DEAD / PENDING)
```

---

## Resilience

| Layer | Mechanism |
|-------|-----------|
| **Discovery** | Cloudflare Queues (durable) + HTTP fallback |
| **Queue consumer** | Per-message `ack` / `retry` with exponential backoff; poison messages acked |
| **Edge → Healer** | Circuit breaker (5 failures → open 30s → 1 half-open probe) |
| **Healer → Wayback** | Circuit breaker (5 failures → open 60s); `no archive` does not trip |
| **On circuit open** | Longer queue retry delay; Wayback open writes `PENDING` + `reason: circuit_open` |
| **DLQ** | Optional `autofix-discovery-dlq` after `max_retries` |

`GET /healthz` returns `{ "status": "ok", "wayback_circuit": "closed|open|half_open" }`.

---

## Deploy (requires your Cloudflare account)

### Prerequisites
```bash
npm install -g wrangler   # or use npx
wrangler login
```

### 1. Create KV + Queues

```bash
cd edge-worker
npm install

npx wrangler kv:namespace create AUTOFIX_KV
npx wrangler kv:namespace create AUTOFIX_KV --preview

npx wrangler queues create autofix-discovery
# optional DLQ:
npx wrangler queues create autofix-discovery-dlq
```

Paste the KV ids into `edge-worker/wrangler.toml`:

```toml
[[kv_namespaces]]
binding = "AUTOFIX_KV"
id = "PASTE_ID_HERE"
preview_id = "PASTE_PREVIEW_ID_HERE"
```

Uncomment `dead_letter_queue` in `wrangler.toml` only after creating the DLQ.

### 2. Deploy the Worker

```bash
cd edge-worker
npx wrangler deploy
```

### 3. Run the healer and set `HEALER_DISCOVER_URL`

```bash
cd healer
cp .env.example .env
# CF_ACCOUNT_ID, CF_API_TOKEN, CF_KV_NAMESPACE_ID
go run .
```

Expose it (Cloudflare Tunnel / ngrok), then:

```bash
cd edge-worker
npx wrangler secret put HEALER_DISCOVER_URL
# https://<tunnel-host>/v1/discover
```

### 4. Verify

```bash
npx wrangler tail
curl https://<healer>/healthz
```

---

## Healer

| Variable | Description |
|----------|-------------|
| `CF_ACCOUNT_ID` | Cloudflare Account ID |
| `CF_API_TOKEN` | **Workers KV Storage → Edit** |
| `CF_KV_NAMESPACE_ID` | Same KV `id` as in `wrangler.toml` |
| `HEALER_LISTEN` | Default `:8080` |

Endpoints: `GET /healthz`, `POST /v1/discover`.

```bash
cd healer
docker build -t autofix-healer .
docker run --env-file .env -p 8080:8080 autofix-healer
```

---

## Project layout

```
autofix-engine/
├── edge-worker/
│   ├── src/index.ts         # HTMLRewriter, queue consumer, circuit breaker
│   ├── wrangler.toml        # KV + Queues
│   ├── package.json
│   └── tsconfig.json
├── healer/
│   ├── main.go              # discovery API, soft-404, Wayback, KV writer
│   ├── circuitbreaker.go    # Closed / Open / Half-Open
│   ├── go.mod
│   ├── Dockerfile
│   └── .env.example
├── client/autofix.js
├── .github/workflows/ci.yml
├── .gitignore
└── README.md
```

---

## Tech stack

- **Edge**: Cloudflare Workers (`HTMLRewriter`, KV, Queues) + circuit breaker
- **Healer**: Go 1.22 — soft-404, Wayback circuit breaker, CF KV API
- **Client**: vanilla JS healed-link indicators
- **CI**: GitHub Actions (tsc + `go build` / `go vet`)

---

## Notes

- Prefer Queues for discovery; HTTP is a fallback when the queue binding is missing.
- Circuit breakers are in-process (per Worker isolate / healer process). Scale horizontally with care, or move shared state later if needed.
- Soft-404 detection uses title/body heuristics after a successful HTTP status.
