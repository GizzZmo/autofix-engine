#!/usr/bin/env bash
# Validate edge-worker/wrangler.toml KV ids are not placeholders and look like CF namespace ids.
set -euo pipefail

TOML="${1:-edge-worker/wrangler.toml}"

if [[ ! -f "$TOML" ]]; then
  echo "::error::File not found: $TOML"
  exit 1
fi

fail=0

# Extract values for id / preview_id under the AUTOFIX_KV binding (simple line parse).
kv_id=$(grep -E '^id\s*=' "$TOML" | head -1 | sed -E 's/^id\s*=\s*"?([^"#]+)"?.*/\1/' | tr -d '[:space:]')
kv_preview=$(grep -E '^preview_id\s*=' "$TOML" | head -1 | sed -E 's/^preview_id\s*=\s*"?([^"#]+)"?.*/\1/' | tr -d '[:space:]')

echo "Parsed id=[$kv_id]"
echo "Parsed preview_id=[$kv_preview]"

PLACEHOLDERS=(
  "YOUR_KV_NAMESPACE_ID"
  "YOUR_KV_PREVIEW_ID"
  "YOUR_KV_NAMESPACE"
  "REPLACE_ME"
  "TODO"
  "changeme"
)

for p in "${PLACEHOLDERS[@]}"; do
  if grep -qF "$p" "$TOML"; then
    echo "::error::Placeholder still present in $TOML: $p"
    fail=1
  fi
done

# Cloudflare KV namespace ids are 32 hex characters.
is_cf_kv_id() {
  [[ "$1" =~ ^[0-9a-fA-F]{32}$ ]]
}

if [[ -z "$kv_id" ]]; then
  echo "::error::Missing id = \"...\" under [[kv_namespaces]] in $TOML"
  fail=1
elif ! is_cf_kv_id "$kv_id"; then
  echo "::error::id must be a 32-char hex Cloudflare KV namespace id, got: $kv_id"
  echo "Run: npx wrangler kv:namespace create AUTOFIX_KV"
  fail=1
fi

if [[ -z "$kv_preview" ]]; then
  echo "::error::Missing preview_id = \"...\" under [[kv_namespaces]] in $TOML"
  fail=1
elif ! is_cf_kv_id "$kv_preview"; then
  echo "::error::preview_id must be a 32-char hex Cloudflare KV namespace id, got: $kv_preview"
  echo "Run: npx wrangler kv:namespace create AUTOFIX_KV --preview"
  fail=1
fi

if [[ "$kv_id" == "$kv_preview" ]] && [[ -n "$kv_id" ]]; then
  echo "::warning::id and preview_id are identical; production and preview usually use different namespaces."
fi

if [[ "$fail" -ne 0 ]]; then
  echo ""
  echo "Fix: ./scripts/setup-cloudflare.sh  → paste ids into edge-worker/wrangler.toml → commit"
  echo "See DEPLOY.md"
  exit 1
fi

echo "KV ids look valid."
