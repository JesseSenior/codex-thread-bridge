#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
tag=${1:-}
[[ $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo 'Release tag must be stable vX.Y.Z' >&2; exit 1; }
version=$(sed -n 's/^const Version = "\([^"]*\)"/\1/p' internal/version/version.go)
[[ $tag == "v$version" ]] || { echo 'Tag and project version differ' >&2; exit 1; }
[[ $(dist/codex-thread-bridge-linux-amd64 --version) == "$version" ]] || { echo 'Binary and project version differ' >&2; exit 1; }
