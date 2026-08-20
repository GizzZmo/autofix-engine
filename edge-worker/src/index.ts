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
    // Extremely defensive fallback
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

    // No KV binding configured yet — pass through unchanged
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

  async queue(batch: MessageBatch<DiscoveryMessage>, env: Env): Promise<void> {
    const allUrls = new Set<string>();

    for (const msg of batch.messages) {
      try {
        const body = msg.body;
        if (body && Array.isArray(body.urls)) {
          for (const u of body.urls) {
            if (typeof u === "string" && u.length > 0) allUrls.add(u);
          }
        }
        msg.ack();
      } catch {
        msg.retry();
      }
    }

    if (allUrls.size === 0) return;

    const urls = Array.from(allUrls);
    console.log(`Queue batch: forwarding ${urls.length} url(s) to healer`);

    if (!env.HEALER_DISCOVER_URL) {
      console.warn("HEALER_DISCOVER_URL not set — batch acked, not forwarded");
      return;
    }

    try {
      const res = await fetch(env.HEALER_DISCOVER_URL, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ urls }),
      });
      if (!res.ok) {
        console.error(`Healer returned ${res.status}`);
      }
    } catch (err) {
      console.error("Failed to forward queue batch to healer:", err);
    }
  },
};

export default worker;

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
      console.error("Queue send failed, falling back to HTTP:", err);
    }
  }

  // 3. HTTP fallback
  if (env.HEALER_DISCOVER_URL) {
    try {
      await fetch(env.HEALER_DISCOVER_URL, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ urls }),
      });
    } catch (err) {
      console.error("Failed to push discoveries to healer:", err);
    }
  }

  console.log(`DISCOVERED ${urls.length} new link(s)`);
}
