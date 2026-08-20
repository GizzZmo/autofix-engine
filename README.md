# 🔧 AutoFix: The Self-Healing Web Layer

AutoFix eliminates 404s and broken external links without requiring database migrations.

### How it works
1. **Intercept** — The Edge Worker parses HTML as it leaves your origin.
2. **Lookup** — It checks a global Cloudflare KV registry for the health status of every external link.
3. **Resolve** — Dead links are hot-swapped with a Wayback Machine archive (and marked `rel="nofollow archived"`).
4. **Discover** — Unknown links are written as `PENDING` into KV and (optionally) POSTed to the Healer.
5. **Heal** — A background Go service drains a discovery queue, verifies links (including soft-404 heuristics), queries the Internet Archive, and writes the healed record back into Cloudflare KV via the official API.

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

### 3. Run the Healer

**Locally:**

```bash
cd healer
cp .env.example .env
# edit .env with your Cloudflare credentials

go mod tidy
go run .
```

**Docker:**

```bash
cd healer
docker build -t autofix-healer .
docker run --env-file .env -p 8080:8080 autofix-healer
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
│   ├── main.go          # Queue, soft-404, Wayback, CF KV writer, HTTP API
│   ├── go.mod
│   ├── Dockerfile
│   └── .env.example
├── client/
│   └── autofix.js
├── .github/workflows/ci.yml
├── .gitignore
└── README.md
```

---

## Tech Stack

- **Edge**: TypeScript on Cloudflare Workers (`HTMLRewriter`, KV)
- **Healer**: Go 1.22 — concurrent queue workers, soft-404 detection, Wayback Machine, Cloudflare KV REST API
- **Client**: Vanilla JS tooltip / indicator
- **CI**: GitHub Actions (type-check Worker, build + vet Healer)

---

## Healer verification pipeline

1. **HEAD** request with browser-like User-Agent  
2. If non-2xx → mark dead (`http_4xx` / `http_5xx`)  
3. Else **Range GET** (first 8 KB) and inspect for soft-404 signals:  
   - `<title>` matching 404 / “not found” / “page missing”  
   - Body text matching common 404 phrases  
4. If dead → query Wayback Machine → write `HEALED` or `DEAD` into KV

---

## Notes & next improvements

- Edge currently does one KV `get` per external link. For high-link-count pages consider a two-phase collect-then-batch pattern or an in-memory Bloom filter.
- For very high volume, replace the in-process channel with Cloudflare Queues or a durable queue (Redis / NATS).
- Soft-404 heuristics can be extended with site-specific patterns.
