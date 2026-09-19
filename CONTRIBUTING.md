# Contributing

Keep the bridge independent of a particular repository, Desktop installation,
skill collection, or user's paths. Do not add a bridge database or a companion
process. Do not read or migrate historical ledger files.

- `rpc.py`: JSON-RPC over the existing Unix WebSocket, with unanswered server requests.
- `settings.py`: read server-owned task settings and verify restored permissions.
- `bridge.py`: task operations, partial failures, pagination, and stateless waits.
- `server.py`: ten MCP tools and read-only CLI diagnostics.

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
