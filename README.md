# 🔧 AutoFix: The Self-Healing Web Layer

[![CI](https://github.com/GizzZmo/autofix-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/GizzZmo/autofix-engine/actions/workflows/ci.yml)
[![Deploy Edge](https://github.com/GizzZmo/autofix-engine/actions/workflows/deploy-edge.yml/badge.svg)](https://github.com/GizzZmo/autofix-engine/actions/workflows/deploy-edge.yml)
[![Coverage](https://img.shields.io/badge/coverage-17.6%25-yellow)](https://github.com/GizzZmo/autofix-engine/actions/workflows/ci.yml)
[![Codecov](https://codecov.io/gh/GizzZmo/autofix-engine/branch/main/graph/badge.svg)](https://codecov.io/gh/GizzZmo/autofix-engine)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.6-3178C6?logo=typescript&logoColor=white)](./edge-worker)
[![Go](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white)](./healer)
[![Cloudflare Workers](https://img.shields.io/badge/Cloudflare-Workers-F38020?logo=cloudflare&logoColor=white)](https://workers.cloudflare.com/)
[![License](https://img.shields.io/badge/license-Use%20freely-lightgrey)](#license)

[Architecture & Edge Simulator](https://aistudio.google.com/apps/69748ab5-5005-41fc-b817-64865f3368fe?showPreview=true&showAssistant=true&fullscreenApplet=true)

AutoFix eliminates 404s and broken external links without requiring database migrations.

**Blueprint → [BLUEPRINT.md](./BLUEPRINT.md)** · **Deploy guide → [DEPLOY.md](./DEPLOY.md)** · **Makefile** · **docker-compose.yml**  
**Contracts → [autofix-polyglot](https://github.com/GizzZmo/autofix-polyglot)** (schemas, observability, versioning, command centre)  
**Ops UI → [command-centre/](./command-centre/)** (observe + supervise against `/metrics`, `/healthz`, `/v1/admin/*`)

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

## Observability (OpenTelemetry)

Normative field/metric names: [polyglot OBSERVABILITY.md](https://github.com/GizzZmo/autofix-polyglot/blob/main/docs/OBSERVABILITY.md).

| Layer | What |
|-------|------|
| **Healer** | `healer/telemetry.go` — Prometheus **`GET /metrics`**, optional **OTLP** traces+metrics |
| **Edge** | `edge-worker/src/telemetry.ts` — structured JSON logs + W3C **`traceparent`** on healer calls |
| **Command centre** | `command-centre/` — human stats + supervise UI |

### Healer metrics (Prometheus)

- `autofix_heal_duration_seconds` / `autofix_check_duration_seconds` / `autofix_wayback_duration_seconds`
- `autofix_circuit_state{name="healer_wayback"}` / `autofix_circuit_trips_total`
- `autofix_queue_depth` / `autofix_discover_requests_total` / `autofix_links_total`

### Enable OTLP export

```bash
# healer/.env
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

`service.name` = `autofix-healer`. Span names: `autofix.heal`, `autofix.check`, `autofix.wayback`, `autofix.discover` — wired in `main.go` and edge `index.ts`.

### Command centre

```bash
# terminal 1
cd healer && go run .
# terminal 2
cd command-centre && python3 -m http.server 8090
# open http://localhost:8090 — set Healer URL to http://localhost:8080
```

Contracts: [COMMAND_CENTRE.md](https://github.com/GizzZmo/autofix-polyglot/blob/main/docs/COMMAND_CENTRE.md).

---

## Stats

| Metric | Value |
|--------|------:|
| Core source | ~26 KB+ |
| Edge Worker | TypeScript |
| Go Healer | main + circuit breaker + **telemetry** |
| Stack | Cloudflare Workers · KV · Queues · Go · Node 22 · OpenTelemetry |
| Resilience | Queues + HTTP fallback · exponential backoff · dual circuit breakers |

| Component | Role |
|-----------|------|
| `edge-worker/` | HTML stream rewrite, KV lookup, queue producer, edge circuit breaker |
| `healer/` | Discovery, soft-404, Wayback, KV write, OTel metrics/traces |
| `command-centre/` | Ops stats + supervise UI |
| `client/` | Optional browser runtime |
| `scripts/` | KV/queue setup, lockfile expand, KV id guard |

---

## Quick start

```bash
git clone https://github.com/GizzZmo/autofix-engine.git
cd autofix-engine
wrangler login

./scripts/setup-cloudflare.sh   # create KV + queues; paste ids into wrangler.toml
make edge-deploy

cp healer/.env.example healer/.env   # fill CF_* ; optional OTEL_EXPORTER_OTLP_ENDPOINT
make healer-run                      # or: make compose-up
# scrape metrics: curl localhost:8080/metrics
# ops UI: cd command-centre && python3 -m http.server 8090
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
| `make healer-test` | Unit tests + coverage |
| `make compose-up` | Docker Compose healer |
| `make ci` | Typecheck + build/vet/test |

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
├── BLUEPRINT.md           # agent / contributor guide
├── edge-worker/          # Worker + telemetry.ts
├── healer/               # Go healer + telemetry.go + circuit breaker
├── command-centre/       # observe + supervise UI
├── client/autofix.js
├── scripts/
├── docker-compose.yml
├── Makefile
├── DEPLOY.md
└── .github/workflows/
```

---

## CI / CD

| Workflow | Trigger | Checks |
|----------|---------|--------|
| **CI** | push / PR to `main` | Edge `tsc` · Healer `go build` / `vet` / `test` |
| **Deploy Edge** | push to `main` (edge paths) | Wrangler deploy |

**Secrets for deploy:** `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`

---

## License

Use freely; no warranty. See DEPLOY.md for production cautions.
