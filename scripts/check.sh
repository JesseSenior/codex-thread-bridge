#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
files=$(gofmt -l cmd internal)
if [[ -n $files ]]; then printf 'Run gofmt on:\n%s\n' "$files" >&2; exit 1; fi
go mod verify
go vet ./...
go test -race ./...
./scripts/build.sh
CTB_RELEASE_BINARY="$PWD/dist/codex-thread-bridge-linux-amd64" go test ./internal/binarycheck
