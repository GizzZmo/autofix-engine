// The Edge Layer: High-performance HTML streaming rewriter
// + Cloudflare Queue producer/consumer + circuit breaker for healer
//
// Contracts: https://github.com/GizzZmo/autofix-polyglot
// Types: ./types (synced from polyglot types/typescript)

import type { LinkRecord, DiscoveryMessage } from "./types";
import { urlKey } from "./types";
import { logEvent, newTraceparent } from "./telemetry";

export interface Env {
  AUTOFIX_KV: KVNamespace;
  DISCOVERY_QUEUE?: Queue<DiscoveryMessage>;
  HEALER_DISCOVER_URL?: string;
  ENVIRONMENT?: string;
}

/** HTMLRewriter Element has no classList — append a class token safely. */
function addClass(element: Element, token: string): void {
  const existing = element.getAttribute("class") || "";
  const parts = existing.split(/\s+/).filter(Boolean);
  if (!parts.includes(token)) {
    parts.push(token);
    element.setAttribute("class", parts.join(" "));
  }
}

// ---------------------------------------------------------------------------
// Circuit breaker (in-isolate) for healer HTTP calls
// ---------------------------------------------------------------------------

type BreakerState = "closed" | "open" | "half_open";

class CircuitBreaker {
  private state: BreakerState = "closed";
  private failures = 0;
  private openedAt = 0;
  private halfOpenInFlight = 0;

  constructor(
    private readonly failureThreshold = 5,
    private readonly openMs = 30_000,
    private readonly halfOpenMax = 1
  ) {}

  get status(): BreakerState {
    if (this.state === "open" && Date.now() - this.openedAt >= this.openMs) {
      return "half_open";
    }
    return this.state;
  }

  async exec<T>(fn: () => Promise<T>): Promise<T> {
    let s = this.state;
    if (s === "open") {
      if (Date.now() - this.openedAt < this.openMs) {
        throw new Error("CircuitOpen");
      }
      this.state = "half_open";
      this.halfOpenInFlight = 0;
      s = "half_open";
    }

    if (s === "half_open" && this.halfOpenInFlight >= this.halfOpenMax) {
      throw new Error("CircuitOpen");
    }
    if (s === "half_open") this.halfOpenInFlight++;

    try {
      const result = await fn();
      this.onSuccess();
      return result;
    } catch (err) {
      this.onFailure();
      throw err;
    }
  }

  private onSuccess() {
    const wasOpen = this.state === "open" || this.state === "half_open";
    this.failures = 0;
    this.state = "closed";
    this.halfOpenInFlight = 0;
    if (wasOpen) {
      logEvent({
        event: "circuit.close",
        circuit: "closed",
        circuit_name: "edge_healer",
      });
    }
  }

  private onFailure() {
    this.failures++;
    if (this.state === "half_open" || this.failures >= this.failureThreshold) {
      this.state = "open";
      this.openedAt = Date.now();
      this.halfOpenInFlight = 0;
      logEvent({
        event: "circuit.open",
        level: "warn",
        circuit: "open",
        circuit_name: "edge_healer",
        count: this.failures,
        duration_ms: this.openMs,
      });
    }
  }
}

/** Shared breaker for all healer POSTs in this isolate. */
const healerBreaker = new CircuitBreaker(5, 30_000, 1);

