#!/usr/bin/env bash
# Create AUTOFIX_KV (prod + preview) only and patch wrangler.toml.
# Thin wrapper around the KV portion of setup-cloudflare.sh logic.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
exec "$ROOT/scripts/setup-cloudflare.sh"
