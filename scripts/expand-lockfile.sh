#!/usr/bin/env bash
set -euo pipefail
export ROOT="$(cd "$(dirname "$0")/.." && pwd)"
python3 -c "
import base64, gzip, os
from pathlib import Path
root = Path(os.environ['ROOT'])
src = root / 'edge-worker' / 'package-lock.json.gz.b64'
dst = root / 'edge-worker' / 'package-lock.json'
data = ''.join(src.read_text().split())
dst.write_bytes(gzip.decompress(base64.b64decode(data)))
print(f'Wrote {dst} ({dst.stat().st_size} bytes)')
"
