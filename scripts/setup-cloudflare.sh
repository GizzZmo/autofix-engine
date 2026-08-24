#!/usr/bin/env bash
# Local bootstrap: install deps + ensure KV/queues + patch wrangler.toml.
# For CI, deploy-edge.yml calls ensure-cloudflare-resources.sh with SKIP_NPM=1.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
chmod +x "$ROOT/scripts/ensure-cloudflare-resources.sh" "$ROOT/scripts/check-wrangler-kv.sh"

"$ROOT/scripts/ensure-cloudflare-resources.sh"

echo ""
echo "Next:"
echo "  1. Review edge-worker/wrangler.toml (KV ids written)"
echo "  2. Optional: uncomment dead_letter_queue in wrangler.toml"
echo "  3. cd edge-worker && npx wrangler deploy"
echo "  4. npx wrangler secret put HEALER_DISCOVER_URL"
echo "  5. For CI: set secrets CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID"
echo "     (KV namespaces are auto-created on deploy; optional Variables still work)"
echo ""
echo "Validate: ./scripts/check-wrangler-kv.sh edge-worker/wrangler.toml"
