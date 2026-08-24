#!/usr/bin/env bash
# Ensure Cloudflare KV namespaces + Queues exist and patch wrangler.toml.
#
# Auth (either):
#   - Interactive: wrangler login
#   - CI: CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID
#
# Optional overrides (skip create if already known):
#   CLOUDFLARE_KV_NAMESPACE_ID
#   CLOUDFLARE_KV_PREVIEW_ID
#
# Usage:
#   ./scripts/ensure-cloudflare-resources.sh
#   SKIP_NPM=1 ./scripts/ensure-cloudflare-resources.sh   # CI (deps already installed)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EDGE="$ROOT/edge-worker"
TOML="$EDGE/wrangler.toml"
BINDING="AUTOFIX_KV"
QUEUE_NAME="autofix-discovery"
DLQ_NAME="autofix-discovery-dlq"
SKIP_NPM="${SKIP_NPM:-0}"

cd "$EDGE"

if ! command -v npx >/dev/null 2>&1; then
  echo "::error::npx not found; install Node.js 18+"
  exit 1
fi

if [[ ! -f "$TOML" ]]; then
  echo "::error::Missing $TOML"
  exit 1
fi

if [[ "$SKIP_NPM" != "1" ]]; then
  if [[ -f package-lock.json ]]; then
    npm ci
  else
    npm install
  fi
fi

is_cf_kv_id() {
  [[ "$1" =~ ^[0-9a-fA-F]{32}$ ]]
}

extract_hex32() {
  local text="$1"
  local id
  id=$(printf '%s' "$text" | grep -oE '(id|preview_id)[[:space:]]*=[[:space:]]*"[0-9a-fA-F]{32}"' | head -1 | grep -oE '[0-9a-fA-F]{32}' || true)
  if [[ -z "$id" ]]; then
    id=$(printf '%s' "$text" | grep -oE '"id"[[:space:]]*:[[:space:]]*"[0-9a-fA-F]{32}"' | head -1 | grep -oE '[0-9a-fA-F]{32}' || true)
  fi
  if [[ -z "$id" ]]; then
    id=$(printf '%s' "$text" | grep -oE '[0-9a-fA-F]{32}' | head -1 || true)
  fi
  printf '%s' "$id"
}

wrangler_kv() {
  # Prefer modern subcommand; fall back to legacy kv:namespace
  if npx wrangler kv namespace --help >/dev/null 2>&1; then
    npx wrangler kv namespace "$@"
  else
    # map: create X --preview → kv:namespace create X --preview
    npx wrangler kv:namespace "$@"
  fi
}

list_namespaces_json() {
  set +e
  local out
  out=$(npx wrangler kv namespace list --format=json 2>/dev/null)
  local rc=$?
  if [[ $rc -ne 0 || -z "$out" ]]; then
    out=$(npx wrangler kv:namespace list 2>/dev/null)
  fi
  set -e
  printf '%s' "$out"
}

