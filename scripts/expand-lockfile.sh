#!/usr/bin/env bash
set -euo pipefail
export ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export DST="$ROOT/edge-worker/package-lock.json"
if [[ -f "$DST" ]] && [[ $(wc -c < "$DST") -gt 1000 ]]; then
  echo "package-lock.json already present ($(wc -c < "$DST") bytes)"
  exit 0
fi
for i in 0 1 2 3; do
  if [[ ! -f "$ROOT/edge-worker/package-lock.part${i}.b64" ]]; then
    echo "Missing package-lock.part${i}.b64" >&2
    exit 1
  fi
done
python3 -c '
import base64, gzip, os
from pathlib import Path
root = Path(os.environ["ROOT"])
parts = [root / ("edge-worker/package-lock.part%d.b64" % i) for i in range(4)]
data = "".join(p.read_text().strip() for p in parts)
dst = Path(os.environ["DST"])
dst.write_bytes(gzip.decompress(base64.b64decode(data)))
print("Wrote %s (%d bytes)" % (dst, dst.stat().st_size))
'
