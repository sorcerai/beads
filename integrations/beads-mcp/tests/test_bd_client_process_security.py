"""Behavioral tests for the bd subprocess security boundary."""

import asyncio
import contextlib
import json
import os
import signal
import subprocess
import sys
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from beads_mcp.bd_client import BdClient, BdCommandError
from beads_mcp.models import AddDependencyParams, CloseIssueParams, InitParams


@pytest.fixture
def command_script(tmp_path: Path) -> Path:
    script = tmp_path / "command.py"
    script.write_text(
        """
import json
import os
import subprocess
import sys
import time
from pathlib import Path

mode = sys.argv[1]
if mode == "normal":
    print(json.dumps({"ok": True, "value": 42}))
elif mode == "environment":
    print(json.dumps({
        "github_token": os.environ.get("GITHUB_TOKEN"),
        "aws_secret_access_key": os.environ.get("AWS_SECRET_ACCESS_KEY"),
        "path": os.environ.get("PATH"),
        "home": os.environ.get("HOME"),
        "beads_dir": os.environ.get("BEADS_DIR"),
    }))
elif mode == "stdout-overflow":
    print(json.dumps({"payload": "x" * 4096}))
elif mode == "stderr-overflow":
    sys.stderr.write("x" * 4096)
    print(json.dumps({"ok": True}))
elif mode == "hang":
    child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
    Path(sys.argv[2]).write_text(json.dumps([os.getpid(), child.pid]))
    while True:
        time.sleep(60)
elif mode == "silent-hang":
    Path(sys.argv[2]).write_text(str(os.getpid()))
    os.close(1)
    os.close(2)
    while True:
        time.sleep(60)
else:
    raise SystemExit(f"unknown mode: {mode}")
""".lstrip()
    )
    return script


def make_client(tmp_path: Path) -> BdClient:
    beads_dir = tmp_path / ".beads"
    beads_dir.mkdir(exist_ok=True)
    return BdClient(
        bd_path=sys.executable,
        beads_dir=str(beads_dir),
        working_dir=str(tmp_path),
    )


