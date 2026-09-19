"""Stateless task operations against one existing remote App Server."""

import asyncio
import base64
import hashlib
import json
from pathlib import Path

from .rpc import AppServer, RpcError, TransportError
from .settings import SettingsError, saved_settings, verify_settings

PINNED_SECTION_ID = "01984de2-8f74-7c91-a3b2-5c5e937cf318"
DISPLAY_FIELDS = frozenset({"text", "preview", "summary", "objective", "aggregatedOutput"})


def nonempty(value, name, maximum=100_000):
    if not isinstance(value, str) or not value.strip() or len(value) > maximum:
        raise ValueError(f"{name} must contain 1–{maximum} characters")


def page_size(limit):
    if not 1 <= limit <= 100:
        raise ValueError("limit must be between 1 and 100")


def existing_directory(cwd):
    path = Path(cwd)
    if not path.is_absolute() or not path.is_dir():
        raise ValueError("cwd must be an existing absolute directory on this remote host")
    return str(path.resolve())


def clipped(value, limit, *, display_text=False):
    if display_text and isinstance(value, str) and len(value) > limit:
        return value[:limit] + f"\n[truncated; original length {len(value)} characters]"
    if isinstance(value, list):
        return [clipped(item, limit, display_text=display_text) for item in value]
    if isinstance(value, dict):
        return {k: clipped(v, limit, display_text=k in DISPLAY_FIELDS) for k, v in value.items()}
    return value


