# Contributing

Keep the bridge independent of a particular repository, Desktop installation,
skill collection, or user's paths. Do not add a bridge database or a companion
process. Do not read or migrate historical ledger files.

- `internal/rpc`: JSON-RPC over the existing Unix WebSocket, with unanswered server requests.
- `internal/settings`: read server-owned task settings and verify restored permissions.
- `internal/bridge`: task operations, partial failures, pagination, and stateless waits.
- `internal/mcpserver` and `cmd/codex-thread-bridge`: ten MCP tools and read-only CLI diagnostics.

Use the development commands in the README. Test mutation response loss at each
step, retained identifiers after partial success, and delivery races without
replay. Test cursors across MCP restarts. Worktree requests must fail before RPC
calls or filesystem changes. Do not add fork support.

Use a fake server for automated tests. Live checks must use disposable tasks and
must not restart the shared App Server or interrupt existing user tasks. Desktop
disconnection and answers to human requests remain user-controlled. Keep personal
paths, credentials, transcripts, and task IDs out of commits.

Report the bridge and server versions, failed method, actual server error, and
known artifact IDs when diagnosing a failure. Do not infer Desktop project
membership from directory paths or backend project IDs.

Use Go 1.27.0 and the committed module versions. Run `./scripts/check.sh` before
submitting a change and `./scripts/test-binary.sh` for binary or transport changes.
Release builds must use `CGO_ENABLED=0`, Linux amd64, and baseline `GOAMD64=v1`.

The JSON fixtures under the MCP and bridge test directories were captured from
Python 0.2.0 before the migration. Keep them as compatibility references. The Go
SDK emits the default `idempotentHint: false` explicitly; this is equivalent to
the omitted Python hint. Tool validation must retain the supported Python scalar
conversions. Wait hashes use Python-compatible sorted JSON with ASCII escaping.

The installer tests use mock downloads and an isolated Codex configuration. Do
not use the real user configuration in tests. Release checks do not create tags
or publish releases locally.
