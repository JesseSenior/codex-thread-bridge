# codex-thread-bridge

An MCP server that manages tasks through an existing Codex App Server on the
same host. It can continue to manage tasks while Desktop is disconnected,
provided the App Server and bridge remain running.

Distributed as a single Linux amd64 executable. No Python or Go runtime is needed.

## Install

```sh
curl -fsSL https://github.com/JesseSenior/codex-thread-bridge/releases/latest/download/install.sh | bash
```

Requires Linux amd64 (kernel 3.2+), Bash, curl, `sha256sum` or `shasum`, and the
Codex CLI on `PATH`. Run as the same user as the authenticated App Server.

The installer verifies the download, installs it at
`~/.local/bin/codex-thread-bridge`, and registers it with Codex. Run the same
command to update.

To select a version or socket, add `--version` or `--socket`:

```sh
curl -fsSL https://github.com/JesseSenior/codex-thread-bridge/releases/latest/download/install.sh | bash -s -- --version v0.3.0 --socket /path/to/app.sock
```

The default socket is `$CODEX_HOME/app-server-control/app-server-control.sock`,
where `CODEX_HOME` defaults to `~/.codex`. Pass `--socket` again when updating
if you use a custom socket.

## Included tools

| Tool | Purpose |
| --- | --- |
| `create_thread` | Create a task in an existing directory |
| `send_message_to_thread` | Start a turn or steer the running turn |
| `list_projects` | List App Server project records |
| `list_threads` | List active and idle tasks |
| `list_archived_threads` | List archived tasks |
| `read_thread` | Read task details and history |
| `wait_threads` | Wait for up to eight tasks |
| `set_thread_pinned` | Pin or unpin a task |
| `set_thread_archived` | Archive or unarchive a task |
| `set_thread_title` | Rename a task |

All tools operate on this host. Forks and worktrees are not supported.
Approvals and requests for human input remain with Desktop.

## Use

Check the connection:

```sh
~/.local/bin/codex-thread-bridge diagnose
~/.local/bin/codex-thread-bridge --help
```

From your MCP client, call `create_thread` with:

```json
{"cwd": "/absolute/path/to/project", "prompt": "Read the parser and explain it."}
```

Use the returned `threadId` with `send_message_to_thread`:

```json
{"threadId": "TASK_ID", "prompt": "Also check how empty input is handled."}
```

Use `read_thread` to read the result, or `wait_threads` to wait:

```json
{"targets": [{"threadId": "TASK_ID"}], "timeoutMs": 20000}
```

When Codex supplies `_meta.threadId` in the MCP request, the bridge adds
Desktop-compatible source attribution to initial prompts and follow-up messages.
Desktop can then show “Sent by … from another task.” The caller ID is used only
for that request; it is not stored or read from the process environment. Clients
without this metadata send ordinary messages. Invalid caller IDs are rejected.
No additional tool argument is required.

Attributed turn starts use native tool output on servers at or above
`0.151.0-alpha.4`. Steering and older or unrecognized server versions use the
Desktop text wrapper. The bridge selects the format before delivery and never
retries a message with another format after failure.

`create_thread` and `send_message_to_thread` accept optional `model` and
`thinking` values. Explicit overrides must match the App Server model catalog.
Omitted settings retain server defaults on creation or saved task settings on
follow-up. If the effective combination cannot be checked, the request fails
before a change.

Follow-up overrides are saved through `thread/settings/update`. A running turn
receives the message through steering and keeps its current settings. The next
turn uses the saved settings. An idle task starts its next turn immediately.
Servers without settings-update support cannot accept follow-up overrides.

Follow-up results include `settingsUpdated` and `messageSent`: `true` means
confirmed, `false` means not applied or not sent, and `null` means unknown.
Settings can be saved even if message delivery fails. If an operation returns
`outcome_unknown`, inspect the task before sending it again.

`read_thread` returns `threadId`, `title`, `cwd`, `status`, `turns`, and
`nextCursor`. Each turn contains `id`, `status`, `messages`, `tools`, and an error
when present. Messages have a role and text; tools have identity, type, status,
and available command or tool details. Set `includeOutputs: true` to add tool
and command outputs. Outputs are excluded by default; raw reasoning is excluded
in both modes. Recognized delegation messages show the decoded prompt and
`sourceThreadId`, including when outputs are excluded. `maxOutputCharsPerItem`
limits displayed text (default 4000).

`wait_threads` returns `threads`, `errors`, and `timedOut`. Each task contains
status, outcome, pending interaction methods, and compact `progress` with the
turn ID/status, latest commentary, final text, and error when available. The
default timeout is 120000 ms; use zero for an immediate snapshot. Commentary
alone does not end a wait. Pass a returned cursor as `afterCursor` to suppress
previously delivered final text. Version-2 cursors remain valid across bridge
restarts and are specific to the server socket and task. Other cursor versions
are rejected.

All tools reject unknown input fields. Existing directory filters, pagination,
and creation permission options remain available.

To remove:

```sh
codex mcp remove codex-thread-bridge
rm -- "$HOME/.local/bin/codex-thread-bridge"
```

### Build from source

With Go 1.27.0 installed, run from this repository:

```sh
./scripts/build.sh
codex mcp add codex-thread-bridge -- "$PWD/dist/codex-thread-bridge-linux-amd64"
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development instructions. MIT licensed.
