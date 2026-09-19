import pytest


async def test_worktree_request_has_no_side_effects(bridge, fake_server, tmp_path):
    fake, _ = fake_server
    with pytest.raises(ValueError, match="Worktree support is not available"):
        await bridge.create_thread(
            str(tmp_path),
            environment={
                "type": "worktree",
                "startingState": {"type": "working-tree"},
            },
            prompt="Must not be sent",
        )
    assert fake.calls == []
    assert list(tmp_path.iterdir()) == []
