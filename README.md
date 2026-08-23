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

## Stats

| Metric | Value |
|--------|------:|
| Core source | ~26 KB (~740 LOC) |
| Edge Worker | TypeScript · ~9.4 KB (~270 LOC) |
| Go Healer | main + circuit breaker · ~14 KB (~420 LOC) |
| Healer tests | ~1 KB (~30 LOC) |
| **Go coverage** | **17.6%** statements (circuit breaker ~90%+) |
| Client runtime | `autofix.js` · ~1.7 KB (~50 LOC) |
| CI / deploy workflows | 2 · path-filtered jobs |
| Stack | Cloudflare Workers · KV · Queues · Go · Node 22 |
| Resilience | Queues + HTTP fallback · exponential backoff · dual circuit breakers |

| Component | Role |
|-----------|------|
| `edge-worker/` | HTML stream rewrite, KV lookup, queue producer, edge circuit breaker |
| `healer/` | Discovery consumer, soft-404, Wayback, KV write, Wayback circuit breaker |
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
| `make healer-test` | Unit tests + coverage |
| `make healer-bench` | CPU benchmarks (`-count=3`) → `healer/bench.txt` |
| `make healer-bench-cpu` | CPU profile → `healer/cpu.prof` |
| `make healer-bench-mem` | Memory profile → `healer/mem.prof` |
| `make healer-benchstat OLD=a.txt NEW=b.txt` | Compare with [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) |
| `make compose-up` | Docker Compose healer |
| `make ci` | Typecheck + build/vet/test/short-bench |

### Benchmarks (healer)

```bash
make healer-bench
# or:
cd healer && go test -run=^$ -bench=. -benchmem -count=3 ./...

# Profiles
make healer-bench-cpu   # go tool pprof healer/cpu.prof
make healer-bench-mem   # go tool pprof healer/mem.prof

# Before/after comparison
make healer-bench && cp healer/bench.txt old.txt
# ... change code ...
make healer-bench && cp healer/bench.txt new.txt
make healer-benchstat OLD=old.txt NEW=new.txt
```

Hot paths covered: circuit breaker (success / fail / open / parallel), soft-404 heuristics, discovery queue enqueue, KV `keyFor`.

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
├── healer/               # Go healer + circuit breaker + benches
├── client/autofix.js
├── scripts/              # setup-cloudflare.sh, expand-lockfile, dev.sh
├── docker-compose.yml
├── Makefile
├── DEPLOY.md
└── .github/workflows/    # ci.yml, deploy-edge.yml
```

---

## CI / CD

| Workflow | Trigger | Checks |
|----------|---------|--------|
| [**CI**](https://github.com/GizzZmo/autofix-engine/actions/workflows/ci.yml) | push / PR to `main` | Path filters → Edge (`npm ci` + `tsc`) · Healer (`go build` / `vet` / `test` + **coverage** + **short benches**) · ci-gate |
| [**Deploy Edge**](https://github.com/GizzZmo/autofix-engine/actions/workflows/deploy-edge.yml) | push to `main` (edge paths) or manual | Expand lockfile → `npm ci` → KV id guard → Wrangler deploy |

**Coverage:** `go test -coverprofile=coverage.out` · artifact `healer-coverage` · optional [Codecov](https://codecov.io/gh/GizzZmo/autofix-engine) when `CODECOV_TOKEN` is set.

**Benchmarks:** CI runs `-benchtime=100ms` and uploads artifact `healer-bench` (`bench.txt`). Local: `make healer-bench`.

**Secrets for deploy:** `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`

**Caching:** npm via `package-lock.json` · Go via `healer/go.sum` (no `node_modules` primary cache)

---

## License

Use freely; no warranty. See DEPLOY.md for production cautions.
