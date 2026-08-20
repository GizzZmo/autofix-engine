// The Edge Layer: High-performance HTML streaming rewriter
// + Cloudflare Queue producer/consumer + circuit breaker for healer

export interface Env {
  AUTOFIX_KV: KVNamespace;
  DISCOVERY_QUEUE?: Queue<DiscoveryMessage>;
  HEALER_DISCOVER_URL?: string;
  ENVIRONMENT?: string;
}

interface LinkRecord {
  status: "PENDING" | "HEALED" | "DEAD" | "HEALTHY";
  original_url: string;
  resolved_url?: string;
  discovered_at?: string;
  healed_at?: string;
  reason?: string;
}

interface DiscoveryMessage {
  urls: string[];
  discovered_at: string;
}

/** Safe base64 key for arbitrary URLs (handles Unicode). */
function urlKey(url: string): string {
  try {
    return btoa(unescape(encodeURIComponent(url)));
  } catch {
    return encodeURIComponent(url).slice(0, 512);
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
    this.failures = 0;
    this.state = "closed";
    this.halfOpenInFlight = 0;
  }

  private onFailure() {
    this.failures++;
    if (this.state === "half_open" || this.failures >= this.failureThreshold) {
      this.state = "open";
      this.openedAt = Date.now();
      this.halfOpenInFlight = 0;
      console.warn(
        `Circuit breaker OPEN after ${this.failures} failure(s); cooling ${this.openMs}ms`
      );
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
                element.classList.add("autofix-healed");
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
            console.error("reportDiscoveries failed:", err);
          })
        );
      }

      return transformed;
    } catch (err) {
      console.error("HTMLRewriter failed, returning original:", err);
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
          console.warn(`Invalid message body, acking id=${msg.id}`);
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
        msg.ack();
      } catch (err) {
        const attempts = msg.attempts ?? 0;
        const message = err instanceof Error ? err.message : String(err);
        console.error(`Message ${msg.id} failed (attempt ${attempts}):`, message);

        // Longer delay when circuit is open (dependency known-bad)
        const base = message === "CircuitOpen" ? 120 : 60;
        const delaySeconds = Math.min(base * 2 ** attempts, 3600);
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

  await healerBreaker.exec(async () => {
    console.log(`Forwarding ${urls.length} url(s) to healer`);

    const res = await fetch(env.HEALER_DISCOVER_URL!, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ urls }),
      signal: AbortSignal.timeout(15_000),
    });

    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`Healer ${res.status}: ${text.slice(0, 200)}`);
    }
  });
}

async function fallbackHttp(env: Env, urls: string[]): Promise<void> {
  if (!env.HEALER_DISCOVER_URL) return;
  try {
    await healerBreaker.exec(async () => {
      const res = await fetch(env.HEALER_DISCOVER_URL!, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ urls }),
        signal: AbortSignal.timeout(15_000),
      });
      if (!res.ok) {
        throw new Error(`Healer HTTP ${res.status}`);
      }
    });
  } catch (err) {
    console.error(
      "HTTP fallback failed:",
      err instanceof Error ? err.message : err
    );
  }
}

async function reportDiscoveries(env: Env, urls: string[]): Promise<void> {
  const now = new Date().toISOString();

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
          console.error("KV put failed for", url, err);
        }
      })
    );
  }

  if (env.DISCOVERY_QUEUE) {
    try {
      const message: DiscoveryMessage = { urls, discovered_at: now };
      await env.DISCOVERY_QUEUE.send(message);
      console.log(`Queued ${urls.length} link(s) on DISCOVERY_QUEUE`);
      return;
    } catch (err) {
      console.error(
        "Queue send failed, falling back to HTTP:",
        err instanceof Error ? err.message : err
      );
    }
  }

  await fallbackHttp(env, urls);
  console.log(`DISCOVERED ${urls.length} new link(s)`);
}
