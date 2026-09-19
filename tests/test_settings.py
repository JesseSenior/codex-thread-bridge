import json

import pytest

from codex_thread_bridge.settings import SettingsError, saved_settings, verify_settings


def metadata(tmp_path, **fields):
    context = {
        "cwd": str(tmp_path),
        "model": "model",
        "effort": "low",
        "approval_policy": "on-request",
        "sandbox_policy": {"type": "read-only"},
        "workspace_roots": [str(tmp_path)],
        **fields,
    }
    path = tmp_path / "task.jsonl"
    path.write_text(
        json.dumps({"type": "session_meta", "payload": {"id": "task"}})
        + "\n"
        + json.dumps({"type": "turn_context", "payload": context})
        + "\n"
    )
    return {"id": "task", "cwd": str(tmp_path), "path": str(path)}


@pytest.mark.parametrize(
    "kind,expected",
    [
        ("read-only", {"type": "readOnly", "networkAccess": False}),
        ("danger-full-access", {"type": "dangerFullAccess"}),
        (
            "workspace-write",
            {
                "type": "workspaceWrite",
                "writableRoots": ["/extra"],
                "networkAccess": True,
                "excludeSlashTmp": True,
                "excludeTmpdirEnvVar": True,
            },
        ),
    ],
)
def test_saved_sandbox_fields_are_preserved(tmp_path, kind, expected):
    policy = {"type": kind}
    if kind == "workspace-write":
        policy.update(
            writable_roots=["/extra"],
            network_access=True,
            exclude_slash_tmp=True,
            exclude_tmpdir_env_var=True,
        )
    thread = metadata(tmp_path, sandbox_policy=policy)
    params, sandbox = saved_settings(thread)
    assert sandbox == expected
    assert params["sandbox"] == kind
    response = {
        "cwd": params["cwd"],
        "model": "model",
        "reasoningEffort": "low",
        "approvalPolicy": "on-request",
        "sandbox": sandbox,
        "runtimeWorkspaceRoots": [str(tmp_path)],
    }
    verify_settings(response, params, sandbox)
    response["runtimeWorkspaceRoots"] = ["/different"]
    with pytest.raises(SettingsError, match="differ"):
        verify_settings(response, params, sandbox)


def test_saved_builtin_profile_and_custom_profile_rejection(tmp_path):
    thread = metadata(tmp_path, active_permission_profile={"id": ":read-only"})
    params, _ = saved_settings(thread)
    assert params["permissions"] == ":read-only" and "sandbox" not in params
    thread = metadata(tmp_path, active_permission_profile={"id": "custom"})
    with pytest.raises(SettingsError, match="Custom permission profile"):
        saved_settings(thread)


def test_missing_wrong_owner_and_incomplete_metadata(tmp_path):
    with pytest.raises(SettingsError):
        saved_settings({"id": "task"})
    thread = metadata(tmp_path)
    thread["id"] = "other"
    with pytest.raises(SettingsError):
        saved_settings(thread)
    thread = metadata(tmp_path)
    with open(thread["path"], "a") as stream:
        stream.write('{"type": "turn_context"')
    assert saved_settings(thread)[0]["model"] == "model"
    with open(thread["path"], "a") as stream:
        stream.write("\n")
    with pytest.raises(SettingsError, match="Invalid task metadata"):
        saved_settings(thread)
