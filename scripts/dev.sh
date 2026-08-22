#!/usr/bin/env bash
# Local dev: healer on :8080 + wrangler dev (needs two terminals if not using background).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ ! -f "$ROOT/healer/.env" ]]; then
  echo "Missing healer/.env — copy from healer/.env.example and fill CF_* vars"
  exit 1
fi

# Ensure .dev.vars points at local healer
if [[ ! -f "$ROOT/edge-worker/.dev.vars" ]]; then
  echo "HEALER_DISCOVER_URL=http://127.0.0.1:8080/v1/discover" > "$ROOT/edge-worker/.dev.vars"
  echo "Wrote edge-worker/.dev.vars"
fi

echo "Starting healer on :8080 ..."
(cd "$ROOT/healer" && go run .) &
HEALER_PID=$!

cleanup() {
  echo "Stopping healer (pid $HEALER_PID)..."
  kill "$HEALER_PID" 2>/dev/null || true
}
trap cleanup EXIT

sleep 1
echo "Starting wrangler dev ..."
(cd "$ROOT/edge-worker" && npx wrangler dev)
