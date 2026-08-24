# AutoFix Deployment Guide

End-to-end instructions to deploy the Edge Worker (Cloudflare) and the Go Healer.

Also see: `Makefile`, `scripts/setup-cloudflare.sh`, `scripts/ensure-cloudflare-resources.sh`, `docker-compose.yml`.

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
3. A **CF API token** with Workers deploy + KV edit permission

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

### Local (interactive)

```bash
chmod +x scripts/setup-cloudflare.sh
./scripts/setup-cloudflare.sh
```

Creates KV namespaces + queues and patches `edge-worker/wrangler.toml`.

### CI (automatic)

On deploy, `scripts/ensure-cloudflare-resources.sh` runs with your API token:

1. Uses `vars.CLOUDFLARE_KV_*` if set
2. Otherwise **creates** (or resolves existing) `AUTOFIX_KV` production + preview namespaces
3. Ensures queues `autofix-discovery` and `autofix-discovery-dlq`
4. Patches `wrangler.toml` for that job only (ids are not committed)

### Manual

```bash
cd edge-worker && npm install

npx wrangler kv namespace create AUTOFIX_KV
npx wrangler kv namespace create AUTOFIX_KV --preview
npx wrangler queues create autofix-discovery
npx wrangler queues create autofix-discovery-dlq   # optional
```

Paste KV `id` / `preview_id` into `edge-worker/wrangler.toml`, or export:

```bash
export CLOUDFLARE_KV_NAMESPACE_ID=...
export CLOUDFLARE_KV_PREVIEW_ID=...
./scripts/check-wrangler-kv.sh --write
```

Validate:

```bash
./scripts/check-wrangler-kv.sh edge-worker/wrangler.toml
```

Uncomment `dead_letter_queue` only after the DLQ exists.

### API token

1. [API Tokens](https://dash.cloudflare.com/profile/api-tokens) → Create Token
2. Permissions:
   - **Account → Workers Scripts → Edit**
   - **Account → Workers KV Storage → Edit**
   - **Account → Queues → Edit** (if available on your plan)
3. Copy token + Account ID (dashboard sidebar)

---

## 2. Deploy the Edge Worker

```bash
make edge-deploy
# or: cd edge-worker && npx wrangler deploy
```

### CI deploy

Workflow: `.github/workflows/deploy-edge.yml` (path-filtered on `main`, or **workflow_dispatch**).

**Required secrets** (Settings → Secrets and variables → Actions):

| Name | Value |
|------|--------|
| `CLOUDFLARE_API_TOKEN` | Token with Workers + KV (+ Queues) Edit |
| `CLOUDFLARE_ACCOUNT_ID` | Account ID |

**Optional variables** (skip auto-create when you already know ids):

| Name | Value |
|------|--------|
| `CLOUDFLARE_KV_NAMESPACE_ID` | 32-char hex production KV id |
| `CLOUDFLARE_KV_PREVIEW_ID` | 32-char hex preview KV id |

If secrets are missing, the job **skips deploy** with a warning (does not fail the push).

Set `HEALER_DISCOVER_URL` once manually:

```bash
cd edge-worker && npx wrangler secret put HEALER_DISCOVER_URL
```

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

Use the **same** production KV namespace id as the Worker (`id` in wrangler / CI logs).

### Local + tunnel

```bash
make healer-run
cloudflared tunnel --url http://localhost:8080
```

### Docker Compose

```bash
make compose-up
curl -s http://127.0.0.1:8080/healthz
```

### Docker only

```bash
make healer-docker
docker run -d --name autofix-healer --env-file healer/.env -p 8080:8080 --restart unless-stopped autofix-healer
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

---

## 7. Troubleshooting

| Symptom | Likely cause |
|---------|----------------|
| Deploy skips | Missing `CLOUDFLARE_API_TOKEN` / `ACCOUNT_ID` |
| Ensure KV fails | Token lacks **Workers KV Storage → Edit** |
| Queue create warns | Plan/permissions; create queue in dashboard |
| No healer traffic | Missing/wrong `HEALER_DISCOVER_URL` |
| `CircuitOpen` | Healer down |
| KV not updating | Healer `CF_KV_NAMESPACE_ID` ≠ Worker prod id |

---

## 8. Security

- Use `wrangler secret` for `HEALER_DISCOVER_URL`
- Prefer CI auto-create or repository Variables over committing KV ids
- Restrict healer ingress (tunnel, firewall, Access)
- Never commit `.env` / `.dev.vars`

---

## Quick reference

```bash
./scripts/setup-cloudflare.sh   # local
# or: only CLOUDFLARE_API_TOKEN + ACCOUNT_ID on GitHub → auto KV on deploy
make edge-deploy
make compose-up
npx wrangler secret put HEALER_DISCOVER_URL
make edge-tail
```
