#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 go build -trimpath -ldflags='-s -w' -o dist/codex-thread-bridge-linux-amd64 ./cmd/codex-thread-bridge
cp install.sh dist/install.sh
if command -v sha256sum >/dev/null 2>&1; then
  (cd dist && sha256sum codex-thread-bridge-linux-amd64 install.sh > SHA256SUMS)
else
  (cd dist && shasum -a 256 codex-thread-bridge-linux-amd64 install.sh > SHA256SUMS)
fi
