#!/usr/bin/env bash
# Bootstrap Cloudflare KV + Queues for AutoFix and patch wrangler.toml.
# Requires: wrangler logged in (npx wrangler login)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EDGE="$ROOT/edge-worker"
TOML="$EDGE/wrangler.toml"
BINDING="AUTOFIX_KV"

cd "$EDGE"

if ! command -v npx >/dev/null 2>&1; then
  echo "npx not found; install Node.js 18+"
  exit 1
fi

if [[ ! -f "$TOML" ]]; then
  echo "Missing $TOML"
  exit 1
fi

echo "==> Installing edge-worker deps"
npm install

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

extract_hex32() {
  # Prefer id = "..." style, then any 32-char hex token
  local text="$1"
  local id
  id=$(printf '%s' "$text" | grep -oE '(id|preview_id)[[:space:]]*=[[:space:]]*"[0-9a-fA-F]{32}"' | head -1 | grep -oE '[0-9a-fA-F]{32}' || true)
  if [[ -z "$id" ]]; then
    id=$(printf '%s' "$text" | grep -oE '[0-9a-fA-F]{32}' | head -1 || true)
  fi
  printf '%s' "$id"
}

create_kv() {
  local preview_flag="${1:-}"
  local label="production"
  local cmd=(npx wrangler kv namespace create "$BINDING")
  # Support both modern and legacy command names
  if ! npx wrangler kv namespace --help >/dev/null 2>&1; then
    cmd=(npx wrangler kv:namespace create "$BINDING")
  fi
  if [[ "$preview_flag" == "--preview" ]]; then
    label="preview"
    cmd+=(--preview)
  fi

  echo "==> Creating KV namespace $BINDING ($label)"
  local out
  set +e
  out=$("${cmd[@]}" 2>&1)
  local rc=$?
  set -e
  printf '%s\n' "$out"

  local id
  id=$(extract_hex32 "$out")
  if [[ -n "$id" ]]; then
    printf '%s' "$id"
    return 0
  fi

  # Already exists or non-zero — try list + title match
  echo "==> Could not parse id from create output (rc=$rc); trying list…"
  resolve_from_list "$preview_flag"
}

resolve_from_list() {
  local preview_flag="${1:-}"
  local list
  set +e
  list=$(npx wrangler kv namespace list 2>/dev/null || npx wrangler kv:namespace list 2>/dev/null)
  set -e

  # Prefer titles containing AUTOFIX_KV / binding; preview titles often end with _preview
  local pattern="AUTOFIX_KV"
  if [[ "$preview_flag" == "--preview" ]]; then
    pattern="preview"
  fi

  local id
  id=$(printf '%s' "$list" | grep -i "$pattern" | grep -oE '[0-9a-fA-F]{32}' | head -1 || true)
  if [[ -z "$id" ]]; then
    # Last resort: first namespace id (dangerous) — skip
    echo "WARN: could not resolve namespace id from list" >&2
    printf ''
    return 0
  fi
  printf '%s' "$id"
}

patch_toml_kv() {
  local prod_id="$1"
  local preview_id="$2"
  local tmp
  tmp=$(mktemp)

  awk -v pid="$prod_id" -v previd="$preview_id" '
    BEGIN { in_kv = 0 }
    /^\[\[kv_namespaces\]\]/ { in_kv = 1; print; next }
    in_kv && /^binding[[:space:]]*=/ { print; next }
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
  echo "==> Updated $TOML"
  grep -E '^(id|preview_id|binding)' "$TOML" || true
}

# ---------------------------------------------------------------------------
# Create / resolve KV ids
# ---------------------------------------------------------------------------

PROD_ID=$(create_kv)
echo ""
PREVIEW_ID=$(create_kv --preview)
echo ""

if [[ -z "$PROD_ID" || -z "$PREVIEW_ID" ]]; then
  echo "ERROR: failed to obtain both KV ids."
  echo "  prod=[$PROD_ID] preview=[$PREVIEW_ID]"
  echo "Create manually and edit wrangler.toml, or re-run after wrangler login."
  exit 1
fi

echo "Resolved production id: $PROD_ID"
echo "Resolved preview id:    $PREVIEW_ID"

patch_toml_kv "$PROD_ID" "$PREVIEW_ID"

# ---------------------------------------------------------------------------
# Queues
# ---------------------------------------------------------------------------

echo "==> Creating queue autofix-discovery"
npx wrangler queues create autofix-discovery 2>&1 || echo "(queue may already exist)"

echo "==> Creating optional DLQ autofix-discovery-dlq"
npx wrangler queues create autofix-discovery-dlq 2>&1 || echo "(DLQ may already exist)"

echo ""
echo "Done. Next:"
echo "  1. Review edge-worker/wrangler.toml (KV ids written)"
echo "  2. Optional: uncomment dead_letter_queue in wrangler.toml"
echo "  3. cd edge-worker && npx wrangler deploy"
echo "  4. npx wrangler secret put HEALER_DISCOVER_URL"
echo "  5. Commit wrangler.toml if you want CI deploy to use these ids"
echo ""
echo "Validate: ./scripts/check-wrangler-kv.sh edge-worker/wrangler.toml"
