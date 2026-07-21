"""Security boundary tests for the MCP server workspace and admin surfaces."""

from __future__ import annotations

import os
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock

import pytest

from beads_mcp import server
from beads_mcp import workspace as workspace_module


@pytest.fixture(autouse=True)
def reset_server_context(monkeypatch: pytest.MonkeyPatch):
    saved = dict(server._workspace_context)
    server._workspace_context.clear()
    token = server.current_workspace.set(None)
    for name in (
        "BEADS_CONTEXT_SET",
        "BEADS_DB",
        "BEADS_MCP_ALLOWED_ROOTS",
        "BEADS_MCP_ENABLE_ADMIN_MUTATIONS",
        "BEADS_REQUIRE_CONTEXT",
        "BEADS_WORKING_DIR",
    ):
        monkeypatch.delenv(name, raising=False)
    yield
    server.current_workspace.reset(token)
    server._workspace_context.clear()
    server._workspace_context.update(saved)


def make_workspace(path: Path) -> Path:
    path.mkdir(parents=True)
    beads = path / ".beads"
    beads.mkdir()
    (beads / "metadata.json").write_text("{}")
    return path


@server.with_workspace
async def echo_workspace(workspace_root: str | None = None) -> str | None:
    return server.current_workspace.get()


@server.require_context
async def write_without_context() -> str:
    return "written"


