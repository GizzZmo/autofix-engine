#!/usr/bin/env bash
set -euo pipefail
export ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export DST="$ROOT/edge-worker/package-lock.json"

if [[ -f "$DST" ]] && [[ $(wc -c < "$DST") -gt 1000 ]]; then
  echo "package-lock.json already present ($(wc -c < "$DST") bytes)"
  exit 0
fi

expand_parts() {
  python3 -c '
import base64, gzip, os, sys
from pathlib import Path
root = Path(os.environ["ROOT"])
dst = Path(os.environ["DST"])
parts16 = [root / ("edge-worker/package-lock.p%02d.b64" % i) for i in range(16)]
parts4 = [root / ("edge-worker/package-lock.part%d.b64" % i) for i in range(4)]
if all(p.is_file() for p in parts16):
    parts = parts16
elif all(p.is_file() for p in parts4):
    parts = parts4
else:
    sys.exit(2)
data = "".join(p.read_text().strip() for p in parts)
dst.write_bytes(gzip.decompress(base64.b64decode(data)))
print("Wrote %s (%d bytes) from %d parts" % (dst, dst.stat().st_size, len(parts)))
'
}

if expand_parts 2>/tmp/expand-err.txt; then
  exit 0
fi
echo "part expand failed:" >&2
cat /tmp/expand-err.txt >&2 || true
echo "Falling back to npm install --package-lock-only" >&2

if command -v npm >/dev/null 2>&1; then
  (cd "$ROOT/edge-worker" && npm install --package-lock-only --ignore-scripts --no-audit --no-fund)
  if [[ -f "$DST" ]] && [[ $(wc -c < "$DST") -gt 1000 ]]; then
    echo "Generated package-lock.json via npm ($(wc -c < "$DST") bytes)"
    exit 0
  fi
fi
echo "Could not produce a usable package-lock.json" >&2
exit 1
