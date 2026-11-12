from pathlib import Path

import pytest

from pgmi_blackbox.differential import AllowList, Difference, contract_rows, load_allow

HERE = Path(__file__).resolve().parents[1]


def test_the_allow_list_loads_against_the_contract():
    allow = load_allow(HERE / "differential-allow.yaml")
    unknown = sorted(set(allow.skip) - {str(p.relative_to(HERE)) for p in (HERE / "cases").rglob("*.yaml")})
    assert not unknown, f"skipped cases that do not exist: {unknown}"


def test_rows_are_first_cells_and_headings_of_the_section():
    text = "## Behaviour changes in 0.20\n### Heading\n| A | b |\n|---|---|\n| Row one | x |\n## Next\n| Late | y |\n"
    assert contract_rows(text) == {"Heading", "A", "Row one"}


def _allow(tmp_path: Path, entry: str) -> AllowList:
    contract = tmp_path / "contract.md"
    contract.write_text("## Behaviour changes in 0.20\n| Unknown key | exit 11 | exit 10 | owner |\n")
    path = tmp_path / "allow.yaml"
    path.write_text(f"accept:\n  - {entry}\n")
    return load_allow(path, contract)


def test_an_entry_must_name_a_contract_row(tmp_path):
    with pytest.raises(ValueError, match="is not a row or heading"):
        _allow(tmp_path, "{row: Invented, case: x, field: exit, rust: '10'}")


def test_an_entry_must_match_narrowly(tmp_path):
    with pytest.raises(ValueError, match="needs a go or a rust pattern"):
        _allow(tmp_path, "{row: Unknown key, case: x, field: exit}")


def test_an_entry_accepts_only_its_case_step_field_and_pattern(tmp_path):
    allow = _allow(tmp_path, "{row: Unknown key, case: 'variant/conn.*', step: 'steps[0]', field: exit, rust: '^10$'}")
    d = Difference("variant/conn: x", "steps[0]", "exit", "11", "10")
    assert allow.reason(d) == "0.20: Unknown key"
    for other in (
        Difference("cases/x.yaml", "steps[0]", "exit", "11", "10"),
        Difference("variant/conn: x", "steps[1]", "exit", "11", "10"),
        Difference("variant/conn: x", "steps[0]", "stderr", "11", "10"),
        Difference("variant/conn: x", "steps[0]", "exit", "11", "13"),
    ):
        assert allow.reason(other) is None


def test_rewrites_accept_only_what_they_leave_equal(tmp_path):
    allow = _allow(tmp_path, "{row: Unknown key, case: x, field: stderr, go: '(?<=: )infix: ', go_as: ''}")
    assert allow.reason(Difference("x", "steps[0]", "stderr", "e: infix: a", "e: a")) == "0.20: Unknown key"
    assert allow.reason(Difference("x", "steps[0]", "stderr", "e: infix: a", "e: b")) is None


def test_a_rewrite_needs_the_pattern_it_rewrites(tmp_path):
    with pytest.raises(ValueError, match="rust_as rewrites what rust matches"):
        _allow(tmp_path, "{row: Unknown key, case: x, field: exit, go: '1', rust_as: ''}")
