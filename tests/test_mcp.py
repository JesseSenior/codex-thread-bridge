import sys

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client


async def test_real_mcp_stdio_discovery_create_read_and_dedup(fake_server, tmp_path):
    fake, socket = fake_server
    params = StdioServerParameters(
        command=sys.executable,
        args=[
            "-m",
            "codex_thread_bridge.server",
            "--socket",
            str(socket),
            "--state-dir",
            str(tmp_path / "state"),
        ],
    )
    async with stdio_client(params) as (read, write), ClientSession(read, write) as session:
        await session.initialize()
        tools = (await session.list_tools()).tools
        assert {tool.name for tool in tools} == {
            "get_capabilities",
            "create_thread",
            "create_worktree_thread",
            "send_message_to_thread",
            "read_thread",
            "list_threads",
            "wait_thread",
            "get_goal",
            "get_operation",
        }
        caps = await session.call_tool("get_capabilities", {})
        assert not caps.isError
        assert caps.structuredContent["capabilities"]["desktopManagedWorktrees"] is False
        args = {"request_id": "mcp-create", "cwd": str(tmp_path), "prompt": "READY"}
        result = await session.call_tool("create_thread", args)
        assert not result.isError
        receipt = result.structuredContent
        assert receipt["status"] == "accepted"
        repeated = await session.call_tool("create_thread", args)
        assert repeated.structuredContent["replayed"]
        followup = await session.call_tool(
            "send_message_to_thread",
            {
                "request_id": "mcp-send",
                "thread_id": receipt["threadId"],
                "message": "FOLLOWUP",
            },
        )
        sent = followup.structuredContent
        assert sent["status"] == "accepted"
        waited = await session.call_tool(
            "wait_thread",
            {
                "thread_id": receipt["threadId"],
                "turn_id": sent["turnId"],
                "timeout_seconds": 0,
            },
        )
        assert waited.structuredContent["turn"]["items"][0]["text"] == "FOLLOWUP"
        history = await session.call_tool("read_thread", {"thread_id": receipt["threadId"]})
        assert len(history.structuredContent["turnsPage"]["data"]) == 2
        goal = await session.call_tool("get_goal", {"thread_id": receipt["threadId"]})
        assert goal.structuredContent["goal"] is None
        invalid = await session.call_tool("create_thread", {**args, "sandbox": "invalid"})
        assert invalid.isError
    assert fake.count("thread/start") == 1 and fake.count("turn/start") == 2


async def test_mcp_socket_alias_restart_does_not_repeat_creation(fake_server, tmp_path):
    fake, socket = fake_server
    alias = socket.parent / "alias.sock"
    alias.symlink_to(socket)
    receipts = []
    for path in [alias, socket]:
        params = StdioServerParameters(
            command=sys.executable,
            args=[
                "-m",
                "codex_thread_bridge.server",
                "--socket",
                str(path),
                "--state-dir",
                str(tmp_path / "state"),
            ],
        )
        async with stdio_client(params) as (read, write), ClientSession(read, write) as session:
            await session.initialize()
            result = await session.call_tool(
                "create_thread",
                {
                    "request_id": "stable-create",
                    "cwd": str(tmp_path),
                    "prompt": "READY",
                },
            )
            assert not result.isError
            receipts.append(result.structuredContent)
    assert receipts[0]["threadId"] == receipts[1]["threadId"]
    assert receipts[1]["replayed"]
    assert fake.count("thread/start") == 1 and fake.count("turn/start") == 1
