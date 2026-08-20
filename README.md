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
                     Queue consumer (same Worker)
                              │
                              ▼ POST /v1/discover
                        Go Healer → Wayback → KV (HEALED/DEAD)
```

---

## Deploy (you run these — requires your Cloudflare account)

### Prerequisites
```bash
npm install -g wrangler   # or use npx
wrangler login            # opens browser, authorises your CF account
```

### 1. Create KV + Queues

```bash
cd edge-worker
npm install

# KV namespaces
npx wrangler kv:namespace create AUTOFIX_KV
# → copy the "id"  e.g. a1b2c3d4...

npx wrangler kv:namespace create AUTOFIX_KV --preview
# → copy the "preview_id"

# Durable discovery queues
npx wrangler queues create autofix-discovery
npx wrangler queues create autofix-discovery-dlq
```

Edit `edge-worker/wrangler.toml` and paste the KV ids:

```toml
[[kv_namespaces]]
binding = "AUTOFIX_KV"
id = "PASTE_ID_HERE"
preview_id = "PASTE_PREVIEW_ID_HERE"
```

### 2. Deploy the Worker

```bash
cd edge-worker
npx wrangler deploy
```

### 3. Point the Worker at a running healer

Start the healer somewhere reachable (local tunnel, VPS, Fly, Railway, …):

```bash
cd healer
cp .env.example .env
# fill CF_ACCOUNT_ID, CF_API_TOKEN, CF_KV_NAMESPACE_ID
go run .
# listens on :8080
```

Expose it (example with Cloudflare Tunnel or ngrok):

```bash
# example
cloudflared tunnel --url http://localhost:8080
# → https://random.trycloudflare.com
```

Then set the secret on the Worker:

```bash
cd edge-worker
npx wrangler secret put HEALER_DISCOVER_URL
# paste: https://random.trycloudflare.com/v1/discover
```

Redeploy is not required for secrets; they are available immediately.

### 4. Verify

```bash
# tail live logs
npx wrangler tail

# hit a page behind the Worker that contains external links
# you should see DISCOVERED / Queued messages, then healer logs
```

---

## Healer env vars

| Variable | Description |
|----------|-------------|
| `CF_ACCOUNT_ID` | Cloudflare Account ID |
| `CF_API_TOKEN` | Token with **Workers KV Storage → Edit** |
| `CF_KV_NAMESPACE_ID` | Same `id` as in `wrangler.toml` |
| `HEALER_LISTEN` | Default `:8080` |

Endpoints: `GET /healthz`, `POST /v1/discover`.

Docker:

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
│   ├── src/index.ts      # fetch handler + queue consumer
│   ├── wrangler.toml     # KV + Queues bindings
│   ├── package.json
│   └── tsconfig.json
├── healer/
│   ├── main.go
│   ├── go.mod
│   ├── Dockerfile
│   └── .env.example
├── client/autofix.js
├── .github/workflows/ci.yml
└── README.md
```

---

## Tech stack

- **Edge**: Cloudflare Workers (`HTMLRewriter`, KV, **Queues**)
- **Healer**: Go 1.22 — soft-404 heuristics, Wayback Machine, CF KV API
- **Client**: vanilla JS indicators
- **CI**: GitHub Actions

---

## Notes

- Queue path is preferred; HTTP POST remains a fallback when `DISCOVERY_QUEUE` is unavailable.
- Dead-letter queue `autofix-discovery-dlq` captures poison messages after retries.
- For multi-million-link sites, increase `max_batch_size` / scale the Go healer horizontally.
