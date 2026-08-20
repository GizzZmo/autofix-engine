# AutoFix Deployment Guide

End-to-end instructions to deploy the Edge Worker (Cloudflare) and the Go Healer.

---

## Architecture at deploy time

```
Internet → Cloudflare (Worker + KV + Queue)
                │
                │  HEALER_DISCOVER_URL (secret)
                ▼
         Go Healer (VPS / Docker / tunnel)
                │
                ├─ Wayback Machine (public API)
                └─ Cloudflare KV API (writes HEALED / DEAD)
```

You need:

1. A **Cloudflare account** (Workers + KV + Queues)
2. A host for the **healer** (local + tunnel is fine for testing)
3. A **CF API token** with KV edit permission (for the healer)

---

## 0. Prerequisites

```bash
# Node 18+
node -v

# Go 1.22+ (for local healer builds)
go version

# Wrangler
npm install -g wrangler
# or use npx wrangler …

wrangler login
```

Clone the repo:

```bash
git clone https://github.com/GizzZmo/autofix-engine.git
cd autofix-engine
```

---

## 1. Cloudflare resources

### 1.1 KV namespaces

```bash
cd edge-worker
npm install

npx wrangler kv:namespace create AUTOFIX_KV
# Output includes: id = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

npx wrangler kv:namespace create AUTOFIX_KV --preview
# Output includes: preview_id = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
```

Edit `edge-worker/wrangler.toml`:

```toml
[[kv_namespaces]]
binding = "AUTOFIX_KV"
id = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
preview_id = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
```

### 1.2 Queues

```bash
npx wrangler queues create autofix-discovery

# Optional dead-letter queue
npx wrangler queues create autofix-discovery-dlq
```

If you created the DLQ, uncomment in `wrangler.toml`:

```toml
[[queues.consumers]]
queue = "autofix-discovery"
# …
dead_letter_queue = "autofix-discovery-dlq"
```

### 1.3 API token for the healer

