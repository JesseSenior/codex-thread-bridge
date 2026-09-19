import asyncio
import json

import pytest

from codex_thread_bridge.bridge import PINNED_SECTION_ID, Bridge
from codex_thread_bridge.rpc import AppServer, RpcError


async def test_defaults_and_exact_prompt(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    result = await bridge.create_thread(str(tmp_path), prompt="  exact\nmessage  ", title="Example")
    assert result["status"] == "accepted"
    assert result["creation"]["model"] == "server-default"
    assert next(p for m, p in fake.calls if m == "thread/start") == {
        "cwd": str(tmp_path.resolve()),
        "ephemeral": False,
    }
    tid = result["threadId"]
    assert fake.threads[tid]["turns"][0]["items"][0]["text"] == "  exact\nmessage  "
    assert fake.threads[tid]["name"] == "Example"
    second = await bridge.send_message_to_thread(tid, "next")
    assert second["delivery"] == "started" and second["turnId"] == "turn-2"
    assert fake.count("thread/resume") == 0
    assert next(p for m, p in fake.calls if m == "turn/start").keys() == {"threadId", "input"}


async def test_empty_creation_and_explicit_overrides(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    result = await bridge.create_thread(
        str(tmp_path),
        model="other",
        thinking="low",
        sandbox="read-only",
        approvalPolicy="untrusted",
    )
    assert result["status"] == "accepted" and "turnId" not in result
    assert fake.count("turn/start") == 0
    assert result["creation"]["approvalPolicy"] == "untrusted"
    assert result["creation"]["reasoningEffort"] == "low"


async def test_steer_and_completion_race_never_duplicate(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    fake.complete_turns = False
    result = await bridge.create_thread(str(tmp_path), prompt="start")
    tid = result["threadId"]
    assert (await bridge.send_message_to_thread(tid, "steer"))["delivery"] == "steered"
    assert fake.count("turn/start") == 1

    def complete_before_steer(method, params):
        if method == "turn/steer":
            fake.threads[tid]["status"] = {"type": "idle"}
            fake.threads[tid]["turns"][-1]["status"] = "completed"

    fake.before = complete_before_steer
    raced = await bridge.send_message_to_thread(tid, "race")
    assert raced["status"] == "failed" and raced["rpcError"]["code"] == -32600
    assert fake.count("turn/start") == 1 and fake.count("turn/steer") == 2


async def test_idle_start_race_is_reported_without_steering(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path)

    def activate(method, params):
        if method == "turn/start":
            fake.threads[tid]["status"] = {"type": "active"}

    fake.before = activate
    result = await bridge.send_message_to_thread(tid, "race")
    assert result["status"] == "failed"
    assert fake.count("turn/start") == 1 and fake.count("turn/steer") == 0


@pytest.mark.parametrize("method", ["thread/start", "thread/name/set", "turn/start"])
async def test_creation_response_loss_retains_known_ids(bridge, fake_server, tmp_path, method):
    fake, _ = fake_server
    fake.drop_after = method
    result = await bridge.create_thread(str(tmp_path), prompt="only once", title="Retained")
    assert result["status"] == "outcome_unknown"
    assert fake.count(method) == 1
    assert ("threadId" in result) == (method != "thread/start")
    assert len(fake.threads) == 1


async def test_partial_creation_preserves_server_error(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    fake.reject["thread/name/set"] = {
        "code": -42,
        "message": "name rejected",
        "data": {"detail": 1},
    }
    result = await bridge.create_thread(str(tmp_path), title="Rejected", prompt="not sent")
    assert result["status"] == "failed" and result["threadId"] in fake.threads
    assert result["rpcError"] == fake.reject["thread/name/set"]
    assert fake.count("turn/start") == 0


async def test_archive_rejects_running_and_metadata_persists(bridge, fake_server, tmp_path):
    fake, path = fake_server
    tid = fake.add_thread(tmp_path, status={"type": "active"})
    assert (await bridge.set_thread_archived(tid, True))["status"] == "failed"
    assert fake.count("thread/archive") == 0 and fake.count("turn/interrupt") == 0
    fake.threads[tid]["status"] = {"type": "idle"}
    await bridge.set_thread_title(tid, "Saved")
    await bridge.set_thread_pinned(tid, True)
    await bridge.set_thread_archived(tid, True)
    client = AppServer(path)
    try:
        fresh = Bridge(client)
        assert (await fresh.list_threads(archived=True))["data"][0]["name"] == "Saved"
        assert (await fresh.list_threads())["data"] == []
        assert fake.threads[tid]["section"] == PINNED_SECTION_ID
        await fresh.set_thread_pinned(tid, False)
        await fresh.set_thread_archived(tid, False)
        assert (await fresh.list_threads())["data"][0]["id"] == tid
        assert fake.threads[tid]["section"] is None
    finally:
        await client.close()


@pytest.mark.parametrize(
    "operation,method,args",
    [
        ("set_thread_title", "thread/name/set", ["title"]),
        ("set_thread_pinned", "thread/section/move", [True]),
        ("set_thread_archived", "thread/archive", [True]),
        ("set_thread_archived", "thread/unarchive", [False]),
        ("send_message_to_thread", "turn/start", ["message"]),
    ],
)
async def test_mutation_loss_has_no_replay(bridge, fake_server, tmp_path, operation, method, args):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path)
    fake.drop_after = method
    result = await getattr(bridge, operation)(tid, *args)
    assert result["status"] == "outcome_unknown" and result["threadId"] == tid
    assert fake.count(method) == 1


async def test_paginated_reads_preserve_opaque_cursors(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    for _ in range(3):
        await bridge.create_thread(str(tmp_path), prompt="x" * 300)
    first = await bridge.list_threads(limit=1)
    second = await bridge.list_threads(limit=1, cursor=first["nextCursor"])
    assert first["data"][0]["id"] != second["data"][0]["id"]
    tid = first["data"][0]["id"]
    await bridge.send_message_to_thread(tid, "latest")
    page = await bridge.read_thread(tid, turnLimit=1, maxOutputCharsPerItem=100)
    older = await bridge.read_thread(
        tid, turnLimit=1, cursor=page["turnsPage"]["nextCursor"], maxOutputCharsPerItem=100
    )
    assert older["turnsPage"]["data"][0]["items"][0]["text"].endswith(
        "[truncated; original length 300 characters]"
    )
    assert fake.count("thread/resume") == 0


async def test_projects_missing_or_paginated(bridge, fake_server):
    fake, _ = fake_server
    with pytest.raises(ValueError, match="Desktop required"):
        await bridge.list_projects()
    fake.projects = [{"id": "project1"}, {"id": "project2"}]
    first = await bridge.list_projects(limit=1)
    assert (await bridge.list_projects(limit=1, cursor=first["nextCursor"]))["data"] == [
        {"id": "project2"}
    ]
    fake.reject["project/list"] = {"code": -1, "message": "unavailable"}
    with pytest.raises(RpcError, match="unavailable"):
        await bridge.list_projects()


async def test_multi_wait_and_restart_cursors(bridge, fake_server, tmp_path):
    fake, path = fake_server
    first = await bridge.create_thread(str(tmp_path), prompt="complete")
    tid = first["threadId"]
    other = fake.add_thread(
        tmp_path, status={"type": "active", "activeFlags": ["waitingOnApproval"]}
    )
    waited = await bridge.wait_threads([{"threadId": tid}, {"threadId": other}], 0)
    assert [t["outcome"] for t in waited["threads"]] == ["completed", "interaction_required"]
    targets = [{"threadId": t["threadId"], "afterCursor": t["cursor"]} for t in waited["threads"]]
    client = AppServer(path)
    try:
        again = await Bridge(client).wait_threads(targets, 1)
        assert again["timedOut"]
        assert all(t["unchanged"] and "turn" not in t for t in again["threads"])
        fake.threads[other]["status"] = {"type": "idle"}
        changed = await Bridge(client).wait_threads(targets, 0)
        assert changed["threads"][1]["outcome"] == "completed"
    finally:
        await client.close()
    assert fake.count("thread/resume") == 0


async def test_wait_deadline_errors_and_cursor_validation(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path, status={"type": "active"})
    result = await bridge.wait_threads([{"threadId": tid}], 1)
    assert result["timedOut"] and result["threads"][0]["outcome"] == "running"
    missing = await bridge.wait_threads([{"threadId": "missing"}], 0)
    assert missing["errors"][0]["rpcError"]["code"] == -32602
    for targets in [
        [],
        [{"threadId": tid}] * 9,
        [{"threadId": tid}, {"threadId": tid}],
        [{"threadId": tid, "afterCursor": "bad"}],
    ]:
        with pytest.raises(ValueError):
            await bridge.wait_threads(targets, 0)
    with pytest.raises(ValueError):
        await bridge.wait_threads(
            [{"threadId": "other", "afterCursor": result["threads"][0]["cursor"]}], 0
        )


async def test_wait_observes_completion_during_wait(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path, status={"type": "active"})

    async def finish():
        await asyncio.sleep(0.01)
        fake.threads[tid]["status"] = {"type": "idle"}

    completion = asyncio.create_task(finish())
    result = await bridge.wait_threads([{"threadId": tid}], 1000)
    await completion
    assert result["threads"][0]["outcome"] == "completed" and not result["timedOut"]


def record_context(tmp_path, tid, **overrides):
    path = tmp_path / "rollout.jsonl"
    context = {
        "cwd": str(tmp_path),
        "model": "saved-model",
        "effort": "low",
        "approval_policy": "on-request",
        "approvals_reviewer": "user",
        "sandbox_policy": {"type": "read-only"},
        "workspace_roots": [str(tmp_path)],
        **overrides,
    }
    path.write_text(
        json.dumps({"type": "session_meta", "payload": {"id": tid}})
        + "\n"
        + json.dumps({"type": "turn_context", "payload": context})
        + "\n"
    )
    return str(path)


async def test_unloaded_messaging_restores_permissions(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path, status={"type": "notLoaded"})
    fake.threads[tid]["path"] = record_context(tmp_path, tid)
    result = await bridge.send_message_to_thread(tid, "resume safely")
    assert result["status"] == "accepted"
    params = next(p for m, p in fake.calls if m == "thread/resume")
    assert params["sandbox"] == "read-only" and params["approvalPolicy"] == "on-request"
    assert params["model"] == "saved-model" and params["config"]["model_reasoning_effort"] == "low"


async def test_permission_mismatch_or_missing_context_withholds_message(
    bridge, fake_server, tmp_path
):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path, status={"type": "notLoaded"})
    result = await bridge.send_message_to_thread(tid, "not sent")
    assert result["status"] == "failed" and fake.count("thread/resume") == 0
    fake.threads[tid]["path"] = record_context(tmp_path, tid)
    fake.override_resume = {"sandbox": {"type": "dangerFullAccess"}}
    result = await bridge.send_message_to_thread(tid, "not sent")
    assert result["status"] == "failed" and result["messageSent"] is False
    assert fake.count("turn/start") == 0


async def test_resume_response_loss_does_not_start_turn(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path, status={"type": "notLoaded"})
    fake.threads[tid]["path"] = record_context(tmp_path, tid)
    fake.drop_after = "thread/resume"
    result = await bridge.send_message_to_thread(tid, "not sent")
    assert result["status"] == "outcome_unknown" and result["threadId"] == tid
    assert fake.count("thread/resume") == 1 and fake.count("turn/start") == 0


async def test_unavailable_app_server_is_not_an_unknown_delivery(tmp_path):
    rpc = AppServer(tmp_path / "missing.sock", timeout=0.01)
    result = await Bridge(rpc).create_thread(str(tmp_path), prompt="not sent")
    assert result["status"] == "failed" and "was not sent" in result["error"]
    assert "threadId" not in result
    await rpc.close()


async def test_steer_loss_retains_turn_id(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    fake.complete_turns = False
    created = await bridge.create_thread(str(tmp_path), prompt="start")
    fake.drop_after = "turn/steer"
    result = await bridge.send_message_to_thread(created["threadId"], "steer once")
    assert result["status"] == "outcome_unknown" and result["turnId"] == created["turnId"]
    assert fake.count("turn/steer") == 1 and fake.count("turn/start") == 1


@pytest.mark.parametrize("flag", ["waitingOnUserInput", "waitingOnApproval"])
async def test_wait_reports_interaction_flags(bridge, fake_server, tmp_path, flag):
    fake, _ = fake_server
    tid = fake.add_thread(tmp_path, status={"type": "active", "activeFlags": [flag]})
    result = await bridge.wait_threads([{"threadId": tid}], 0)
    assert result["threads"][0]["outcome"] == "interaction_required"


@pytest.mark.parametrize("status", ["failed", "interrupted"])
async def test_wait_reports_terminal_errors(bridge, fake_server, tmp_path, status):
    fake, _ = fake_server
    tid = fake.add_thread(
        tmp_path,
        turns=[{"id": "turn1", "status": status, "items": [], "error": {"message": "stopped"}}],
    )
    result = await bridge.wait_threads([{"threadId": tid}], 0)
    assert result["threads"][0]["outcome"] == status
    assert result["threads"][0]["turn"]["error"]["message"] == "stopped"


async def test_wait_does_not_report_previous_failure_while_new_turn_starts(
    bridge, fake_server, tmp_path
):
    fake, _ = fake_server
    tid = fake.add_thread(
        tmp_path, status={"type": "active"}, turns=[{"id": "old", "status": "failed", "items": []}]
    )
    result = await bridge.wait_threads([{"threadId": tid}], 0)
    assert result["threads"][0]["outcome"] == "running"


async def test_missing_rollout_file_withholds_message(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    tid = fake.add_thread(
        tmp_path, status={"type": "notLoaded"}, path=str(tmp_path / "missing.jsonl")
    )
    result = await bridge.send_message_to_thread(tid, "not sent")
    assert result["status"] == "failed" and "Cannot read saved task settings" in result["error"]
    assert fake.count("thread/resume") == 0 and fake.count("turn/start") == 0