def process_exists(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


@pytest.mark.asyncio
@pytest.mark.skipif(sys.platform == "win32", reason="process-group descendant termination is POSIX-specific")
async def test_run_command_timeout_terminates_process_and_descendants(
    tmp_path: Path, command_script: Path
) -> None:
    client = make_client(tmp_path)
    client.command_timeout = 0.5
    pid_file = tmp_path / "pids.json"
    pids: list[int] = []

    try:
        with pytest.raises(BdCommandError, match="timed out"):
            await asyncio.wait_for(
                client._run_command(str(command_script), "hang", str(pid_file)),
                timeout=3,
            )

        pids = json.loads(pid_file.read_text())
        deadline = asyncio.get_running_loop().time() + 1
        while any(process_exists(pid) for pid in pids) and asyncio.get_running_loop().time() < deadline:
            await asyncio.sleep(0.01)
        assert not any(process_exists(pid) for pid in pids), f"timed-out command left processes alive: {pids}"
    finally:
        if pid_file.exists() and not pids:
            pids = json.loads(pid_file.read_text())
        for pid in pids:
            with contextlib.suppress(ProcessLookupError):
                os.kill(pid, signal.SIGKILL)


@pytest.mark.asyncio
@pytest.mark.parametrize("stream", ["stdout-overflow", "stderr-overflow"])
async def test_run_command_caps_each_output_stream(
    tmp_path: Path, command_script: Path, stream: str
) -> None:
    client = make_client(tmp_path)
    client.max_output_bytes = 64

    with pytest.raises(BdCommandError, match="output limit"):
        await client._run_command(str(command_script), stream)


@pytest.mark.asyncio
async def test_run_command_sanitizes_credentials_and_preserves_required_environment(
    tmp_path: Path, command_script: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    client = make_client(tmp_path)
    safe_path = os.environ.get("PATH", "")
    safe_home = str(tmp_path / "home")
    monkeypatch.setenv("PATH", safe_path)
    monkeypatch.setenv("HOME", safe_home)
    monkeypatch.setenv("GITHUB_TOKEN", "must-not-reach-child")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "must-not-reach-child")

    result = await client._run_command(str(command_script), "environment")

    assert result == {
        "github_token": None,
        "aws_secret_access_key": None,
        "path": safe_path,
        "home": safe_home,
        "beads_dir": client.beads_dir,
    }


@pytest.mark.asyncio
async def test_run_command_returns_normal_json(
    tmp_path: Path, command_script: Path
) -> None:
    client = make_client(tmp_path)

    result = await client._run_command(str(command_script), "normal")

    assert result == {"ok": True, "value": 42}


@pytest.fixture
def guarded_bd(tmp_path: Path) -> Path:
    script = tmp_path / "guarded-bd"
    script.write_text(
        f"#!{sys.executable}\n"
        + """
import json
import os
import time
from pathlib import Path

mode = Path("guard-mode").read_text()
if mode == "environment":
    Path("observed-env.json").write_text(json.dumps({
        "github_token": os.environ.get("GITHUB_TOKEN"),
        "aws_secret_access_key": os.environ.get("AWS_SECRET_ACCESS_KEY"),
        "path": os.environ.get("PATH"),
        "home": os.environ.get("HOME"),
        "beads_dir": os.environ.get("BEADS_DIR"),
    }))
    print("bd version 1.0.0")
elif mode == "output":
    print("bd version 1.0.0 " + "x" * 4096)
elif mode == "timeout":
    Path("alternate-pid").write_text(str(os.getpid()))
    while True:
        time.sleep(60)
else:
    raise SystemExit(f"unknown guard mode: {mode}")
""".lstrip()
    )
    script.chmod(0o700)
    return script


async def wait_for_path(path: Path, timeout: float = 1) -> None:
    deadline = asyncio.get_running_loop().time() + timeout
    while not path.exists() and asyncio.get_running_loop().time() < deadline:
        await asyncio.sleep(0.01)
    assert path.exists(), f"subprocess did not create {path}"


async def wait_for_processes_to_exit(pids: list[int], timeout: float = 1) -> None:
    deadline = asyncio.get_running_loop().time() + timeout
    while any(process_exists(pid) for pid in pids) and asyncio.get_running_loop().time() < deadline:
        await asyncio.sleep(0.01)


async def invoke_alternate_command(client: BdClient, command: str) -> None:
    if command == "check-version":
        await client._check_version()
    elif command == "add-dependency":
        await client.add_dependency(
            AddDependencyParams(issue_id="bd-child", depends_on_id="bd-parent", dep_type="blocks")
        )
    elif command == "text-command":
        await client._run_text_command("comment", "bd-1", "hello")
    elif command == "quickstart":
        await client.quickstart()
    elif command == "init":
        await client.init(InitParams(prefix="test"))
    else:
        raise AssertionError(f"unknown command case: {command}")


@pytest.mark.asyncio
@pytest.mark.skipif(sys.platform == "win32", reason="process-group descendant termination is POSIX-specific")
async def test_run_command_timeout_includes_wait_after_output_eof(
    tmp_path: Path, command_script: Path
) -> None:
    client = make_client(tmp_path)
    client.command_timeout = 0.2
    pid_file = tmp_path / "silent-pid"
    pid: int | None = None

    try:
        with pytest.raises(BdCommandError, match="timed out"):
            await asyncio.wait_for(
                client._run_command(str(command_script), "silent-hang", str(pid_file)),
                timeout=2,
            )
        pid = int(pid_file.read_text())
        await wait_for_processes_to_exit([pid])
        assert not process_exists(pid), f"timed-out silent command remained alive: {pid}"
    finally:
        if pid is None and pid_file.exists():
            pid = int(pid_file.read_text())
        if pid is not None:
            with contextlib.suppress(ProcessLookupError):
                os.kill(pid, signal.SIGKILL)


@pytest.mark.asyncio
@pytest.mark.skipif(sys.platform == "win32", reason="process-group descendant termination is POSIX-specific")
async def test_run_command_cancellation_terminates_and_reaps_process_group(
    tmp_path: Path, command_script: Path
) -> None:
    client = make_client(tmp_path)
    pid_file = tmp_path / "cancel-pids.json"
    task = asyncio.create_task(client._run_command(str(command_script), "hang", str(pid_file)))
    pids: list[int] = []

    try:
        await wait_for_path(pid_file)
        pids = json.loads(pid_file.read_text())
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task

        await wait_for_processes_to_exit(pids)
        assert not any(process_exists(pid) for pid in pids), f"cancelled command left processes alive: {pids}"
    finally:
        if not task.done():
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task
        if pid_file.exists() and not pids:
            pids = json.loads(pid_file.read_text())
        for pid in pids:
            with contextlib.suppress(ProcessLookupError):
                os.kill(pid, signal.SIGKILL)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "command",
    ["check-version", "add-dependency", "text-command", "quickstart", "init"],
)
@pytest.mark.parametrize("guard", ["environment", "output", "timeout"])
async def test_alternate_command_paths_share_subprocess_hardening(
    tmp_path: Path,
    guarded_bd: Path,
    monkeypatch: pytest.MonkeyPatch,
    command: str,
    guard: str,
) -> None:
    beads_dir = tmp_path / ".beads"
    beads_dir.mkdir()
    client = BdClient(bd_path=str(guarded_bd), beads_dir=str(beads_dir), working_dir=str(tmp_path))
    client.command_timeout = 0.2 if guard == "timeout" else 2
    client.max_output_bytes = 64
    (tmp_path / "guard-mode").write_text(guard)

    safe_path = os.environ.get("PATH", "")
    safe_home = str(tmp_path / "home")
    monkeypatch.setenv("PATH", safe_path)
    monkeypatch.setenv("HOME", safe_home)
    monkeypatch.setenv("GITHUB_TOKEN", "must-not-reach-child")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "must-not-reach-child")
    pid_file = tmp_path / "alternate-pid"
    pid: int | None = None

    try:
        if guard == "environment":
            await invoke_alternate_command(client, command)
            observed = json.loads((tmp_path / "observed-env.json").read_text())
            assert observed == {
                "github_token": None,
                "aws_secret_access_key": None,
                "path": safe_path,
                "home": safe_home,
                "beads_dir": None if command == "init" else str(beads_dir),
            }
        elif guard == "output":
            with pytest.raises(BdCommandError, match="output limit"):
                await invoke_alternate_command(client, command)
        else:
            with pytest.raises(BdCommandError, match="timed out"):
                await asyncio.wait_for(invoke_alternate_command(client, command), timeout=2)
            pid = int(pid_file.read_text())
            await wait_for_processes_to_exit([pid])
            assert not process_exists(pid), f"timed-out {command} process remained alive: {pid}"
    finally:
        if pid is None and pid_file.exists():
            pid = int(pid_file.read_text())
        if pid is not None:
            with contextlib.suppress(ProcessLookupError):
                os.kill(pid, signal.SIGKILL)


