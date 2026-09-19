#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 go test -c -o dist/mcp.test ./internal/mcpserver
for runtime in scratch debian:bookworm-slim; do
  label=${runtime//[:\/]/-}
  docker build --platform linux/amd64 --build-arg "RUNTIME=$runtime" -f packaging/Smoke.Dockerfile -t "ctb-smoke:$label" dist
  docker run --rm --platform linux/amd64 --read-only --network none --tmpfs /work:rw,exec,mode=1777 --user 65534:65534 "ctb-smoke:$label"
done
