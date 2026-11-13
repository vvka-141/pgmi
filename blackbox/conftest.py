from contextlib import ExitStack

import pytest

from pgmi_blackbox.extras import Extras
from pgmi_blackbox.server import pgmi_binary, postgres, reference_binary


@pytest.fixture(scope="session")
def server():
    with postgres() as s:
        yield s


@pytest.fixture(scope="session")
def server_version_num(server):
    with server.connect() as conn:
        return int(conn.execute("SHOW server_version_num").fetchone()[0])


@pytest.fixture(scope="session")
def binary(tmp_path_factory):
    return pgmi_binary(tmp_path_factory.mktemp("bin"))


@pytest.fixture(scope="session")
def extras(tmp_path_factory):
    with ExitStack() as stack:
        yield Extras(tmp_path_factory.mktemp("extras"), stack).ensure


@pytest.fixture(scope="session")
def reference():
    return reference_binary()
