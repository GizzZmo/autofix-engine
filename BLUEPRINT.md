# AutoFix project blueprint + prompt guide

Two repos, one system:

| Repo | Role |
|------|------|
| [autofix-polyglot](https://github.com/GizzZmo/autofix-polyglot) | **Contracts first**: schemas, ADRs, types, examples, observability names |
| [autofix-engine](https://github.com/GizzZmo/autofix-engine) | **Runnable code**: Worker, healer, client, command centre, CI/deploy |

Rule: **change contracts in polyglot before code in engine.**

This file is the standing agent / contributor guide. Same copy lives in both repos.
Canonical contracts remain in [autofix-polyglot](https://github.com/GizzZmo/autofix-polyglot).

---

## 1. What it is

AutoFix is a **self-healing web layer**: intercept HTML at the edge, look up link health, rewrite dead externals to Wayback snapshots, discover unknown URLs off the request path, heal them in Go, write results back to KV.

It is **not** a CMS, not a general bug-fixer, and not a multi-tenant SaaS yet.

---

## 2. ISO-style layers (software, not OSI protocols)

| Layer | Job | Runtime | Status |
|-------|-----|---------|--------|
| **L7 Presentation** | Visual / DOM hints | `client/autofix.js` | Thin |
| **L6 Application** | `data-autofix-*`, `rel="nofollow archived"` | HTML attributes | Done on rewrite |
| **L5 Edge policy** | Stream rewrite, discover, circuit to healer | CF Worker TS | Done + HoL mitigations |
| **L4 Async boundary** | Queue + HTTP fallback | CF Queues | Done |
| **L3 Domain** | Soft-404, Wayback, workers, admin | Go healer | Done (7A+7B + optional Turso) |
| **L2 Shared state** | `LinkRecord` | Cloudflare KV | Done (edge contract) |
| **L1 External** | Origin HTML, archive.org, Turso | HTTP | Partial |

**Polyglot rule:** change language only when the runtime changes (isolate vs process vs browser).

**Paradigm rule:** streaming rewrite ≠ queue discovery ≠ concurrent heal ≠ fail-fast circuits.

---

## 3. What exists today (engine)

### Edge (`edge-worker/`)

- `HTMLRewriter` on `a[href]`
- Request-scope **L1 KV memo** + `cacheTtl` + **~200ms** `Promise.race` (no HoL stall)
- Rewrite only `HEALED` + `resolved_url`
- Discover via `ctx.waitUntil` → Queue, then HTTP fallback
- Isolate circuit breaker for healer POSTs (`traceparent` on outbound)
- Vendored types from polyglot (`urlKey`, `LinkRecord`, `DiscoveryMessage`)
- `wrangler.toml` may still have **placeholder KV ids**; CI can inject/create namespaces

### Healer (`healer/`)

- Discovery queue, pause/requeue, `POST /v1/discover`
- Soft-404 heuristics, Wayback (own circuit), KV REST write-back
- OpenTelemetry + Prometheus `/metrics`, `/healthz`, `GET /v1/admin/stats`
- Phase **7B** admin: `POST /v1/admin/actions` + audit ring (`ADMIN_TOKEN`)
- Optional **Turso** `heal_events` after KV writes (analytics trail; KV stays edge contract)
- Tests/benches on circuit + helpers

### Ops / client

- `command-centre/` observe + supervise UI
- `client/autofix.js` progressive enhancement
- `index.html` architecture/simulator page
- OTEL compose stack under `otel/`

### CI / deploy

- Path-filtered CI: Node 22 `npm ci` (lockfile expand-from-parts), Go test/cover/bench
- Deploy workflow: ensure KV+queues if CF secrets exist; skip cleanly if not
- Scripts: `ensure-cloudflare-resources.sh`, `check-wrangler-kv.sh`, `expand-lockfile.sh`

### Polyglot

- Schemas: link-record, discovery, discover req/res, admin stats/actions/audit
- Types: TS + Go skeletons
- ADRs 001–009
- Observability + versioning docs
- CI `npm run validate` on schemas + golden examples
- Roadmap: Phases **0–7B complete**; follow-ups listed

---

## 4. Normative contracts (do not invent new ones)

**KV key:** `base64.StdEncoding` of UTF-8 bytes of the absolute URL  
(TS: `urlKey()` via `TextEncoder` + `btoa` — **not** `encodeURIComponent` tricks)

**`LinkRecord` statuses**

| Status | Edge |
|--------|------|
| `PENDING` | leave href |
| `HEALED` | rewrite to `resolved_url` |
| `DEAD` | leave href |
| `HEALTHY` | leave href |

**Surfaces**

- Queue: `{ urls, discovered_at }`
- Healer: `POST /v1/discover` → `{ enqueued }`
- Admin: `GET /v1/admin/stats`, `POST /v1/admin/actions`, `GET /v1/admin/audit`
- Logs/metrics names: [polyglot OBSERVABILITY.md](https://github.com/GizzZmo/autofix-polyglot/blob/main/docs/OBSERVABILITY.md)

**Invariants**

1. Request path must not wait on heal work.
2. Idempotent hops (heal twice → same KV shape).
3. Circuits contain failure; never fail the whole HTML page.
4. Browser never talks to KV / Wayback / CF APIs.
5. Turso/D1 are **not** on the rewrite hot path.

---

## 5. Known gaps / debt

| Item | Why it matters |
|------|----------------|
| KV ids still placeholders in git | Deploy needs vars/secrets or `setup-cloudflare.sh` |
| Lockfile shipped as 16 b64 parts | Fragile CI; expand script must stay green |
| `httpserver.go` / `worker.go` stubs | Split leftover; live HTTP/main in `main.go`, workers in `ops.go` |
| JS client types | Polyglot still pending browser subset |
| No OpenAPI | Discover + admin are schema-only |
| Turso optional | Heal events empty until env is set |
| Dual-write never | Turso **after** KV, never instead of KV |

---

## 6. How to prompt future work

Copy as a system / first-message prefix:

```text
You are working on AutoFix, a self-healing web layer.

Repos:
- Contracts (source of truth): https://github.com/GizzZmo/autofix-polyglot
- Implementation: https://github.com/GizzZmo/autofix-engine

Split of duty:
- New fields, statuses, HTTP shapes, metric/log names → polyglot first
  (schemas/, docs/CONTRACTS.md, examples/, OBSERVABILITY.md), then engine.
- Runtime behaviour → engine only, aligned with existing contracts.

Layers:
L5 Cloudflare Worker (TS, HTMLRewriter, KV, Queues)
L4 Queue + POST /v1/discover HTTP fallback
L3 Go healer (soft-404, Wayback, circuit, admin, optional Turso events)
L2 Cloudflare KV LinkRecord (edge contract)
L7 optional client JS only for UX

Hard rules:
1. Do not block HTMLRewriter on unbounded KV/Turso/D1 awaits.
   Use L1 memo + cacheTtl + ~200ms budget; discovery on waitUntil.
2. KV key = std base64(UTF-8 bytes of absolute URL). Match Go types.KeyFor and TS urlKey().
3. Statuses: PENDING | HEALED | DEAD | HEALTHY. Only HEALED rewrites href.
4. Structured log keys from OBSERVABILITY.md (event, url, url_key, status, circuit, …).
5. Admin writes require Bearer ADMIN_TOKEN + audit; UI never holds CF tokens.
6. Optional Turso heal_events is append-only analytics. KV remains the rewrite contract.
7. Prefer small, reviewable commits. Do not invent placeholder hex KV ids.
8. Do not reintroduce sequential await-per-link waterfalls or dual-write of the same row to two primaries.

When changing code: read the existing file first; keep types/ in engine in sync with polyglot types/.
When changing contracts: add schema + example + CONTRACTS.md in the same change.
```

---

## 7. Ready-made task prompts

**A — Contract-first field**  
Add field X to LinkRecord. Update polyglot schema, examples (valid+invalid), types TS+Go, CONTRACTS.md, VERSIONING if needed. Then vendor into engine edge + healer. No behavior change except persist/read the field.

**B — Edge latency**  
Do not add awaits inside HTMLRewriter except budgeted L1 KV. Measure/document KV_LOOKUP_BUDGET_MS. Discovery stays waitUntil.

**C — Healer backend**  
New archive provider behind the same CheckArchive-style interface. Own circuit. No-snapshot is not a trip. Still write LinkRecord to KV; optionally Insert HealEvent.

**D — Ops**  
Command centre must only call /healthz, /metrics, /v1/admin/*. No KV credentials in the browser. Writes need actor+reason+audit_id.

**E — CI**  
Do not require committed KV ids. Deploy may inject CLOUDFLARE_KV_* or run ensure-cloudflare-resources.sh. Missing secrets = skip deploy, not red CI for template clones.

**F — Storage hybrid**  
KV = live link status for rewrite. Turso = heal_events history. D1 only if we add a CF-only projection. Never dual-write two sources of truth on the same request.

---

## 8. Suggested next product work (priority)

1. Stabilize healer package layout — one `main`, no stub/dupe files; `go test ./...` clean.
2. Commit or inject real KV ids so deploy is actually on.
3. OpenAPI for `/v1/discover` + `/v1/admin/*` in polyglot.
4. Browser type subset for `data-autofix-*`.
5. Codegen or submodule so engine types are not hand-copied.
6. Heal-events queries in command centre (read Turso from healer API, not from the browser).
7. Second archive backend (optional) behind the same healer interface.

---

## 9. Mental model for agents

```text
HTML request
  → Worker fetch origin
  → HTMLRewriter a[href]
       → urlKey → L1 Map → KV get (≤200ms)
            HEALED → rewrite
            miss/timeout → discover set
  → return stream
  → waitUntil: PENDING KV + Queue/HTTP

Queue/HTTP
  → Healer POST /v1/discover
  → workers: CheckLink → Wayback?
       → KV Put LinkRecord          // edge contract
       → Turso Insert heal_events   // optional trail
```

Use §6 as the standing prompt; use A–F as scoped tickets. Contracts live in polyglot; behavior lives in engine.
