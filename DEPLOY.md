# AutoFix Deployment Guide

End-to-end instructions to deploy the Edge Worker (Cloudflare) and the Go Healer.

Also see: `Makefile`, `scripts/setup-cloudflare.sh`, `docker-compose.yml`.

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
node -v    # 18+
go version # 1.22+
npm install -g wrangler   # or use npx
wrangler login
```

```bash
git clone https://github.com/GizzZmo/autofix-engine.git
cd autofix-engine
```

---

## 1. Bootstrap Cloudflare resources

### Automated

```bash
chmod +x scripts/setup-cloudflare.sh
./scripts/setup-cloudflare.sh
```

### Manual

```bash
cd edge-worker && npm install

npx wrangler kv:namespace create AUTOFIX_KV
npx wrangler kv:namespace create AUTOFIX_KV --preview
npx wrangler queues create autofix-discovery
npx wrangler queues create autofix-discovery-dlq   # optional
```

Paste KV `id` / `preview_id` into `edge-worker/wrangler.toml`.

Uncomment `dead_letter_queue` only after creating the DLQ.

### API token for the healer

1. [API Tokens](https://dash.cloudflare.com/profile/api-tokens) → Create Token
2. Permission: **Account → Workers KV Storage → Edit**
3. Copy token + Account ID (dashboard sidebar)

---

## 2. Deploy the Edge Worker

```bash
make edge-deploy
# or: cd edge-worker && npx wrangler deploy
```

### CI deploy (optional)

Repo → Settings → Secrets:

| Secret | Value |
|--------|--------|
| `CLOUDFLARE_API_TOKEN` | Token with Workers deploy rights |
| `CLOUDFLARE_ACCOUNT_ID` | Account ID |

Workflow: `.github/workflows/deploy-edge.yml` (runs on `main` pushes under `edge-worker/`, or **workflow_dispatch**).

Set `HEALER_DISCOVER_URL` once manually (`wrangler secret put`) — not injected by CI by default.

### Custom routes

Dashboard → Worker → Triggers, or in `wrangler.toml`:

```toml
[[routes]]
pattern = "yoursite.com/*"
zone_name = "yoursite.com"
```

---

## 3. Deploy the Healer

### Configure

```bash
cd healer
cp .env.example .env
# CF_ACCOUNT_ID, CF_API_TOKEN, CF_KV_NAMESPACE_ID, HEALER_LISTEN
```

### Local + tunnel

```bash
make healer-run
cloudflared tunnel --url http://localhost:8080
```

### Docker Compose

```bash
# ensure healer/.env exists
make compose-up
curl -s http://127.0.0.1:8080/healthz
```

### Docker only

```bash
make healer-docker
docker run -d --name autofix-healer --env-file healer/.env -p 8080:8080 --restart unless-stopped autofix-healer
```

### systemd (VPS)

```bash
cd healer && go build -o autofix-healer .
sudo mv autofix-healer /usr/local/bin/
# EnvironmentFile=/etc/autofix-healer.env with CF_* vars
# ExecStart=/usr/local/bin/autofix-healer
```

---

## 4. Connect Worker → Healer

```bash
cd edge-worker
npx wrangler secret put HEALER_DISCOVER_URL
# https://<healer-host>/v1/discover
```

---

## 5. Local development

```bash
# Terminal-friendly one-shot (healer + wrangler)
chmod +x scripts/dev.sh
./scripts/dev.sh
```

Or separately:

```bash
cp edge-worker/.dev.vars.example edge-worker/.dev.vars
make healer-run          # terminal 1
make edge-dev            # terminal 2
```

Note: `wrangler dev --remote` does **not** support Queues.

---

## 6. Verification

```bash
curl -s https://<healer>/healthz
curl -s -X POST https://<healer>/v1/discover \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://httpstat.us/404"]}'
make edge-tail
```

Load a page through the Worker with external links → Queue / DISCOVERED logs → healer HEALED/DEAD → later requests rewrite links.

---

## 7. Troubleshooting

| Symptom | Likely cause |
|---------|----------------|
| Deploy fails on KV id | Placeholder still in `wrangler.toml` |
| Deploy fails on DLQ | Create queue or comment out `dead_letter_queue` |
| No healer traffic | Missing/wrong `HEALER_DISCOVER_URL` |
| `CircuitOpen` | Healer down; wait or fix healer |
| KV not updating | Healer `CF_*` / wrong namespace id |
| CI deploy fails | Missing GitHub secrets or KV ids not in toml |

---

## 8. Security

- Use `wrangler secret` for `HEALER_DISCOVER_URL`
- Restrict healer ingress (tunnel, firewall, Access)
- KV-only API token for the healer
- Never commit `.env` / `.dev.vars`

---

## Quick reference

```bash
./scripts/setup-cloudflare.sh
make edge-deploy
make compose-up   # or make healer-run
npx wrangler secret put HEALER_DISCOVER_URL
make edge-tail
```