@pytest.mark.asyncio
async def test_mcp_close_suppresses_post_close_hook(tmp_path: Path) -> None:
    marker = tmp_path / "post-close-marker"
    fake_bd = tmp_path / "close-bd"
    fake_bd.write_text(
        f"#!{sys.executable}\n"
        + """
import json
import os
import sys
from pathlib import Path

if os.environ.get("BD_NO_CLOSE_HOOK") != "1" and "--no-hooks" not in sys.argv:
    Path("post-close-marker").write_text("hook fired")
print(json.dumps([{
    "id": "bd-1",
    "title": "Closed issue",
    "status": "closed",
    "priority": 1,
    "issue_type": "bug",
    "created_at": "2025-01-25T00:00:00Z",
    "updated_at": "2025-01-25T01:00:00Z",
    "closed_at": "2025-01-25T01:00:00Z"
}]))
""".lstrip()
    )
    fake_bd.chmod(0o700)
    beads_dir = tmp_path / ".beads"
    beads_dir.mkdir()
    client = BdClient(bd_path=str(fake_bd), beads_dir=str(beads_dir), working_dir=str(tmp_path))

    closed = await client.close(CloseIssueParams(issue_id="bd-1", reason="done"))

    assert closed[0].status == "closed"
    assert not marker.exists(), "MCP close allowed the post-close hook subprocess boundary to fire"


