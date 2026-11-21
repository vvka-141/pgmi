import os
import threading
import time
from pathlib import Path

import pytest

from pgmi_blackbox.runner import Workspace, settle, wait_gone
from pgmi_blackbox.server import postgres


def test_wait_gone_returns_once_the_sessions_are_gone():
    answers = iter([[("old",)], [("old",)], []])
    started = time.monotonic()
    assert wait_gone(lambda: next(answers), 5, poll=0.01) == []
    assert time.monotonic() - started < 1


def test_wait_gone_gives_up_at_its_bound_with_what_is_left():
    started = time.monotonic()
    assert wait_gone(lambda: [("old",)], 0.3, poll=0.01) == [("old",)]
    assert 0.3 <= time.monotonic() - started < 1


@pytest.fixture
def workspace(tmp_path: Path):
    if not os.environ.get("PGMI_TEST_CONN"):
        pytest.skip("needs PGMI_TEST_CONN")
    with postgres() as server:
        ws = Workspace(server=server, binary=Path("/bin/false"), root=tmp_path)
        with server.connect() as c:
            c.execute(f'CREATE DATABASE "{ws.database}"')
        yield ws
        with server.connect() as c:
            c.execute(f'DROP DATABASE IF EXISTS "{ws.database}" WITH (FORCE)')


def test_settle_waits_for_a_session_on_the_case_database_to_end(workspace):
    held = workspace.server.connect(workspace.database)
    threading.Timer(1, held.close).start()
    started = time.monotonic()
    settle(workspace, "steps[2]", 10)
    assert 1 <= time.monotonic() - started < 5


def test_settle_fails_naming_the_session_that_stays(workspace):
    with workspace.server.connect(workspace.database) as held:
        pid = held.info.backend_pid
        with pytest.raises(AssertionError, match=rf"steps\[2\]: settle: still connected to {workspace.database} after 0.5s:\n\({pid}, "):
            settle(workspace, "steps[2]", 0.5)
