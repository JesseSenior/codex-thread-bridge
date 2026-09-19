"""Read server-owned settings; never create or update a bridge state file."""

import json
from pathlib import Path


class SettingsError(ValueError):
    pass


def saved_settings(thread):
    """Build resume parameters from the last complete server turn context."""
    path = Path(thread.get("path") or "")
    if not path.is_absolute():
        raise SettingsError("No saved task settings. Open the task in Desktop before messaging it.")
    context = None
    owner = None
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                if not line.endswith("\n"):
                    break  # The server can still be appending the final record.
                raise SettingsError("Invalid task metadata; no message was sent") from None
            if record.get("type") == "session_meta":
                owner = record["payload"].get("id")
            elif record.get("type") == "turn_context":
                context = record["payload"]
    if owner != thread["id"] or context is None:
        raise SettingsError("Saved task settings are unavailable; open the task in Desktop.")
    required = {"sandbox_policy", "approval_policy", "cwd", "model", "effort"}
    if not required <= context.keys():
        raise SettingsError("Incomplete saved task settings; no message was sent")
    sandbox = context["sandbox_policy"]
    kind = sandbox["type"]
    types = {
        "read-only": "readOnly",
        "workspace-write": "workspaceWrite",
        "danger-full-access": "dangerFullAccess",
    }
    if kind not in types:
        raise SettingsError(f"Cannot safely restore sandbox {kind!r}; use Desktop")
    expected = {"type": types[kind]}
    params = {
        "threadId": thread["id"],
        "excludeTurns": True,
        "cwd": thread["cwd"],
        "model": thread.get("model") or context["model"],
        "approvalPolicy": context["approval_policy"],
        "config": {"model_reasoning_effort": thread.get("reasoningEffort", context["effort"])},
    }
    if "approvals_reviewer" in context:
        params["approvalsReviewer"] = context["approvals_reviewer"]
    if "workspace_roots" in context:
        params["runtimeWorkspaceRoots"] = context["workspace_roots"]
    if thread.get("modelProvider"):
        params["modelProvider"] = thread["modelProvider"]
    for source, target in (("service_tier", "serviceTier"), ("personality", "personality")):
        if context.get(source) is not None:
            params[target] = context[source]
    if context.get("summary") is not None:
        params["config"]["model_reasoning_summary"] = context["summary"]
    profile = context.get("active_permission_profile")
    if profile:
        # Custom profiles can change independently of a task. The legacy sandbox
        # response cannot prove that their detailed filesystem rules still match.
        if profile["id"] not in {":read-only", ":workspace", ":danger-full-access"}:
            raise SettingsError("Custom permission profile cannot be verified; use Desktop")
        params["permissions"] = profile["id"]
    else:
        if context.get("file_system_sandbox_policy") is not None:
            raise SettingsError("Detailed filesystem policy cannot be restored; use Desktop")
        params["sandbox"] = kind
    if kind == "read-only":
        expected["networkAccess"] = sandbox.get("network_access", False)
        if expected["networkAccess"]:
            raise SettingsError("Read-only network override cannot be restored; use Desktop")
    elif kind == "workspace-write":
        fields = {
            "writable_roots": ("writableRoots", []),
            "network_access": ("networkAccess", False),
            "exclude_tmpdir_env_var": ("excludeTmpdirEnvVar", False),
            "exclude_slash_tmp": ("excludeSlashTmp", False),
        }
        params["config"]["sandbox_workspace_write"] = {
            key: sandbox.get(key, default) for key, (_, default) in fields.items()
        }
        expected.update(
            {name: sandbox.get(key, default) for key, (name, default) in fields.items()}
        )
    return params, expected


def verify_settings(resumed, params, sandbox):
    checks = {
        "cwd": params["cwd"],
        "model": params["model"],
        "approvalPolicy": params["approvalPolicy"],
        "sandbox": sandbox,
        "reasoningEffort": params["config"]["model_reasoning_effort"],
    }
    for key in ("approvalsReviewer", "runtimeWorkspaceRoots", "modelProvider", "serviceTier"):
        if key in params:
            checks[key] = params[key]
    if any(resumed.get(key) != value for key, value in checks.items()):
        raise SettingsError("Resumed settings differ from saved settings; no message was sent")
    if "permissions" in params:
        if (resumed.get("activePermissionProfile") or {}).get("id") != params["permissions"]:
            raise SettingsError("Resumed permission profile differs; no message was sent")