resolve_from_list() {
  local want_preview="${1:-}"
  local list
  list=$(list_namespaces_json)

  if [[ -z "$list" ]]; then
    printf ''
    return 0
  fi

  # Prefer node for reliable JSON title matching when available
  if command -v node >/dev/null 2>&1 && printf '%s' "$list" | head -c1 | grep -q '\['; then
    local id
    id=$(LIST_JSON="$list" WANT_PREVIEW="$want_preview" BINDING="$BINDING" node -e '
      const raw = process.env.LIST_JSON || "[]";
      let arr;
      try { arr = JSON.parse(raw); } catch { process.exit(0); }
      if (!Array.isArray(arr)) process.exit(0);
      const binding = (process.env.BINDING || "AUTOFIX_KV").toLowerCase();
      const wantPreview = process.env.WANT_PREVIEW === "--preview";
      const score = (t) => {
        t = String(t || "").toLowerCase();
        let s = 0;
        if (t.includes(binding)) s += 10;
        if (t.includes("autofix")) s += 5;
        const isPrev = t.includes("preview") || t.endsWith("_preview");
        if (wantPreview && isPrev) s += 20;
        if (!wantPreview && !isPrev) s += 20;
        if (wantPreview && !isPrev) s -= 5;
        if (!wantPreview && isPrev) s -= 5;
        return s;
      };
      arr.sort((a, b) => score(b.title) - score(a.title));
      const best = arr[0];
      if (best && score(best.title) >= 10 && best.id) process.stdout.write(String(best.id));
    ' 2>/dev/null || true)
    if [[ -n "$id" ]] && is_cf_kv_id "$id"; then
      printf '%s' "$id"
      return 0
    fi
  fi

  # Grep fallback
  local pattern="AUTOFIX_KV"
  if [[ "$want_preview" == "--preview" ]]; then
    pattern="preview"
  fi
  local id
  id=$(printf '%s' "$list" | grep -i "$pattern" | grep -oE '[0-9a-fA-F]{32}' | head -1 || true)
  printf '%s' "$id"
}

create_or_resolve_kv() {
  local preview_flag="${1:-}"
  local label="production"
  [[ "$preview_flag" == "--preview" ]] && label="preview"

  echo "==> Ensuring KV namespace $BINDING ($label)"

  local out rc id
  set +e
  if [[ -n "$preview_flag" ]]; then
    out=$(wrangler_kv create "$BINDING" --preview 2>&1)
  else
    out=$(wrangler_kv create "$BINDING" 2>&1)
  fi
  rc=$?
  set -e
  printf '%s\n' "$out"

  id=$(extract_hex32 "$out")
  if is_cf_kv_id "$id"; then
    printf '%s' "$id"
    return 0
  fi

  echo "==> Create did not yield id (rc=$rc); resolving from list…"
  id=$(resolve_from_list "$preview_flag")
  if is_cf_kv_id "$id"; then
    printf '%s' "$id"
    return 0
  fi

  printf ''
}

patch_toml_kv() {
  local prod_id="$1"
  local preview_id="$2"
  local tmp
  tmp=$(mktemp)

  awk -v pid="$prod_id" -v previd="$preview_id" '
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
  echo "==> Patched $TOML"
  grep -E '^(id|preview_id|binding)[[:space:]]*=' "$TOML" || true
}

ensure_queue() {
  local name="$1"
  echo "==> Ensuring queue $name"
  set +e
  local out
  out=$(npx wrangler queues create "$name" 2>&1)
  local rc=$?
  set -e
  printf '%s\n' "$out"
  if [[ $rc -ne 0 ]]; then
    if printf '%s' "$out" | grep -qiE 'already exists|duplicate|409'; then
      echo "(queue $name already exists)"
      return 0
    fi
    echo "::warning::Could not create queue $name (rc=$rc) — deploy may still succeed if it exists"
  fi
}

# ---------------------------------------------------------------------------
# Resolve production + preview ids
# ---------------------------------------------------------------------------

PROD_ID="${CLOUDFLARE_KV_NAMESPACE_ID:-}"
PREVIEW_ID="${CLOUDFLARE_KV_PREVIEW_ID:-}"

if [[ -n "$PROD_ID" ]] && ! is_cf_kv_id "$PROD_ID"; then
  echo "::error::CLOUDFLARE_KV_NAMESPACE_ID invalid: $PROD_ID"
  exit 1
fi
if [[ -n "$PREVIEW_ID" ]] && ! is_cf_kv_id "$PREVIEW_ID"; then
  echo "::error::CLOUDFLARE_KV_PREVIEW_ID invalid: $PREVIEW_ID"
  exit 1
fi

if [[ -z "$PROD_ID" ]]; then
  PROD_ID=$(create_or_resolve_kv)
  echo ""
fi
if [[ -z "$PREVIEW_ID" ]]; then
  PREVIEW_ID=$(create_or_resolve_kv --preview)
  echo ""
fi

if ! is_cf_kv_id "$PROD_ID" || ! is_cf_kv_id "$PREVIEW_ID"; then
  echo "::error::Failed to obtain both KV namespace ids"
  echo "  prod=[$PROD_ID] preview=[$PREVIEW_ID]"
  echo "Ensure CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID are set (Workers KV Edit)."
  exit 1
fi

echo "Resolved production id: $PROD_ID"
echo "Resolved preview id:    $PREVIEW_ID"

# Export for subsequent steps / check script
if [[ -n "${GITHUB_ENV:-}" ]]; then
  {
    echo "CLOUDFLARE_KV_NAMESPACE_ID=$PROD_ID"
    echo "CLOUDFLARE_KV_PREVIEW_ID=$PREVIEW_ID"
  } >> "$GITHUB_ENV"
fi
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "prod_id=$PROD_ID"
    echo "preview_id=$PREVIEW_ID"
  } >> "$GITHUB_OUTPUT"
fi

patch_toml_kv "$PROD_ID" "$PREVIEW_ID"

ensure_queue "$QUEUE_NAME"
ensure_queue "$DLQ_NAME"

echo ""
echo "Cloudflare resources ready."