@pytest.mark.asyncio
async def test_allowed_roots_default_to_current_directory(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    allowed = tmp_path / "allowed"
    workspace = make_workspace(allowed / "repo")
    monkeypatch.chdir(allowed)

    assert await echo_workspace(workspace_root=str(workspace)) == os.path.realpath(workspace)
    outside = make_workspace(tmp_path / "outside")
    with pytest.raises(ValueError):
        await echo_workspace(workspace_root=str(outside))


@pytest.mark.asyncio
async def test_allowed_roots_parse_path_separator_config(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    first = make_workspace(tmp_path / "first")
    second = make_workspace(tmp_path / "second")
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", f" {first} {os.pathsep}{second}")

    assert await echo_workspace(workspace_root=str(first)) == os.path.realpath(first)
    assert await echo_workspace(workspace_root=str(second)) == os.path.realpath(second)
    unlisted = make_workspace(tmp_path / "unlisted")
    with pytest.raises(ValueError):
        await echo_workspace(workspace_root=str(unlisted))


@pytest.mark.asyncio
@pytest.mark.parametrize("escape", ["outside", "symlink"])
async def test_with_workspace_rejects_realpath_outside_allowed_roots(
    escape: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    allowed = tmp_path / "allowed"
    trusted = make_workspace(allowed / "trusted")
    outside = make_workspace(tmp_path / "outside")
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", str(allowed))
    server._workspace_context["BEADS_WORKING_DIR"] = str(trusted)

    candidate = outside
    if escape == "symlink":
        candidate = allowed / "workspace-link"
        candidate.symlink_to(outside, target_is_directory=True)

    with pytest.raises(ValueError):
        await echo_workspace(workspace_root=str(candidate))


@pytest.mark.asyncio
async def test_with_workspace_rejects_redirect_outside_allowed_roots(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    allowed = tmp_path / "allowed"
    workspace = make_workspace(allowed / "worker")
    outside = make_workspace(tmp_path / "outside")
    (workspace / ".beads" / "redirect").write_text(str(outside / ".beads"))
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", str(allowed))

    with pytest.raises(ValueError):
        await echo_workspace(workspace_root=str(workspace))


@pytest.mark.asyncio
async def test_require_context_rejects_write_without_opt_in_env():
    with pytest.raises(ValueError, match="Context not set"):
        await write_without_context()


@pytest.mark.asyncio
async def test_context_set_does_not_mutate_process_environment(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    workspace = make_workspace(tmp_path / "workspace")
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", str(tmp_path))
    monkeypatch.setattr(server, "_resolve_workspace_root", lambda _path: str(workspace))
    monkeypatch.setattr(server, "_find_beads_project", lambda _path: None)
    before = {name: value for name, value in os.environ.items() if name.startswith("BEADS_")}

    await server._context_set(str(workspace))

    after = {name: value for name, value in os.environ.items() if name.startswith("BEADS_")}
    assert after == before


@pytest.mark.asyncio
async def test_admin_debug_redacts_identity_and_full_database_path(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    workspace = make_workspace(tmp_path / "workspace")
    db_path = tmp_path / "private" / ".beads" / "beads.db"
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", str(tmp_path))
    monkeypatch.setenv("HOME", "/secret/home/security-user")
    monkeypatch.setenv("USER", "security-user")
    monkeypatch.setenv("BEADS_DB", str(db_path))

    result = await server.admin(action="debug", workspace_root=str(workspace))

    assert "HOME:" not in result
    assert "USER:" not in result
    assert "/secret/home/security-user" not in result
    assert "security-user" not in result
    assert str(db_path) not in result


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("action", "mutation_args", "target"),
    [
        ("validate", {"fix_all": True}, "beads_validate"),
        ("repair", {"fix": True}, "beads_repair_deps"),
        ("pollution", {"clean": True}, "beads_detect_pollution"),
    ],
)
async def test_admin_mutations_require_explicit_enable(
    action: str,
    mutation_args: dict[str, bool],
    target: str,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    workspace = make_workspace(tmp_path / "workspace")
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", str(tmp_path))
    operation = AsyncMock(return_value={"ok": True})
    monkeypatch.setattr(server, target, operation)

    with pytest.raises((PermissionError, ValueError)):
        await server.admin(action=action, workspace_root=str(workspace), **mutation_args)
    operation.assert_not_awaited()

    monkeypatch.setenv("BEADS_MCP_ENABLE_ADMIN_MUTATIONS", "1")
    assert await server.admin(action=action, workspace_root=str(workspace), **mutation_args) == {
        "ok": True
    }
    operation.assert_awaited_once()


@pytest.mark.asyncio
async def test_context_set_then_init_binds_persistent_workspace_without_environment(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    workspace = make_workspace(tmp_path / "workspace")
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", str(tmp_path))
    monkeypatch.setattr(server, "_resolve_workspace_root", lambda _path: str(workspace))
    monkeypatch.setattr(server, "_find_beads_project", lambda _path: None)
    seen_workspaces: list[str | None] = []

    async def fake_init(prefix: str | None = None):
        seen_workspaces.append(server.current_workspace.get())
        return {"prefix": prefix}

    monkeypatch.setattr(server, "beads_init", fake_init)
    await server.context(action="set", workspace_root=str(workspace))
    monkeypatch.delenv("BEADS_CONTEXT_SET", raising=False)
    monkeypatch.delenv("BEADS_WORKING_DIR", raising=False)

    assert await server.context(action="init", prefix="safe") == {"prefix": "safe"}
    assert seen_workspaces == [os.path.realpath(workspace)]
    assert server.current_workspace.get() is None


@pytest.mark.asyncio
async def test_context_init_rejects_legacy_environment_outside_allowed_roots(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    allowed = make_workspace(tmp_path / "allowed")
    outside = make_workspace(tmp_path / "outside")
    monkeypatch.setenv("BEADS_MCP_ALLOWED_ROOTS", str(allowed))
    monkeypatch.setenv("BEADS_CONTEXT_SET", "1")
    monkeypatch.setenv("BEADS_WORKING_DIR", str(outside))
    init = AsyncMock(return_value={"initialized": True})
    monkeypatch.setattr(server, "beads_init", init)

    try:
        result = await server.context(action="init", prefix="unsafe")
    except ValueError:
        result = None

    init.assert_not_awaited()
    assert result is None or (isinstance(result, str) and result.startswith("Error:"))


def test_get_git_workspace_roots_bounds_subprocess(monkeypatch: pytest.MonkeyPatch):
    run = MagicMock(
        return_value=MagicMock(
            returncode=0,
            stdout="/repo/worktree\n/repo/.git\n",
        )
    )
    monkeypatch.setattr(workspace_module.subprocess, "run", run)

    assert workspace_module.get_git_workspace_roots("/repo/worktree") == (
        "/repo/worktree",
        "/repo",
    )
    kwargs = run.call_args.kwargs
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert 0 < kwargs["timeout"] <= 10