1. Open [Cloudflare API Tokens](https://dash.cloudflare.com/profile/api-tokens)
2. **Create Token** → custom token
3. Permissions: **Account → Workers KV Storage → Edit**
4. Account Resources: include your account
5. Copy the token (shown once)

Also note your **Account ID** (dashboard sidebar / Workers overview).

---

## 2. Deploy the Edge Worker

```bash
cd edge-worker
npx wrangler deploy
```

Note the workers.dev URL (or attach a custom route in the dashboard):

```text
https://autofix-proxy.<your-subdomain>.workers.dev
```

### Routes (production)

In the Cloudflare dashboard → Workers → `autofix-proxy` → Triggers:

- Add route: `yoursite.com/*` (or a path prefix)
- Ensure the zone is on Cloudflare DNS/proxy

Or in `wrangler.toml`:

```toml
[[routes]]
pattern = "yoursite.com/*"
zone_name = "yoursite.com"
```

---

## 3. Deploy the Healer

### 3.1 Configure environment

```bash
cd healer
cp .env.example .env
```

```env
CF_ACCOUNT_ID=your_account_id
CF_API_TOKEN=your_api_token
CF_KV_NAMESPACE_ID=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx   # same as wrangler.toml id
HEALER_LISTEN=:8080
```

### 3.2 Option A — local + tunnel (dev / demo)

```bash
cd healer
go mod tidy
go run .
# listens on :8080
```

Expose publicly:

```bash
# Cloudflare Tunnel (no account linking required for quick tunnel)
cloudflared tunnel --url http://localhost:8080
# → https://random-name.trycloudflare.com

# or ngrok
ngrok http 8080
```

### 3.3 Option B — Docker

```bash
cd healer
docker build -t autofix-healer .
docker run -d --name autofix-healer \
  --env-file .env \
  -p 8080:8080 \
  --restart unless-stopped \
  autofix-healer
```

Put a reverse proxy (Caddy, nginx, Traefik) or tunnel in front for TLS.

### 3.4 Option C — any Linux VPS

```bash
cd healer
go build -o autofix-healer .
sudo mv autofix-healer /usr/local/bin/

# systemd unit example
sudo tee /etc/systemd/system/autofix-healer.service << 'EOF'
[Unit]
Description=AutoFix Healer
After=network.target

[Service]
EnvironmentFile=/etc/autofix-healer.env
ExecStart=/usr/local/bin/autofix-healer
Restart=on-failure
User=www-data

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable --now autofix-healer
```

---

## 4. Connect Worker → Healer

The Worker must know the healer’s public URL:

```bash
cd edge-worker
npx wrangler secret put HEALER_DISCOVER_URL
```

Paste (include path):

```text
https://<your-healer-host>/v1/discover
```

Secrets apply without a full redeploy.

---

## 5. Client runtime (optional)

Serve `client/autofix.js` from your origin or CDN and include:

```html
<script src="/autofix.js" defer></script>
```

Shows tooltips on links already rewritten by the Edge (`autofix-healed`).

---

## 6. Verification checklist

```bash
# Healer alive + circuit state
curl -s https://<healer-host>/healthz
# {"status":"ok","wayback_circuit":"closed"}

# Manual discovery
curl -s -X POST https://<healer-host>/v1/discover \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://httpstat.us/404"]}'
# {"enqueued":1}

# Worker logs
cd edge-worker && npx wrangler tail
```

Then load a page **through the Worker** that contains external links. Expect:

1. Worker logs: `Queued N link(s)` or `DISCOVERED`
2. Healer logs: `verifying …` / `HEALED` / `dead`
3. Later requests: links with `class="autofix-healed"` and archive `href`

Inspect KV (dashboard → Workers → KV → `AUTOFIX_KV`) for `PENDING` / `HEALED` records.

---

## 7. Local development

```bash
cd edge-worker
npx wrangler dev
# Uses preview_id KV; queues simulated by Miniflare
# Note: wrangler dev --remote does not support Queues
```

Point `HEALER_DISCOVER_URL` at `http://127.0.0.1:8080/v1/discover` via `.dev.vars`:

```bash
# edge-worker/.dev.vars
HEALER_DISCOVER_URL=http://127.0.0.1:8080/v1/discover
```

Run healer locally in another terminal.

---

## 8. Troubleshooting

| Symptom | Likely cause |
|---------|----------------|
| Worker deploy fails on KV id | Placeholder `YOUR_KV_NAMESPACE_ID` not replaced |
| Deploy fails on DLQ | Create queue or comment out `dead_letter_queue` |
| No healer traffic | Missing/wrong `HEALER_DISCOVER_URL` secret |
| `CircuitOpen` in logs | Healer down or slow; wait 30s or fix healer |
| KV not updating | Healer missing `CF_*` env or wrong namespace id |
| Links never rewrite | Route not hitting Worker, or not HTML `content-type` |
| Queue messages stuck | Check consumer in dashboard; `wrangler tail` |

---

## 9. Security notes

- Prefer **secrets** (`wrangler secret put`) over plain `[vars]` for URLs that should not be public in the Worker script metadata if sensitive.
- Restrict the healer (firewall / Cloudflare Access) so only Cloudflare egress or your tunnel can reach `/v1/discover`.
- Rotate the CF API token if leaked; least privilege = KV Edit only.
- Do not commit `.env` or `.dev.vars` (see `.gitignore`).

---

## Quick reference

```bash
# Resources
npx wrangler kv:namespace create AUTOFIX_KV
npx wrangler queues create autofix-discovery

# Edge
cd edge-worker && npx wrangler deploy
npx wrangler secret put HEALER_DISCOVER_URL
npx wrangler tail

# Healer
cd healer && cp .env.example .env && go run .
# or: docker run --env-file .env -p 8080:8080 autofix-healer
```
