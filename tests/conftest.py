import asyncio
import copy
import json
import tempfile
from pathlib import Path

import pytest
from websockets.asyncio.server import unix_serve

from codex_thread_bridge.bridge import Bridge
from codex_thread_bridge.rpc import AppServer, RpcError


class FakeServer:
    def __init__(self):
        self.calls = []
        self.threads = {}
        self.settings = {}
        self.reject = {}
        self.drop_after = None
        self.complete_turns = True
        self.handshake_extensions = []
        self.pause_after = None
        self.paused = asyncio.Event()
        self.release = asyncio.Event()
        self.projects = []
        self.before = None
        self.override_resume = {}

    def cursor(self, offset):
        return json.dumps({"offset": offset, "scope": {"kind": "opaque"}})

    def offset(self, cursor):
        return 0 if not cursor else json.loads(cursor)["offset"]

    def page(self, rows, params):
        start = self.offset(params.get("cursor"))
        end = start + params["limit"]
        return {
            "data": copy.deepcopy(rows[start:end]),
            "nextCursor": self.cursor(end) if end < len(rows) else None,
        }

    def add_thread(self, cwd, **fields):
        tid = f"thread-{len(self.threads) + 1}"
        self.threads[tid] = {
            "id": tid,
            "cwd": str(cwd),
            "status": {"type": "idle"},
            "turns": [],
            "archived": False,
            **fields,
        }
        return tid

    @staticmethod
    def sandbox(params):
        kind = params.get("sandbox", "workspace-write")
        if params.get("permissions") == ":read-only":
            kind = "read-only"
        if kind == "read-only":
            return {"type": "readOnly", "networkAccess": False}
        if kind == "danger-full-access":
            return {"type": "dangerFullAccess"}
        config = params.get("config", {}).get("sandbox_workspace_write", {})
        return {
            "type": "workspaceWrite",
            "writableRoots": config.get("writable_roots", []),
            "networkAccess": config.get("network_access", False),
            "excludeTmpdirEnvVar": config.get("exclude_tmpdir_env_var", False),
            "excludeSlashTmp": config.get("exclude_slash_tmp", False),
        }

    def dispatch(self, method, p):
        if method == "thread/start":
            tid = self.add_thread(p["cwd"])
            self.settings[tid] = {
                "cwd": p["cwd"],
                "model": p.get("model", "server-default"),
                "reasoningEffort": p.get("config", {}).get("model_reasoning_effort", "medium"),
                "sandbox": self.sandbox(p),
                "approvalPolicy": p.get("approvalPolicy", "on-request"),
            }
            return {"thread": copy.deepcopy(self.threads[tid]), **self.settings[tid]}
        if method == "project/list":
            return self.page(self.projects, p)
        if method == "thread/list":
            return self.page(
                [
                    dict(t, turns=[])
                    for t in self.threads.values()
                    if t["archived"] == p.get("archived", False)
                    and ("cwd" not in p or p["cwd"] == t["cwd"])
                ],
                p,
            )
        if method == "thread/goal/get":
            return {"goal": None}  # Transport-only fixture; not a public bridge tool.
        tid = p["threadId"]
        if tid not in self.threads:
            raise RpcError(method, {"code": -32602, "message": "task does not exist"})
        thread = self.threads[tid]
        if method == "thread/read":
            return {"thread": {**copy.deepcopy(thread), "turns": []}}
        if method == "thread/turns/list":
            return self.page(list(reversed(thread["turns"])), p)
        if method == "thread/resume":
            thread["status"] = {"type": "idle"}
            return {
                "thread": copy.deepcopy(thread),
                "cwd": thread["cwd"],
                "sandbox": self.sandbox(p),
                "approvalPolicy": p.get("approvalPolicy"),
                "model": p.get("model"),
                "reasoningEffort": p.get("config", {}).get("model_reasoning_effort"),
                "runtimeWorkspaceRoots": p.get("runtimeWorkspaceRoots", []),
                "approvalsReviewer": p.get("approvalsReviewer"),
                "activePermissionProfile": {"id": p["permissions"]} if "permissions" in p else None,
                **self.override_resume,
            }
        if method == "turn/start":
            if thread["status"]["type"] == "active":
                raise RpcError(method, {"code": -32600, "message": "task already active"})
            turn = {
                "id": f"turn-{len(thread['turns']) + 1}",
                "status": "completed" if self.complete_turns else "inProgress",
                "items": [{"type": "agentMessage", "text": p["input"][0]["text"]}],
            }
            thread["turns"].append(turn)
            thread["status"] = {"type": "idle" if self.complete_turns else "active"}
            return {"turn": copy.deepcopy(turn)}
        if method == "turn/steer":
            if (
                thread["status"]["type"] != "active"
                or thread["turns"][-1]["id"] != p["expectedTurnId"]
            ):
                raise RpcError(method, {"code": -32600, "message": "turn no longer active"})
            return {"turnId": p["expectedTurnId"]}
        if method == "thread/name/set":
            thread["name"] = p["name"]
        elif method == "thread/section/move":
            thread["section"] = p["sectionId"]
        elif method in {"thread/archive", "thread/unarchive"}:
            thread["archived"] = method == "thread/archive"
        else:
            raise RpcError(method, {"code": -32601, "message": "unsupported"})
        return {}

    async def handle(self, ws):
        self.handshake_extensions.append(ws.request.headers.get("Sec-WebSocket-Extensions"))
        async for raw in ws:
            message = json.loads(raw)
            if "method" not in message:
                continue
            method, params = message["method"], message.get("params", {})
            self.calls.append((method, params))
            if method == "initialized":
                continue
            ident = message["id"]
            if self.before:
                self.before(method, params)
            try:
                if method in self.reject:
                    raise RpcError(method, self.reject[method])
                result = (
                    {"userAgent": "fake/0.154.0"}
                    if method == "initialize"
                    else self.dispatch(method, params)
                )
            except RpcError as error:
                await ws.send(json.dumps({"id": ident, "error": error.error}))
                continue
            if method == self.pause_after:
                self.paused.set()
                await self.release.wait()
            await ws.send(json.dumps({"method": "thread/status/changed", "params": {}}))
            if method == self.drop_after:
                await ws.close()
                return
            await ws.send(json.dumps({"id": ident, "result": result}))

    def count(self, method):
        return sum(name == method for name, _ in self.calls)


@pytest.fixture
async def fake_server():
    with tempfile.TemporaryDirectory(prefix="ctb-") as directory:
        path = Path(directory) / "app.sock"
        fake = FakeServer()
        async with unix_serve(fake.handle, str(path)):
            yield fake, path


@pytest.fixture
async def bridge(fake_server):
    _, socket = fake_server
    rpc = AppServer(socket, timeout=0.5)
    try:
        yield Bridge(rpc)
    finally:
        await rpc.close()
