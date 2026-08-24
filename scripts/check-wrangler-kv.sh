#!/usr/bin/env bash
# Validate edge-worker/wrangler.toml KV ids (or inject from env, then validate).
#
# Env (optional, preferred for CI):
#   CLOUDFLARE_KV_NAMESPACE_ID   32-char hex production namespace id
#   CLOUDFLARE_KV_PREVIEW_ID     32-char hex preview namespace id
#
# Flags:
#   --write   Apply env ids into the toml before validating (default when env set)
#   --soft    Exit 0 with warning if placeholders remain (no real ids available)
set -euo pipefail

TOML="edge-worker/wrangler.toml"
SOFT=0
WRITE=0

for arg in "$@"; do
  case "$arg" in
    --soft) SOFT=1 ;;
    --write) WRITE=1 ;;
    -*) echo "Unknown flag: $arg" >&2; exit 2 ;;
    *) TOML="$arg" ;;
  esac
done

if [[ ! -f "$TOML" ]]; then
  echo "::error::File not found: $TOML"
  exit 1
fi

is_cf_kv_id() {
  [[ "$1" =~ ^[0-9a-fA-F]{32}$ ]]
}

# ---------------------------------------------------------------------------
# Optional inject from environment (GitHub Actions vars / local export)
# ---------------------------------------------------------------------------
PROD_ENV="${CLOUDFLARE_KV_NAMESPACE_ID:-}"
PREV_ENV="${CLOUDFLARE_KV_PREVIEW_ID:-}"

if [[ -n "$PROD_ENV" || -n "$PREV_ENV" ]]; then
  WRITE=1
fi

if [[ "$WRITE" -eq 1 ]]; then
  if [[ -n "$PROD_ENV" ]] && ! is_cf_kv_id "$PROD_ENV"; then
    echo "::error::CLOUDFLARE_KV_NAMESPACE_ID must be 32-char hex, got: $PROD_ENV"
    exit 1
  fi
  if [[ -n "$PREV_ENV" ]] && ! is_cf_kv_id "$PREV_ENV"; then
    echo "::error::CLOUDFLARE_KV_PREVIEW_ID must be 32-char hex, got: $PREV_ENV"
    exit 1
  fi

  if [[ -n "$PROD_ENV" || -n "$PREV_ENV" ]]; then
    tmp=$(mktemp)
    awk -v pid="$PROD_ENV" -v previd="$PREV_ENV" '
      BEGIN { in_kv = 0 }
      /^\[\[kv_namespaces\]\]/ { in_kv = 1; print; next }
      in_kv && /^id[[:space:]]*=/ {
        if (pid != "") print "id = \"" pid "\""
        else print
        next
      }
      in_kv && /^preview_id[[:space:]]*=/ {
        if (previd != "") print "preview_id = \"" previd "\""
        else print
        next
      }
      in_kv && /^\[/ { in_kv = 0 }
      { print }
    ' "$TOML" > "$tmp"
    mv "$tmp" "$TOML"
    echo "Injected KV ids from environment into $TOML"
  fi
fi

# ---------------------------------------------------------------------------
# Parse + validate
# ---------------------------------------------------------------------------
fail=0

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

has_placeholder=0
for p in "${PLACEHOLDERS[@]}"; do
  if grep -qF "$p" "$TOML"; then
    echo "Placeholder still present in $TOML: $p"
    has_placeholder=1
  fi
done

if [[ -z "$kv_id" ]]; then
  echo "::error::Missing id = \"...\" under [[kv_namespaces]] in $TOML"
  fail=1
elif ! is_cf_kv_id "$kv_id"; then
  echo "id is not a 32-char hex Cloudflare KV namespace id: $kv_id"
  fail=1
fi

if [[ -z "$kv_preview" ]]; then
  echo "::error::Missing preview_id = \"...\" under [[kv_namespaces]] in $TOML"
  fail=1
elif ! is_cf_kv_id "$kv_preview"; then
  echo "preview_id is not a 32-char hex Cloudflare KV namespace id: $kv_preview"
  fail=1
fi

if [[ "$kv_id" == "$kv_preview" ]] && is_cf_kv_id "$kv_id"; then
  echo "::warning::id and preview_id are identical; production and preview usually use different namespaces."
fi

if [[ "$fail" -ne 0 || "$has_placeholder" -ne 0 ]]; then
  if [[ "$SOFT" -eq 1 ]]; then
    echo "::warning::KV placeholders present — skipping hard failure (--soft)."
    echo "Create namespaces: ./scripts/setup-cloudflare.sh"
    echo "Or set GitHub vars CLOUDFLARE_KV_NAMESPACE_ID / CLOUDFLARE_KV_PREVIEW_ID for CI deploy."
    echo "See DEPLOY.md"
    exit 0
  fi
  echo "::error::Placeholder or invalid KV id in $TOML"
  echo "Fix options:"
  echo "  1. Local:  ./scripts/setup-cloudflare.sh  → commit wrangler.toml"
  echo "  2. CI:     Repo → Settings → Variables → CLOUDFLARE_KV_NAMESPACE_ID + CLOUDFLARE_KV_PREVIEW_ID"
  echo "  3. Manual: paste 32-char hex ids into edge-worker/wrangler.toml"
  echo "See DEPLOY.md"
  exit 1
fi

echo "KV ids look valid."
