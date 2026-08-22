# 🔧 AutoFix: The Self-Healing Web Layer

[Architecture & Edge Simulator](https://aistudio.google.com/apps/69748ab5-5005-41fc-b817-64865f3368fe?showPreview=true&showAssistant=true&fullscreenApplet=true)

AutoFix eliminates 404s and broken external links without requiring database migrations.

**Deploy guide → [DEPLOY.md](./DEPLOY.md)** · **Makefile** · **docker-compose.yml**

### How it works
1. **Intercept** — Edge Worker parses HTML via `HTMLRewriter`.
2. **Lookup** — Cloudflare KV for link health.
3. **Resolve** — Dead links → Wayback archive (`rel="nofollow archived"`).
4. **Discover** — `PENDING` in KV + **Queues** (HTTP fallback).
5. **Heal** — Go service: soft-404, Wayback, KV write-back, circuit breakers.

```
Browser → Worker (HTMLRewriter + KV + Queue + circuit breaker)
                              │
                              ▼ POST /v1/discover
                     Go Healer → Wayback → KV
```

---

## Quick start

```bash
git clone https://github.com/GizzZmo/autofix-engine.git
cd autofix-engine
wrangler login

./scripts/setup-cloudflare.sh   # create KV + queues; paste ids into wrangler.toml
make edge-deploy

cp healer/.env.example healer/.env   # fill CF_*
make healer-run                      # or: make compose-up
# expose healer, then:
cd edge-worker && npx wrangler secret put HEALER_DISCOVER_URL
```

Local full stack: `./scripts/dev.sh` (healer + `wrangler dev`).

---

## Make targets

| Target | Action |
|--------|--------|
| `make edge-setup` | Create KV + queues |
| `make edge-dev` | `wrangler dev` |
| `make edge-deploy` | Deploy Worker |
| `make edge-tail` | Live logs |
| `make healer-run` | Run Go healer |
| `make compose-up` | Docker Compose healer |
| `make ci` | Typecheck + build/vet |

---

## Resilience

| Layer | Mechanism |
|-------|-----------|
| Discovery | Queues + HTTP fallback |
| Queue consumer | ack / retry + exponential backoff |
| Edge → Healer | Circuit breaker (5 fails / 30s) |
| Healer → Wayback | Circuit breaker (5 fails / 60s) |
| DLQ | Optional after max_retries |

---

## Layout

```
autofix-engine/
├── edge-worker/          # Cloudflare Worker
├── healer/               # Go healer + circuit breaker
├── client/autofix.js
├── scripts/              # setup-cloudflare.sh, dev.sh
├── docker-compose.yml
├── Makefile
├── DEPLOY.md
└── .github/workflows/    # ci.yml, deploy-edge.yml
```

---

## CI / CD

- **CI** — tsc on Worker; `go build` / `go vet` / `go test` on healer
- **Deploy Edge** — `deploy-edge.yml` needs secrets `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`

---

## License

Use freely; no warranty. See DEPLOY.md for production cautions.