const worker = {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    let response: Response;
    try {
      response = await fetch(request);
    } catch {
      return new Response("Bad Gateway", { status: 502 });
    }

    const contentType = response.headers.get("content-type") || "";
    if (!contentType.includes("text/html")) {
      return response;
    }

    if (!env.AUTOFIX_KV) {
      return response;
    }

    const discovered = new Set<string>();

    try {
      const transformed = new HTMLRewriter()
        .on("a[href]", {
          async element(element) {
            try {
              const href = element.getAttribute("href");
              if (
                !href ||
                href.startsWith("/") ||
                href.startsWith("#") ||
                href.startsWith("mailto:") ||
                href.startsWith("javascript:")
              ) {
                return;
              }

              let absolute: string;
              try {
                absolute = new URL(href, request.url).href;
              } catch {
                return;
              }
              if (!absolute.startsWith("http://") && !absolute.startsWith("https://")) {
                return;
              }

              const key = urlKey(absolute);
              const cached = (await env.AUTOFIX_KV.get(key, {
                type: "json",
              })) as LinkRecord | null;

              if (cached?.status === "HEALED" && cached.resolved_url) {
                element.setAttribute("href", cached.resolved_url);
                element.setAttribute("data-autofix-original", absolute);
                element.setAttribute("rel", "nofollow archived");
                addClass(element, "autofix-healed");
                logEvent({
                  event: "link.rewrite",
                  level: "debug",
                  url: absolute,
                  url_key: key,
                  status: "HEALED",
                  resolved_url: cached.resolved_url,
                });
              } else if (!cached) {
                discovered.add(absolute);
              }
            } catch {
              // Never break the stream for a single link
            }
          },
        })
        .transform(response);

      if (discovered.size > 0) {
        ctx.waitUntil(
          reportDiscoveries(env, Array.from(discovered)).catch((err) => {
            logEvent({
              event: "link.discover",
              level: "error",
              error: err instanceof Error ? err.message : String(err),
              count: discovered.size,
            });
          })
        );
      }

      return transformed;
    } catch (err) {
      logEvent({
        event: "link.rewrite",
        level: "error",
        error: err instanceof Error ? err.message : String(err),
      });
      return response;
    }
  },

  /**
   * Queue consumer: per-message ack / retry with exponential backoff.
   * Circuit breaker short-circuits when healer is down (messages retry later).
   */
  async queue(batch: MessageBatch<DiscoveryMessage>, env: Env): Promise<void> {
    for (const msg of batch.messages) {
      try {
        const urls = msg.body?.urls;

        if (!Array.isArray(urls) || urls.length === 0) {
          logEvent({
            event: "queue.consume",
            level: "warn",
            msg_id: msg.id,
            error: "invalid_body",
          });
          msg.ack();
          continue;
        }

        const unique = [
          ...new Set(urls.filter((u) => typeof u === "string" && u.length > 0)),
        ];
        if (unique.length === 0) {
          msg.ack();
          continue;
        }

        await forwardToHealer(env, unique);
        logEvent({
          event: "queue.consume",
          msg_id: msg.id,
          count: unique.length,
        });
        msg.ack();
      } catch (err) {
        const attempts = msg.attempts ?? 0;
        const message = err instanceof Error ? err.message : String(err);
        const base = message === "CircuitOpen" ? 120 : 60;
        const delaySeconds = Math.min(base * 2 ** attempts, 3600);
        logEvent({
          event: "queue.retry",
          level: "error",
          msg_id: msg.id,
          attempt: attempts,
          error: message,
          duration_ms: delaySeconds * 1000,
          circuit: healerBreaker.status,
          circuit_name: "edge_healer",
        });
        msg.retry({ delaySeconds });
      }
    }
  },
};

export default worker;

async function forwardToHealer(env: Env, urls: string[]): Promise<void> {
  if (!env.HEALER_DISCOVER_URL) {
    throw new Error("HEALER_DISCOVER_URL not configured");
  }

  const { traceparent, traceId } = newTraceparent();
  const t0 = Date.now();

  await healerBreaker.exec(async () => {
    logEvent({
      event: "healer.forward",
      count: urls.length,
      trace_id: traceId,
      circuit: healerBreaker.status,
      circuit_name: "edge_healer",
    });

    const res = await fetch(env.HEALER_DISCOVER_URL!, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        traceparent,
      },
      body: JSON.stringify({ urls }),
      signal: AbortSignal.timeout(15_000),
    });

    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`Healer ${res.status}: ${text.slice(0, 200)}`);
    }

    logEvent({
      event: "healer.forward",
      level: "debug",
      count: urls.length,
      duration_ms: Date.now() - t0,
      trace_id: traceId,
    });
  });
}

async function fallbackHttp(env: Env, urls: string[]): Promise<void> {
  if (!env.HEALER_DISCOVER_URL) return;
  try {
    await forwardToHealer(env, urls);
    logEvent({
      event: "queue.fallback_http",
      count: urls.length,
    });
  } catch (err) {
    logEvent({
      event: "queue.fallback_http",
      level: "error",
      count: urls.length,
      error: err instanceof Error ? err.message : String(err),
      circuit: healerBreaker.status,
      circuit_name: "edge_healer",
    });
  }
}

async function reportDiscoveries(env: Env, urls: string[]): Promise<void> {
  const now = new Date().toISOString();
  const t0 = Date.now();

  if (env.AUTOFIX_KV) {
    await Promise.all(
      urls.map(async (url) => {
        try {
          const key = urlKey(url);
          const existing = await env.AUTOFIX_KV.get(key);
          if (existing) return;
          const record: LinkRecord = {
            status: "PENDING",
            original_url: url,
            discovered_at: now,
          };
          await env.AUTOFIX_KV.put(key, JSON.stringify(record));
        } catch (err) {
          logEvent({
            event: "link.discover",
            level: "error",
            url,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      })
    );
  }

  if (env.DISCOVERY_QUEUE) {
    try {
      const message: DiscoveryMessage = { urls, discovered_at: now };
      await env.DISCOVERY_QUEUE.send(message);
      logEvent({
        event: "queue.send",
        count: urls.length,
        duration_ms: Date.now() - t0,
      });
      logEvent({
        event: "link.discover",
        count: urls.length,
        status: "PENDING",
        duration_ms: Date.now() - t0,
      });
      return;
    } catch (err) {
      logEvent({
        event: "queue.send",
        level: "error",
        count: urls.length,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  await fallbackHttp(env, urls);
  logEvent({
    event: "link.discover",
    count: urls.length,
    status: "PENDING",
    duration_ms: Date.now() - t0,
  });
}
