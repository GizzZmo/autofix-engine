// The Edge Layer: High-performance HTML streaming rewriter
export interface Env {
  AUTOFIX_KV: KVNamespace;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const response = await fetch(request);
    const contentType = response.headers.get('content-type') || '';

    // Only process HTML files
    if (!contentType.includes('text/html')) {
      return response;
    }

    // Rewrite the HTML stream
    return new HTMLRewriter()
      .on('a[href]', {
        async element(element) {
          const href = element.getAttribute('href');
          if (!href || href.startsWith('/') || href.startsWith('#')) return;

          // Check KV for a healed version of this URL (Base64 keying)
          const urlHash = btoa(href);
          const cached = await env.AUTOFIX_KV.get(urlHash, { type: 'json' }) as any;

          if (cached && cached.status === 'HEALED') {
            element.setAttribute('href', cached.resolved_url);
            element.setAttribute('data-autofix-original', href);
            element.classList.add('autofix-healed');
          } else if (!cached) {
            // Log for the background healer to discover
            console.log(`DISCOVERED_NEW_LINK: ${href}`);
          }
        }
      })
      .transform(response);
  },
};
