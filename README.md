# 🔧 AutoFix: The Self-Healing Web Layer

AutoFix eliminates 404s and broken external links without requiring database migrations.

### How it works
1. **Intercept** — The Edge Worker parses HTML as it leaves your origin.
2. **Lookup** — It checks a global Cloudflare KV registry for the health status of every external link.
3. **Resolve** — Dead links are hot-swapped with a Wayback Machine archive (and marked `rel="nofollow archived"`).
4. **Discover** — Unknown links are written as `PENDING` into KV and (optionally) POSTed to the Healer.
5. **Heal** — A background Go service drains a discovery queue, verifies links, queries the Internet Archive, and writes the healed record back into Cloudflare KV via the official API.

```
Browser → Cloudflare Worker (HTMLRewriter + KV) → Origin
                ↓ PENDING / HEALED
           Cloudflare KV  ←  Go Healer (Wayback + CF API)
                ↑ discovery POST
```

---

## Quick Start

### 1. Create the Cloudflare KV namespace

```bash
cd edge-worker
npm install

# Production namespace
npx wrangler kv:namespace create AUTOFIX_KV
# → copy the "id" value

# Preview / local-dev namespace
npx wrangler kv:namespace create AUTOFIX_KV --preview
# → copy the "preview_id" value
```

Paste both IDs into `edge-worker/wrangler.toml`:

```toml
[[kv_namespaces]]
binding = "AUTOFIX_KV"
id = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"          # from the first command
preview_id = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy" # from the --preview command
```

### 2. Deploy the Edge Worker

```bash
cd edge-worker
npx wrangler deploy
```

Optional: set the healer discovery URL so the Worker can push new links in real time:

```bash
npx wrangler secret put HEALER_DISCOVER_URL
# enter: https://your-healer-host/v1/discover
```

(or add it under `[vars]` in `wrangler.toml` for non-secret values).

### 3. Run the Healer

```bash
cd healer
cp .env.example .env
# edit .env with your Cloudflare credentials

go mod tidy
go run .
```

Required environment variables (see `.env.example`):

| Variable | Description |
|----------|-------------|
| `CF_ACCOUNT_ID` | Cloudflare Account ID (dashboard sidebar) |
| `CF_API_TOKEN` | API Token with **Account → Workers KV Storage → Edit** |
| `CF_KV_NAMESPACE_ID` | The same `id` you put in `wrangler.toml` |
| `HEALER_LISTEN` | HTTP listen address (default `:8080`) |

The healer exposes:

- `GET  /healthz` — liveness
- `POST /v1/discover` — body `{"urls":["https://…"]}` or `{"url":"https://…"}`

### 4. Client runtime (optional safety net)

```html
<script src="/path/to/autofix.js"></script>
```

---

## Project layout

```
autofix-engine/
├── edge-worker/
│   ├── src/index.ts      # HTMLRewriter + KV lookup + discovery
│   ├── wrangler.toml     # KV binding + vars
│   ├── package.json
│   └── tsconfig.json
├── healer/
│   ├── main.go          # Queue, Wayback, CF KV writer, HTTP API
│   ├── go.mod
│   └── .env.example
├── client/
│   └── autofix.js
├── .github/workflows/ci.yml
└── README.md
```

---

## Tech Stack

- **Edge**: TypeScript on Cloudflare Workers (`HTMLRewriter`, KV)
- **Healer**: Go 1.22 — concurrent queue workers, Wayback Machine, Cloudflare KV REST API
- **Client**: Vanilla JS tooltip / indicator
- **CI**: GitHub Actions (type-check Worker, build + vet Healer)

---

## Notes & next improvements

- Edge currently does one KV `get` per external link. For high-link-count pages consider a two-phase collect-then-`get` pattern or a Bloom filter to stay under tight TTFB budgets.
- Soft-404 detection (DOM title / body heuristics) can be added to the healer for SPA sites that return 200 on missing routes.
- For very high volume, replace the in-process channel with Cloudflare Queues or a durable queue (Redis / NATS).
