import os
import subprocess
import sys

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

TOOLS = {
    "create_thread",
    "send_message_to_thread",
    "list_projects",
    "list_threads",
    "list_archived_threads",
    "read_thread",
    "wait_threads",
    "set_thread_pinned",
    "set_thread_archived",
    "set_thread_title",
}


def process_params(socket, home):
    return StdioServerParameters(
        command=sys.executable,
        args=["-m", "codex_thread_bridge.server", "--socket", str(socket)],
        env={**os.environ, "CODEX_HOME": str(home), "XDG_STATE_HOME": str(home)},
    )


async def test_stdio_inventory_stateless_start_and_worktree_error(fake_server, tmp_path):
    fake, socket = fake_server
    home = tmp_path / "no-state"
    async with (
        stdio_client(process_params(socket, home)) as (read, write),
        ClientSession(read, write) as session,
    ):
        await session.initialize()
        tools = (await session.list_tools()).tools
        assert {t.name for t in tools} == TOOLS
        assert all("request_id" not in t.inputSchema.get("properties", {}) for t in tools)
        assert fake.calls == []
        result = await session.call_tool(
            "create_thread",
            {
                "cwd": str(tmp_path),
                "environment": {"type": "worktree"},
            },
        )
        assert result.isError and "Worktree support is not available" in result.content[0].text
        assert fake.calls == []
    assert not home.exists()


async def test_stdio_creation_pagination_metadata_and_restart_wait(fake_server, tmp_path):
    fake, socket = fake_server
    params = process_params(socket, tmp_path / "state")
    async with stdio_client(params) as (read, write), ClientSession(read, write) as session:
        await session.initialize()
        created = await session.call_tool(
            "create_thread", {"cwd": str(tmp_path), "prompt": "first"}
        )
        assert not created.isError
        tid = created.structuredContent["threadId"]
        await session.call_tool("send_message_to_thread", {"threadId": tid, "prompt": "second"})
        first = await session.call_tool("read_thread", {"threadId": tid, "turnLimit": 1})
        cursor = first.structuredContent["turnsPage"]["nextCursor"]
        second = await session.call_tool(
            "read_thread", {"threadId": tid, "turnLimit": 1, "cursor": cursor}
        )
        assert second.structuredContent["turnsPage"]["data"][0]["items"][0]["text"] == "first"
        for name, args in [
            ("set_thread_title", {"title": "Saved"}),
            ("set_thread_pinned", {"pinned": True}),
            ("set_thread_archived", {"archived": True}),
        ]:
            result = await session.call_tool(name, {"threadId": tid, **args})
            assert result.structuredContent["status"] == "accepted"
        archived = await session.call_tool("list_archived_threads", {})
        assert archived.structuredContent["data"][0]["name"] == "Saved"
        await session.call_tool("set_thread_archived", {"threadId": tid, "archived": False})
        wait = await session.call_tool(
            "wait_threads", {"targets": [{"threadId": tid}], "timeoutMs": 0}
        )
        cursor = wait.structuredContent["threads"][0]["cursor"]
    async with stdio_client(params) as (read, write), ClientSession(read, write) as session:
        await session.initialize()
        wait = await session.call_tool(
            "wait_threads",
            {
                "targets": [{"threadId": tid, "afterCursor": cursor}],
                "timeoutMs": 0,
            },
        )
        assert wait.structuredContent["threads"][0]["unchanged"]
        assert "turn" not in wait.structuredContent["threads"][0]
    assert fake.count("thread/start") == 1 and fake.count("turn/start") == 2
    assert not (tmp_path / "state").exists()


def test_old_state_option_is_removed_and_historical_file_is_untouched(tmp_path):
    ledger = tmp_path / "operations.sqlite3"
    ledger.write_bytes(b"historical data: do not read or change")
    result = subprocess.run(
        [sys.executable, "-m", "codex_thread_bridge.server", "serve", "--state-dir", str(tmp_path)],
        capture_output=True,
        text=True,
    )
    assert result.returncode != 0 and "unrecognized arguments" in result.stderr
    assert ledger.read_bytes() == b"historical data: do not read or change"
