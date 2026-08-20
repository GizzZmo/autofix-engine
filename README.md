# 🔧 AutoFix: The Self-Healing Web Layer

AutoFix eliminates 404s and broken external links without requiring database migrations.

### How it works:
1. **Intercept**: The Edge Worker parses HTML as it leaves your server.
2. **Lookup**: It checks a global KV registry for the health status of every link.
3. **Resolve**: If a link is dead, it is hot-swapped with a **Wayback Machine** archive or a canonical alternative.
4. **Learn**: A background Go process crawls your site, finds dead links, and heals them asynchronously.

### Setup
1. `cd edge-worker && npm install && wrangler publish`
2. `cd healer && go run main.go`
3. Include `<script src="autofix.js"></script>` in your site footer.

### Tech Stack
- **Language**: TypeScript (Edge), Go (Backend), JavaScript (Browser).
- **Runtime**: Cloudflare Workers, Internet Archive API.
- **Data**: Edge KV (Global low-latency store).
