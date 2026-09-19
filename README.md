# codex-thread-bridge

Version `0.3.0`.

A stateless MCP server for task management through an **existing App Server on
one remote host**. It continues to work when ChatGPT Desktop disconnects, provided
the App Server and MCP process remain running. It does not start an App Server,
patch Desktop, install a local companion, or keep a bridge database.

## Install and connect

```sh
curl -fsSL https://github.com/JesseSenior/codex-thread-bridge/releases/latest/download/install.sh | bash
```

The installer downloads the latest stable Linux amd64 executable, verifies its
SHA-256 checksum and version, installs it at `~/.local/bin/codex-thread-bridge`,
and registers its absolute path with `codex mcp add`. No Python, Go, or runtime
extraction is required. Run the same command to update or reinstall.

Requires Linux amd64, kernel 3.2 or later, Bash, curl, `sha256sum` or `shasum`,
and the Codex CLI on `PATH`. The executable has no glibc dependency. Run the
bridge as the same user on the same host as an authenticated App Server with a
Unix WebSocket control socket.

Select a release or a custom socket:

```sh
curl -fsSL https://github.com/JesseSenior/codex-thread-bridge/releases/latest/download/install.sh | bash -s -- --version v0.3.0
curl -fsSL https://github.com/JesseSenior/codex-thread-bridge/releases/latest/download/install.sh | bash -s -- --socket '/absolute/path/to/app.sock'
```

The installer honors `CODEX_HOME` and replaces the existing `codex-thread-bridge`
MCP entry. Pass `--socket` again when updating to retain an explicit override.
Installation does not require a running App Server. If registration fails, the
installer returns an error and prints a command to register the installed binary.

The default socket is `$CODEX_HOME/app-server-control/app-server-control.sock`.
`CODEX_HOME` defaults to `~/.codex`. `diagnose` is read-only and starts no task.
An unavailable server produces a connection error; the bridge never starts one.

```sh
~/.local/bin/codex-thread-bridge --help
~/.local/bin/codex-thread-bridge diagnose
```

Remove the MCP entry and executable:

```sh
codex mcp remove codex-thread-bridge
rm -- "$HOME/.local/bin/codex-thread-bridge"
```

For a source installation, install Go 1.27.0, clone this repository, and run:

```sh
go mod download
./scripts/build.sh
mkdir -p "$HOME/.local/bin"
install -m 755 dist/codex-thread-bridge-linux-amd64 "$HOME/.local/bin/codex-thread-bridge"
codex mcp add codex-thread-bridge -- "$HOME/.local/bin/codex-thread-bridge"
```

## Tools

All task IDs refer to this remote host. There is no implicit current task or
host-routing argument. Parameters use camelCase where they match Desktop.

| Tool | Behavior |
| --- | --- |
| `create_thread` | Create in an existing directory, optionally with a title and initial prompt |
| `send_message_to_thread` | Start a turn when idle; steer the current turn when running |
| `list_projects` | Read server project records; report when Desktop's saved catalog is required |
| `list_threads` | List unarchived tasks with pagination |
| `list_archived_threads` | List archived tasks with pagination |
| `read_thread` | Read task metadata and paginated history without resuming |
| `wait_threads` | Wait for up to eight tasks, with immediate snapshots and restart-safe cursors |
| `set_thread_pinned` | Pin or unpin through the server's section API |
| `set_thread_archived` | Archive or unarchive; reject tasks observed running |
| `set_thread_title` | Persist a task title through the server |

Forks and worktree operations are not supported. A creation request with
`environment: {"type": "worktree"}` fails explicitly before any mutation.
`environment` can be omitted or set to `{"type": "local"}`. The bridge does not
create, copy, register, or clean up worktrees.

## Examples

Create a task in an existing directory. Omitted settings use remote server
defaults; there is no implicit change to sandbox or approval policy.

```json
{
  "cwd": "/absolute/path/to/project",
  "title": "Inspect the parser",
  "prompt": "Read the parser and explain its input format."
}
```

Pass that object to `create_thread`. Optional overrides are `model`, `thinking`,
`sandbox`, `approvalPolicy`, and `permissions`. `sandbox` and `permissions` cannot
be combined. Keep the returned `threadId` and, if present, `turnId`.

Send a message with `send_message_to_thread`:

```json
{"threadId": "TASK_ID", "prompt": "Also check how empty input is handled."}
```

The bridge preserves the settings of a loaded task. For an unloaded task, it
reads the server-owned task log and restores verified settings before sending.
If settings are missing, unsupported, or do not match the resume response, it
withholds the message and reports an error. Custom permission profiles cannot
yet be verified through the tested server's legacy sandbox response; load those
tasks in Desktop before messaging. The bridge never writes task logs. Empty tasks may not appear in Desktop or
archive successfully until their first turn creates a rollout.

Read history with `read_thread`:

```json
{"threadId": "TASK_ID", "turnLimit": 10, "maxOutputCharsPerItem": 4000}
```

