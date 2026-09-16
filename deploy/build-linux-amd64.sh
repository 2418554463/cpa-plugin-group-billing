#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_dir"
go_binary="${CPA_BUILD_GO:-go}"
if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  if [[ -z "${CPA_BUILD_ZIG:-}" ]]; then
    echo 'Cross compilation requires CPA_BUILD_ZIG=/absolute/path/to/zig.' >&2
    exit 1
  fi
  export CC="$CPA_BUILD_ZIG cc -target x86_64-linux-gnu.2.17"
fi
mkdir -p dist/linux-amd64
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 "$go_binary" build \
  -trimpath -buildvcs=false -tags cshared -buildmode=c-shared -ldflags='-s -w' \
  -o dist/linux-amd64/cpa-key-billing.so ./cmd/cpa-key-billing
echo "Built: $repo_dir/dist/linux-amd64/cpa-key-billing.so"
