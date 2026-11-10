import pytest

from pgmi_blackbox.server import pgmi_binary, postgres


@pytest.fixture(scope="session")
def server():
    with postgres() as s:
        yield s


@pytest.fixture(scope="session")
def binary(tmp_path_factory):
    return pgmi_binary(tmp_path_factory.mktemp("bin"))
