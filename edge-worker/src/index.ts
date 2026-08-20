// The Edge Layer: High-performance HTML streaming rewriter
// + Cloudflare Queue producer/consumer for durable discovery

export interface Env {
  AUTOFIX_KV: KVNamespace;
  DISCOVERY_QUEUE?: Queue;
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

export default {
  // -----------------------------------------------------------------------
  // HTTP: intercept HTML and rewrite healed links
  // -----------------------------------------------------------------------
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const response = await fetch(request);
    const contentType = response.headers.get("content-type") || "";

    if (!contentType.includes("text/html")) {
      return response;
    }

    const discovered = new Set<string>();

    const transformed = new HTMLRewriter()
      .on("a[href]", {
        async element(element) {
          const href = element.getAttribute("href");
          if (!href || href.startsWith("/") || href.startsWith("#") || href.startsWith("mailto:")) {
            return;
          }

          let absolute = href;
          try {
            absolute = new URL(href, request.url).href;
          } catch {
            return;
          }
          if (!absolute.startsWith("http")) return;

          const urlHash = btoa(absolute);
          const cached = (await env.AUTOFIX_KV.get(urlHash, { type: "json" })) as LinkRecord | null;

          if (cached?.status === "HEALED" && cached.resolved_url) {
            element.setAttribute("href", cached.resolved_url);
            element.setAttribute("data-autofix-original", absolute);
            element.setAttribute("rel", "nofollow archived");
            element.classList.add("autofix-healed");
          } else if (!cached) {
            discovered.add(absolute);
          }
        },
      })
      .transform(response);

    if (discovered.size > 0) {
      ctx.waitUntil(reportDiscoveries(env, Array.from(discovered)));
    }

    return transformed;
  },

  // -----------------------------------------------------------------------
  // Queue consumer: durable delivery of discovered links to the Go healer
  // -----------------------------------------------------------------------
  async queue(batch: MessageBatch<DiscoveryMessage>, env: Env): Promise<void> {
    const allUrls = new Set<string>();
    for (const msg of batch.messages) {
      for (const u of msg.body.urls || []) {
        if (u) allUrls.add(u);
      }
      msg.ack();
    }

    if (allUrls.size === 0) return;

    const urls = Array.from(allUrls);
    console.log(`Queue batch: forwarding ${urls.length} url(s) to healer`);

    if (!env.HEALER_DISCOVER_URL) {
      console.warn("HEALER_DISCOVER_URL not set — queue messages acked but not forwarded");
      return;
    }

    try {
      const res = await fetch(env.HEALER_DISCOVER_URL, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ urls }),
      });
      if (!res.ok) {
        console.error(`Healer returned ${res.status}: ${await res.text()}`);
        // Do not throw — messages already acked. Retry logic is handled by
        // the producer path writing PENDING into KV as a fallback.
      }
    } catch (err) {
      console.error("Failed to forward queue batch to healer:", err);
    }
  },
};

async function reportDiscoveries(env: Env, urls: string[]): Promise<void> {
  const now = new Date().toISOString();

  // 1. Write PENDING records into KV (source of truth + fallback)
  await Promise.all(
    urls.map(async (url) => {
      const key = btoa(url);
      const existing = await env.AUTOFIX_KV.get(key);
      if (existing) return;

      const record: LinkRecord = {
        status: "PENDING",
        original_url: url,
        discovered_at: now,
      };
      await env.AUTOFIX_KV.put(key, JSON.stringify(record));
    })
  );

  // 2. Prefer durable Queue when available
  if (env.DISCOVERY_QUEUE) {
    try {
      await env.DISCOVERY_QUEUE.send({
        urls,
        discovered_at: now,
      } satisfies DiscoveryMessage);
      console.log(`Queued ${urls.length} link(s) on DISCOVERY_QUEUE`);
      return;
    } catch (err) {
      console.error("Queue send failed, falling back to HTTP:", err);
    }
  }

  // 3. Fallback: direct HTTP POST to healer
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
