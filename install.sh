#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'HELP'
Usage: install.sh [--version vX.Y.Z] [--socket PATH] [--help]

Install codex-thread-bridge for Linux amd64 and register it with Codex.
Defaults to the latest stable release and the bridge's CODEX_HOME socket.
HELP
}
fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }
stable_version() { [[ $1 =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; }
version=''
socket=''
socket_set=false
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --version) (($# >= 2)) || fail '--version requires vX.Y.Z'; version=$2; stable_version "$version" || fail 'Version must be a stable vX.Y.Z tag'; shift 2 ;;
    --socket) (($# >= 2)) || fail '--socket requires a path'; [[ -n $2 ]] || fail 'Socket path cannot be empty'; socket=$2; socket_set=true; shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Only Linux amd64 is supported'
for command in curl codex; do command -v "$command" >/dev/null 2>&1 || fail "Required command is missing: $command"; done
if command -v sha256sum >/dev/null 2>&1; then
  checksum=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  checksum=(shasum -a 256)
else
  fail 'Required command is missing: sha256sum or shasum'
fi
[[ -n ${HOME:-} && $HOME == /* ]] || fail 'HOME must be an absolute path'
repo='https://github.com/JesseSenior/codex-thread-bridge'
if [[ -z $version ]]; then
  resolved=$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --output /dev/null --write-out '%{url_effective}' "$repo/releases/latest") || fail 'Cannot resolve the latest stable release'
  [[ $resolved == "$repo/releases/tag/"* ]] || fail 'Unexpected latest release URL'
  version=${resolved##*/}
  stable_version "$version" || fail 'Latest release does not have a stable vX.Y.Z tag'
fi
asset='codex-thread-bridge-linux-amd64'
work=$(mktemp -d)
staged=''
cleanup() { rm -rf -- "$work"; if [[ -n $staged ]]; then rm -f -- "$staged"; fi; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
base="$repo/releases/download/$version"
for name in "$asset" SHA256SUMS; do
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --output "$work/$name" "$base/$name" || fail "Cannot download $name for $version"
done
expected=''
while read -r hash name extra; do
  if [[ $name == "$asset" || $name == "*$asset" ]]; then
    [[ -z $expected && -z ${extra:-} && $hash =~ ^[[:xdigit:]]{64}$ ]] || fail 'Invalid checksum manifest'
    expected=$(printf '%s' "$hash" | tr 'A-F' 'a-f')
  fi
done < "$work/SHA256SUMS"
[[ -n $expected ]] || fail 'Binary checksum is missing'
actual=$("${checksum[@]}" "$work/$asset") || fail 'Cannot calculate binary checksum'
actual=${actual%% *}
[[ $actual == "$expected" ]] || fail 'Binary checksum does not match'
chmod 755 "$work/$asset"
actual_version=$("$work/$asset" --version) || fail 'Downloaded executable failed its version check'
[[ $actual_version == "${version#v}" ]] || fail 'Downloaded executable version does not match the release'
bin_dir="$HOME/.local/bin"
mkdir -p -- "$bin_dir"
bin_dir=$(cd -- "$bin_dir" && pwd -P)
target="$bin_dir/codex-thread-bridge"
[[ ! -d $target ]] || fail "Installation target is a directory: $target"
staged=$(mktemp "$bin_dir/.codex-thread-bridge.XXXXXX")
cp -- "$work/$asset" "$staged"
chmod 755 "$staged"
mv -fT -- "$staged" "$target"
staged=''
registration=(codex mcp add codex-thread-bridge -- "$target")
if "$socket_set"; then registration+=(--socket "$socket"); fi
if ! "${registration[@]}"; then
  printf 'Installed %s, but Codex registration failed. Run:\n' "$version" >&2
  printf '  ' >&2
  if [[ ${CODEX_HOME+x} ]]; then printf 'CODEX_HOME=%q ' "$CODEX_HOME" >&2; fi
  printf '%q ' "${registration[@]}" >&2
  printf '\n' >&2
  exit 1
fi
printf 'Installed %s at %s and registered codex-thread-bridge with Codex.\n' "$version" "$target"
