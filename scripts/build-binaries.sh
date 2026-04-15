#!/usr/bin/env bash
# Cross-compile trd for all supported targets into bin/.
set -euo pipefail

cd "$(dirname "$0")/.."
mkdir -p bin

targets=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
)

for t in "${targets[@]}"; do
  os="${t%/*}"
  arch="${t#*/}"
  out="bin/trd-${os}-${arch}"
  echo "building $out"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$out" ./cmd/trd
done

echo "done. binaries:"
ls -lh bin/trd-*