def windows_process(pid: int = 4242, returncode: int | None = None) -> MagicMock:
    process = MagicMock()
    process.pid = pid
    process.returncode = returncode
    process.kill = MagicMock()
    process.wait = AsyncMock(return_value=returncode if returncode is not None else -9)
    return process


@pytest.mark.asyncio
async def test_execute_uses_windows_process_group_creation_flag(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    client = make_client(tmp_path)
    creation_flag = 0x200
    monkeypatch.setattr(sys, "platform", "win32")
    monkeypatch.setattr(subprocess, "CREATE_NEW_PROCESS_GROUP", creation_flag, raising=False)
    process = windows_process(returncode=0)
    process.stdout = MagicMock(read=AsyncMock(side_effect=[b"{}", b""]))
    process.stderr = MagicMock(read=AsyncMock(side_effect=[b""]))

    with patch("asyncio.create_subprocess_exec", AsyncMock(return_value=process)) as create:
        await client._execute([client.bd_path, "show"])

    kwargs = create.await_args.kwargs
    assert kwargs["creationflags"] == creation_flag
    assert "start_new_session" not in kwargs


@pytest.mark.asyncio
async def test_kill_process_group_uses_absolute_taskkill_and_reaps_direct_process(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    client = make_client(tmp_path)
    monkeypatch.setattr(sys, "platform", "win32")
    monkeypatch.setenv("SystemRoot", r"C:\Windows")
    direct = windows_process()
    taskkill = windows_process(pid=5151, returncode=0)

    async def bounded_wait(awaitable, timeout):
        assert timeout > 0
        return await awaitable

    with (
        patch("asyncio.create_subprocess_exec", AsyncMock(return_value=taskkill)) as create,
        patch("beads_mcp.bd_client.asyncio.wait_for", AsyncMock(side_effect=bounded_wait)) as wait_for,
    ):
        await client._kill_process_group(direct)

    create.assert_awaited_once()
    assert create.await_args.args == (
        r"C:\Windows\System32\taskkill.exe",
        "/PID",
        "4242",
        "/T",
        "/F",
    )
    wait_for.assert_awaited_once()
    taskkill.wait.assert_awaited_once()
    direct.kill.assert_not_called()
    direct.wait.assert_awaited_once()


@pytest.mark.asyncio
@pytest.mark.parametrize("failure", ["unavailable", "nonzero-exit"])
async def test_kill_process_group_falls_back_to_direct_kill_on_taskkill_failure(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, failure: str
) -> None:
    client = make_client(tmp_path)
    monkeypatch.setattr(sys, "platform", "win32")
    monkeypatch.setenv("SystemRoot", r"C:\Windows")
    direct = windows_process()
    taskkill = windows_process(pid=5151, returncode=1)
    if failure == "unavailable":
        create = AsyncMock(side_effect=FileNotFoundError("taskkill missing"))
    else:
        create = AsyncMock(return_value=taskkill)

    with patch("asyncio.create_subprocess_exec", create):
        await client._kill_process_group(direct)

    create.assert_awaited_once()
    direct.kill.assert_called_once_with()
    direct.wait.assert_awaited_once()