Pass the returned `turnsPage.nextCursor` as `cursor` for older turns. For task and
project lists, use the returned `nextCursor`. Cursors are opaque strings; copy
them unchanged, including JSON-shaped cursors. Display truncation is marked.
IDs, paths, and protocol cursors are not truncated.

Wait with `wait_threads`:

```json
{"targets": [{"threadId": "TASK_ID"}], "timeoutMs": 20000}
```

Use `timeoutMs: 0` for an immediate snapshot. Each task returns an outcome and a
cursor. Pass that cursor as the target's `afterCursor` to suppress unchanged turn
text, including after an MCP restart. The wait returns when any task completes,
fails, needs interaction, or the timeout expires. Per-task errors are returned
separately. Waits never interrupt or resume tasks. Approval and user-input flags
come from the server. Pending Desktop tool calls observed on this connection are
also reported; the tested server does not expose those calls through read-only
history while they are pending, so a fresh MCP connection may report a running
task until Desktop reconnects.

Manage saved metadata:

```json
{"threadId": "TASK_ID", "pinned": true}
```

Use `set_thread_pinned` for that object. `set_thread_archived` takes `archived`,
and `set_thread_title` takes `title`. Archive rejects a task observed running and
never sends an interrupt. Other clients can change task state between the check
and the archive request; the App Server remains authoritative.

## Failure and restart behavior

Mutations return `accepted`, `failed`, or `outcome_unknown`. An accepted prompt
has been dispatched; it has not necessarily completed. Failures retain actual
server errors and any known task and turn IDs. A task created before a later
naming or prompt error remains on the server.

After a timeout or connection loss, **do not resend automatically**. Inspect the
known task and its history first. The bridge has no receipts or deduplication
keys. Repeating a mutation is a new operation and can create another task or
send another message. A rejected steering race is returned without falling back
to a new turn. Read-only connection diagnostics are a CLI command, not an MCP tool.

Pending approvals, human input, and Desktop tools remain unanswered for Desktop
to handle after reconnect. The bridge does not reject, approve, or fabricate a
response. Closing the MCP connection does not cancel the remote task.

The server's project records can differ from Desktop's saved project catalog.
An empty server catalog produces an actionable Desktop-required error. The bridge
does not infer projects from directories, cache Desktop's catalog, or treat an
empty catalog as proof that Desktop is disconnected.

## Migration and compatibility

Version 0.3.0 replaces Python with Go and preserves the 0.2.0 CLI and MCP tools.
Existing wait cursors remain valid for the same socket path and task.

The earlier stateless interface was a breaking change from the ledger-based interface. Remove
`--state-dir` and mutation request IDs from client configuration. Rediscover the
MCP tool schemas. Old ledger files are left untouched; this version neither reads
nor requires them. Task state stays with the App Server. All bridge connection,
request, and wait state is held in memory.

The Python 0.2.0 compatibility checks used ChatGPT Desktop **26.915.31029 (build 9771)** on macOS
and App Server **0.154.0** on Linux. Tests used the existing server, without a
restart. Disposable tasks preserved pending Desktop tool, human-input, and
approval requests across client disconnections; Desktop completed the same
turns. Native Desktop task listing also recognized remote rename, pin, archive,
and unarchive results. Read-only, workspace-write, and built-in read-only profile
restoration were checked. Custom profiles and future protocol changes require
additional validation. No worktree or fork behavior is claimed. The Go port is checked against an
isolated fake App Server and Python reference fixtures. Go 0.3.0 also passed
read-only diagnostics, task listing, history reads, and immediate waits against
App Server 0.154.0 on Linux amd64 with kernel 5.15. Live mutation checks have
not been repeated for Go.

The server does not preserve all sandbox settings when an unloaded task is
resumed without overrides. This bridge restores saved settings and verifies the
response before sending a message. It fails explicitly when it cannot do so.
This is not a substitute for controlling permissions on the App Server itself.

## Development

```sh
./scripts/check.sh
./scripts/test-binary.sh
```

`check.sh` checks formatting, verifies modules, runs `go vet` and race tests,
and builds the static Linux amd64 executable. Race tests require a C compiler;
the release executable does not. `test-binary.sh` requires Docker and runs the
compiled MCP tests in a minimal container without libc and in Debian. Both use
a read-only root filesystem. Container tests do not test an old Linux kernel.

Tests use a fake App Server over a real Unix WebSocket and an MCP stdio client.
They do not start models or use existing user tasks. See [CONTRIBUTING.md](CONTRIBUTING.md).

GitHub Actions runs the checks on pushes and pull requests. A stable `vX.Y.Z`
tag publishes the executable, `SHA256SUMS`, and `install.sh` only after all
checks pass and the tag matches the project and runtime versions. Change the
single version constant in `internal/version/version.go` before tagging.

Independent project, not affiliated with or endorsed by OpenAI. MIT licensed.
