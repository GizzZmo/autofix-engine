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


Architecture Review & Design Feedback: AutoFix Engine

Evaluated Document: AutoFix: Automated Link Health & Edge-Resolution Engine

Document Type: Professional / Technical System Architectural Blueprint

Target Audience: Senior Systems Architects, Edge Infrastructure Engineers, and Engineering Leadership

1. Executive Summary & Assessment

Your architectural blueprint for AutoFix presents a remarkably clean, modular, and forward-thinking edge architecture for solving a ubiquitous web health problem.

Proficiency Level: Good

Assessment Rubric

Technical Precision & Edge Soundness: Good — Solid understanding of edge proxy architecture, though edge async non-blocking execution requires finer refinement.

Architecture Completeness: Good — Clear end-to-end data flow, though SEO implications and edge failure modes need explicit protocols.

Organization & Readability: Outstanding — Logical section progression, ASCII flow diagram, and structured pseudo-code.

Operational Guardrails: Good — Quantitative target ($T_{\text{TTFB}} < 3\text{ms}$) defined, but false-positive detection needs hardening.

2. Key Growth Areas & Technical Recommendations

Area 1: Edge Runtime Latency & Streaming Execution (Technical Precision)

Specific Text Example: Section 7 (Edge Processing Logic) pseudo-code:

const linkHash = await hashUrl(href);
const linkMetadata = await AUTOFIX_KV.get(linkHash, { type: 'json' });


Issue: Executing an asynchronous $I/O$ call (await AUTOFIX_KV.get()) inside the element(element) handler of HTMLRewriter for every <a> tag forces the edge streaming parser to pause stream processing. On pages with 50+ links, sequential KV calls will quickly exceed your target Time to First Byte limit ($T_{\text{TTFB}} < 3\text{ms}$).

Actionable Next Step: How about updating Section 7 to specify a two-phase edge processing pattern?

Phase 1 (Collect): Fast synchronous pass over the HTML stream to extract all unique href attributes.

Phase 2 (Batch Lookup): A single batched multi-key lookup (AUTOFIX_KV.getMany() or in-memory sub-request cache) before stream rewriting, or using an in-memory Bloom filter at the edge worker instance layer to immediately bypass healthy links without KV I/O.

Area 2: SEO & Search Crawler Protocols (Architectural Completeness)

Specific Text Example: Section 5 (Mode 1) states: "SEO crawlers receive updated, valid links directly" and Section 6 outlines replacing dead links with Internet Archive targets.

Issue: Automatically rewriting dead links to external domains (like web.archive.org) without metadata annotations can accidentally dilute page rank or cause search crawlers to index third-party mirror pages instead of recognizing broken internal link structures.

Actionable Next Step: Would you like to add an explicit "SEO & Crawler Policy" subsection to Section 6? This should detail:

Injecting rel="nofollow archived" attributes when substituting third-party Wayback Machine links.

Serving conditional responses based on User-Agent (e.g., returning standard HTTP 301/307 redirects for search bots while serving inline JS modal fallbacks for real human visitors).

Area 3: False-Positive Prevention & Verification Protocols (Operational Guardrails)

Specific Text Example: Section 4.2 (Healing Pipeline):


$$\text{Target Link} \rightarrow [\text{HEAD Request}] \rightarrow \text{Status 200 OK?}$$

Issue: Modern edge firewalls (Cloudflare, Akamai) and Single Page Application (SPA) servers frequently reject automated HEAD requests with 403 Forbidden or 405 Method Not Allowed, or return 200 OK alongside a JavaScript-rendered "Soft 404" page. Relying solely on HTTP HEAD status codes will create significant false-positive flags.

Actionable Next Step: Consider expanding Section 4.2 to introduce a multi-tiered validation heuristic:

Primary: HEAD request with standard browser User-Agent.

Secondary Fallback: Range-header GET request (e.g., Range: bytes=0-1024) if HEAD returns 403/405.

Soft 404 Detection: HTML DOM inspection checking for page title strings like "404 Not Found", "Page Moved", or DOM text length thresholds.

3. Summary of Suggested Edits

  Section 4.2  -> Add HTTP GET Range request fallback and Soft-404 DOM signatures.
  Section 6    -> Add explicit SEO attributes (rel="nofollow", 301 vs 302 handling).
  Section 7    -> Optimize pseudo-code to prevent async blocking in HTMLRewriter stream.
