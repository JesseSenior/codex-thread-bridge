ARG RUNTIME=scratch
FROM ${RUNTIME}
COPY codex-thread-bridge-linux-amd64 /app/codex-thread-bridge
COPY mcp.test /app/mcp.test
WORKDIR /readonly
ENV CTB_TEST_BINARY=/app/codex-thread-bridge CTB_READONLY_HOME=/readonly TMPDIR=/work HOME=/readonly
ENTRYPOINT ["/app/mcp.test", "-test.v"]
