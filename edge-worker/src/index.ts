// The Edge Layer: High-performance HTML streaming rewriter
export interface Env {
  AUTOFIX_KV: KVNamespace;
  HEALER_DISCOVER_URL?: string;
  ENVIRONMENT?: string;
}

interface LinkRecord {
  status: "PENDING" | "HEALED" | "DEAD" | "HEALTHY";
  original_url: string;
  resolved_url?: string;
  discovered_at?: string;
  healed_at?: string;
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const response = await fetch(request);
    const contentType = response.headers.get("content-type") || "";

    // Only process HTML
    if (!contentType.includes("text/html")) {
      return response;
    }

    // Collect unique external hrefs during rewrite so we can fire discovery
    // without blocking the stream more than necessary.
    const discovered = new Set<string>();

    const transformed = new HTMLRewriter()
      .on("a[href]", {
        async element(element) {
          const href = element.getAttribute("href");
          if (!href || href.startsWith("/") || href.startsWith("#") || href.startsWith("mailto:")) {
            return;
          }

          // Prefer absolute http(s) links only
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

    // Non-blocking discovery: write PENDING markers + optional push to healer
    if (discovered.size > 0) {
      ctx.waitUntil(reportDiscoveries(env, Array.from(discovered)));
    }

    return transformed;
  },
};

async function reportDiscoveries(env: Env, urls: string[]): Promise<void> {
  const now = new Date().toISOString();

  // 1. Write PENDING records into KV so the healer can also pick them up by key
  await Promise.all(
    urls.map(async (url) => {
      const key = btoa(url);
      const existing = await env.AUTOFIX_KV.get(key);
      if (existing) return; // already known

      const record: LinkRecord = {
        status: "PENDING",
        original_url: url,
        discovered_at: now,
      };
      await env.AUTOFIX_KV.put(key, JSON.stringify(record));
    })
  );

  // 2. Optional: push batch to healer discovery endpoint (fire-and-forget)
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
