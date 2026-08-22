#!/usr/bin/env bash
# Bootstrap Cloudflare KV + Queues for AutoFix.
# Requires: wrangler logged in (npx wrangler login)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/edge-worker"

if ! command -v npx >/dev/null 2>&1; then
  echo "npx not found; install Node.js 18+"
  exit 1
fi

echo "==> Installing edge-worker deps"
npm install

echo "==> Creating KV namespace AUTOFIX_KV (production)"
npx wrangler kv:namespace create AUTOFIX_KV || true

echo "==> Creating KV namespace AUTOFIX_KV (preview)"
npx wrangler kv:namespace create AUTOFIX_KV --preview || true

echo "==> Creating queue autofix-discovery"
npx wrangler queues create autofix-discovery || true

echo "==> Creating optional DLQ autofix-discovery-dlq"
npx wrangler queues create autofix-discovery-dlq || true

cat << 'EOF'

Next steps:
  1. Copy the KV id / preview_id from the output above into edge-worker/wrangler.toml
  2. Optionally uncomment dead_letter_queue in wrangler.toml
  3. npx wrangler deploy
  4. Start the healer and: npx wrangler secret put HEALER_DISCOVER_URL

See DEPLOY.md for full instructions.
EOF
