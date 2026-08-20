// The Edge Layer: High-performance HTML streaming rewriter
// + Cloudflare Queue producer/consumer for durable discovery

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
   * Poison messages are acked so they do not loop forever.
   * After max_retries, Cloudflare routes to dead_letter_queue (if configured).
   */
  async queue(batch: MessageBatch<DiscoveryMessage>, env: Env): Promise<void> {
    for (const msg of batch.messages) {
      try {
        const urls = msg.body?.urls;

        // Poison / malformed — ack to avoid infinite retries
        if (!Array.isArray(urls) || urls.length === 0) {
          console.warn(`Invalid message body, acking id=${msg.id}`);
          msg.ack();
          continue;
        }

        const unique = [...new Set(urls.filter((u) => typeof u === "string" && u.length > 0))];
        if (unique.length === 0) {
          msg.ack();
          continue;
        }

        await forwardToHealer(env, unique);
        msg.ack();
      } catch (err) {
        const attempts = msg.attempts ?? 0;
        console.error(
          `Message ${msg.id} failed (attempt ${attempts}):`,
          err instanceof Error ? err.message : err
        );

        // Retry with exponential backoff (cap 1h). After max_retries → DLQ.
        const delaySeconds = Math.min(60 * 2 ** attempts, 3600);
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

  console.log(`Forwarding ${urls.length} url(s) to healer`);

  const res = await fetch(env.HEALER_DISCOVER_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ urls }),
    signal: AbortSignal.timeout(15_000),
  });

  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(`Healer ${res.status}: ${text.slice(0, 200)}`);
  }
}

async function fallbackHttp(env: Env, urls: string[]): Promise<void> {
  if (!env.HEALER_DISCOVER_URL) return;
  try {
    const res = await fetch(env.HEALER_DISCOVER_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ urls }),
      signal: AbortSignal.timeout(15_000),
    });
    if (!res.ok) {
      console.error(`Healer HTTP ${res.status}: ${await res.text().catch(() => "")}`);
    }
  } catch (err) {
    console.error("HTTP fallback failed:", err instanceof Error ? err.message : err);
  }
}

async function reportDiscoveries(env: Env, urls: string[]): Promise<void> {
  const now = new Date().toISOString();

  // 1. PENDING markers in KV
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

  // 2. Durable queue (preferred)
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

  // 3. HTTP fallback
  await fallbackHttp(env, urls);
  console.log(`DISCOVERED ${urls.length} new link(s)`);
}
