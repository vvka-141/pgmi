from pathlib import Path

import pytest

from pgmi_blackbox.case import load_cases
from pgmi_blackbox.runner import Workspace, run_case

HERE = Path(__file__).parent
CASES = load_cases(HERE, HERE / "cases")


@pytest.mark.parametrize("case", CASES, ids=[c.path for c in CASES])
def test_case(case, server, binary, reference, extras, tmp_path):
    values, servers = extras(case.servers)
    ws = Workspace(
        server=server, binary=binary, root=tmp_path, extras=values, extra_servers=servers, reference=reference
    )
    run_case(case, ws)