class Bridge:
    def __init__(self, rpc: AppServer):
        self.rpc = rpc
        self._mutation_lock = asyncio.Lock()

    async def diagnostics(self):
        await self.rpc.connect()
        return {"server": self.rpc.info, "socket": str(self.rpc.socket_path), "stateless": True}

    async def _mutate(self, action, **known):
        result = {"status": "accepted", **known}
        async with self._mutation_lock:

            async def call(method, params):
                result["method"] = method
                return await self.rpc.call(method, params)

            try:
                await action(call, result)
            except RpcError as error:
                result.update(status="failed", error=str(error), rpcError=error.error)
            except TransportError as error:
                result.update(
                    status="outcome_unknown" if error.may_have_been_sent else "failed",
                    error=str(error),
                    recovery="Do not resend automatically. Inspect the task and its history.",
                )
            except SettingsError as error:
                result.update(status="failed", error=str(error), messageSent=False)
        return result

    async def create_thread(
        self,
        cwd,
        prompt=None,
        title=None,
        environment=None,
        model=None,
        thinking=None,
        sandbox=None,
        approvalPolicy=None,
        permissions=None,
    ):
        if environment is not None:
            if environment.get("type") == "worktree":
                raise ValueError("Worktree support is not available. Use an existing directory.")
            if environment != {"type": "local"}:
                raise ValueError("Only environment {type: local} is supported")
        directory = await asyncio.to_thread(existing_directory, cwd)
        for key, value in {"prompt": prompt, "title": title, "model": model}.items():
            if value is not None:
                nonempty(value, key)
        if sandbox is not None and permissions is not None:
            raise ValueError("sandbox and permissions cannot be combined")
        params = {"cwd": directory, "ephemeral": False}
        for key, value in {
            "model": model,
            "sandbox": sandbox,
            "approvalPolicy": approvalPolicy,
            "permissions": permissions,
        }.items():
            if value is not None:
                params[key] = value
        if thinking is not None:
            params["config"] = {"model_reasoning_effort": thinking}

        async def action(call, result):
            creation = await call("thread/start", params)
            tid = creation["thread"]["id"]
            result.update(threadId=tid, creation=creation)
            expected = {k: params[k] for k in ("cwd", "model", "approvalPolicy") if k in params}
            if any(creation.get(k) != value for k, value in expected.items()):
                raise SettingsError("Created settings differ from the request; no prompt was sent")
            if sandbox is not None:
                sandbox_type = {
                    "read-only": "readOnly",
                    "workspace-write": "workspaceWrite",
                    "danger-full-access": "dangerFullAccess",
                }[sandbox]
                if creation.get("sandbox", {}).get("type") != sandbox_type:
                    raise SettingsError("Created sandbox differs; no prompt was sent")
            if permissions is not None:
                if (creation.get("activePermissionProfile") or {}).get("id") != permissions:
                    raise SettingsError("Created permission profile differs; no prompt was sent")
            if thinking is not None and creation.get("reasoningEffort") != thinking:
                raise SettingsError("Created reasoning effort differs; no prompt was sent")
            if title is not None:
                await call("thread/name/set", {"threadId": tid, "name": title})
            if prompt is not None:
                turn = await call("turn/start", {"threadId": tid, "input": self._input(prompt)})
                result["turnId"] = turn["turn"]["id"]

        return await self._mutate(action)

    @staticmethod
    def _input(prompt):
        return [{"type": "text", "text": prompt}]

    async def _latest(self, thread_id, items="summary"):
        page = await self.rpc.call(
            "thread/turns/list",
            {
                "threadId": thread_id,
                "limit": 1,
                "itemsView": items,
            },
        )
        return next(iter(page["data"]), None)

    async def send_message_to_thread(self, threadId, prompt):
        nonempty(threadId, "threadId", 128)
        nonempty(prompt, "prompt")

        async def action(call, result):
            thread = (await call("thread/read", {"threadId": threadId}))["thread"]
            if thread["status"]["type"] == "notLoaded":
                try:
                    params, sandbox = await asyncio.to_thread(saved_settings, thread)
                except OSError as error:
                    raise SettingsError(f"Cannot read saved task settings: {error}") from error
                resumed = await call("thread/resume", params)
                result["settings"] = {k: v for k, v in resumed.items() if k != "thread"}
                verify_settings(resumed, params, sandbox)
                thread = resumed["thread"]
            if thread["status"]["type"] == "active":
                turn = await self._latest(threadId)
                if turn is None or turn["status"] != "inProgress":
                    raise SettingsError("Task changed while reading it; message was not sent")
                result["turnId"] = turn["id"]
                steered = await call(
                    "turn/steer",
                    {
                        "threadId": threadId,
                        "expectedTurnId": turn["id"],
                        "input": self._input(prompt),
                    },
                )
                result.update(turnId=steered["turnId"], delivery="steered")
            else:
                started = await call(
                    "turn/start",
                    {
                        "threadId": threadId,
                        "input": self._input(prompt),
                    },
                )
                result.update(turnId=started["turn"]["id"], delivery="started")

        return await self._mutate(action, threadId=threadId)

    async def list_projects(self, limit=20, cursor=None):
        page_size(limit)
        params = {"limit": limit}
        if cursor:
            params["cursor"] = cursor
        page = await self.rpc.call("project/list", params)
        if not page["data"] and not cursor:
            raise ValueError(
                "Desktop required: this App Server exposes no project records. Use Desktop "
                "to inspect its saved project catalog, or create_thread with an explicit cwd. "
                "An empty catalog does not establish whether Desktop is connected."
            )
        return page

    async def list_threads(self, limit=20, cursor=None, cwd=None, archived=False):
        page_size(limit)
        params = {"limit": limit, "archived": archived, "useStateDbOnly": True}
        if cursor:
            params["cursor"] = cursor
        if cwd is not None:
            params["cwd"] = cwd
        return clipped(await self.rpc.call("thread/list", params), 4000)

    async def read_thread(self, threadId, turnLimit=10, cursor=None, maxOutputCharsPerItem=4000):
        nonempty(threadId, "threadId", 128)
        page_size(turnLimit)
        if not 100 <= maxOutputCharsPerItem <= 20_000:
            raise ValueError("maxOutputCharsPerItem must be between 100 and 20000")
        thread = (await self.rpc.call("thread/read", {"threadId": threadId}))["thread"]
        params = {"threadId": threadId, "limit": turnLimit, "itemsView": "full"}
        if cursor:
            params["cursor"] = cursor
        page = await self.rpc.call("thread/turns/list", params)
        return clipped({"thread": thread, "turnsPage": page}, maxOutputCharsPerItem)

    async def set_thread_title(self, threadId, title):
        nonempty(threadId, "threadId", 128)
        nonempty(title, "title", 500)

        async def action(call, result):
            await call("thread/name/set", {"threadId": threadId, "name": title})
            result["title"] = title

        return await self._mutate(action, threadId=threadId)

    async def set_thread_pinned(self, threadId, pinned):
        nonempty(threadId, "threadId", 128)

        async def action(call, result):
            await call(
                "thread/section/move",
                {
                    "threadId": threadId,
                    "sectionId": PINNED_SECTION_ID if pinned else None,
                },
            )
            result["pinned"] = pinned

        return await self._mutate(action, threadId=threadId)

    async def set_thread_archived(self, threadId, archived):
        nonempty(threadId, "threadId", 128)

        async def action(call, result):
            if archived:
                thread = (await call("thread/read", {"threadId": threadId}))["thread"]
                if thread["status"]["type"] == "active":
                    raise SettingsError("Cannot archive a running task. Wait for it to finish.")
            await call("thread/archive" if archived else "thread/unarchive", {"threadId": threadId})
            result["archived"] = archived

        return await self._mutate(action, threadId=threadId)

    def _cursor_scope(self, thread_id):
        return {"v": 1, "host": str(self.rpc.socket_path.resolve()), "threadId": thread_id}

    def _decode_cursor(self, cursor, thread_id):
        try:
            value = json.loads(base64.urlsafe_b64decode(cursor.encode()))
            if not isinstance(value, dict):
                raise ValueError
            if any(value.get(k) != v for k, v in self._cursor_scope(thread_id).items()):
                raise ValueError
            if not isinstance(value["digest"], str):
                raise ValueError
            return value["digest"]
        except (ValueError, KeyError, TypeError, UnicodeError) as error:
            raise ValueError("Invalid wait cursor for this host or task") from error

    async def _snapshot(self, thread_id):
        thread = (await self.rpc.call("thread/read", {"threadId": thread_id}))["thread"]
        turn = await self._latest(thread_id, "full")
        status = thread["status"]
        flags = status.get("activeFlags", [])
        requests = [
            m["method"]
            for m in self.rpc.interactions.values()
            if m.get("params", {}).get("threadId") == thread_id
        ]
        if "waitingOnApproval" in flags or "waitingOnUserInput" in flags or requests:
            outcome = "interaction_required"
        elif status["type"] == "active":
            outcome = "running"
        elif turn is not None and turn["status"] in {"failed", "interrupted"}:
            outcome = turn["status"]
        elif status["type"] == "systemError":
            outcome = "failed"
        elif status["type"] != "active" and (turn is None or turn["status"] == "completed"):
            outcome = "completed"
        else:
            outcome = "running"
        snapshot = clipped(
            {
                "threadId": thread_id,
                "outcome": outcome,
                "status": status,
                "turn": turn,
                "pendingMethods": sorted(set(requests)),
            },
            4000,
        )
        digest = hashlib.sha256(json.dumps(snapshot, sort_keys=True).encode()).hexdigest()
        cursor = base64.urlsafe_b64encode(
            json.dumps(
                {
                    **self._cursor_scope(thread_id),
                    "digest": digest,
                },
                sort_keys=True,
            ).encode()
        ).decode()
        return {**snapshot, "cursor": cursor}, digest

    async def wait_threads(self, targets, timeoutMs=20_000):
        if not 1 <= len(targets) <= 8:
            raise ValueError("targets must contain 1 to 8 tasks")
        if not 0 <= timeoutMs <= 120_000:
            raise ValueError("timeoutMs must be between 0 and 120000")
        ids = [t["threadId"] for t in targets]
        if len(ids) != len(set(ids)):
            raise ValueError("targets must contain distinct task IDs")
        cursors = {}
        for target in targets:
            nonempty(target["threadId"], "threadId", 128)
            if target.get("afterCursor"):
                cursors[target["threadId"]] = self._decode_cursor(
                    target["afterCursor"],
                    target["threadId"],
                )
        end = asyncio.get_running_loop().time() + timeoutMs / 1000
        while True:
            results = await asyncio.gather(
                *(self._snapshot(tid) for tid in ids), return_exceptions=True
            )
            snapshots, errors, ready = [], [], False
            for tid, result in zip(ids, results, strict=True):
                if isinstance(result, Exception):
                    error = {"threadId": tid, "error": str(result)}
                    if isinstance(result, RpcError):
                        error["rpcError"] = result.error
                    errors.append(error)
                    continue
                if isinstance(result, BaseException):
                    raise result
                snapshot, digest = result
                unchanged = cursors.get(tid) == digest
                if unchanged:
                    snapshot.pop("turn", None)
                snapshot["unchanged"] = unchanged
                snapshots.append(snapshot)
                ready |= not unchanged and snapshot["outcome"] != "running"
            remaining = end - asyncio.get_running_loop().time()
            if ready or errors or remaining <= 0:
                return {
                    "threads": snapshots,
                    "errors": errors,
                    "timedOut": not ready and not errors and timeoutMs > 0,
                }
            await asyncio.sleep(min(0.25, remaining))
