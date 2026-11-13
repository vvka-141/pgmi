import os
from pathlib import Path

import pytest

from pgmi_blackbox.case import implementation, load_cases
from pgmi_blackbox.runner import Workspace, run_case

HERE = Path(__file__).parent
ADVANCED_FLOOR = 180000
IMPL = implementation(os.environ)
CASES = load_cases(HERE, HERE / "cases", IMPL)


@pytest.mark.parametrize("case", CASES, ids=[c.path for c in CASES])
def test_case(case, server, server_version_num, binary, reference, extras, tmp_path):
    if not case.runs_on(IMPL):
        pytest.skip(f"impl: {case.only} case, PGMI_IMPL={IMPL}")
    if case.project.init == "advanced" and server_version_num < ADVANCED_FLOOR:
        pytest.skip(f"the advanced template needs PostgreSQL 18; server_version_num={server_version_num}")
    values, servers = extras(case.servers)
    ws = Workspace(
        server=server, binary=binary, root=tmp_path, extras=values, extra_servers=servers, reference=reference
    )
    run_case(case, ws)
