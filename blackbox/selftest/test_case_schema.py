import pytest

from pgmi_blackbox.case import CaseError, parse_case

MINIMAL = {
    "description": "deploy succeeds",
    "steps": [{"pgmi": ["deploy", "{project}"], "expect": {"exit": 0}}],
}


def test_minimal_case_parses():
    case = parse_case("cases/x.yaml", MINIMAL)
    assert case.description == "deploy succeeds"
    assert case.steps[0].args == ("deploy", "{project}")
    assert case.steps[0].expect.exit == 0
    assert case.steps[0].cwd == "{workdir}"
    assert case.steps[0].expect.files == {}


def test_stdin_list_keeps_lines_and_waits():
    doc = {**MINIMAL, "steps": [{"pgmi": ["serve"], "stdin": ["a", {"wait": 2}, "b\n"], "expect": {"exit": 0}}]}
    assert parse_case("cases/x.yaml", doc).steps[0].stdin == ("a\n", 2.0, "b\n")


@pytest.mark.parametrize(
    ("doc", "field"),
    [
        ({"steps": MINIMAL["steps"]}, "description"),
        ({**MINIMAL, "steps": []}, "steps"),
        ({**MINIMAL, "surprise": 1}, "surprise"),
        ({**MINIMAL, "steps": [{"pgmi": ["deploy"]}]}, "steps[0].expect"),
        ({**MINIMAL, "steps": [{"pgmi": ["deploy"], "expect": {}}]}, "steps[0].expect.exit"),
        ({**MINIMAL, "steps": [{"pgmi": "deploy", "expect": {"exit": 0}}]}, "steps[0].pgmi"),
        ({**MINIMAL, "steps": [{"sql": "SELECT 1", "pgmi": ["x"]}]}, "steps[0]"),
        ({**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "stdout": ["("]}}]}, "steps[0].expect.stdout[0]"),
        ({**MINIMAL, "project": {"init": "huge"}}, "project.init"),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "signal": {"name": "SIGFOO", "after": 1}, "expect": {"exit": 0}}]},
            "steps[0].signal.name",
        ),
        ({**MINIMAL, "steps": [{"pgmi": ["x"], "cwd": 3, "expect": {"exit": 0}}]}, "steps[0].cwd"),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "stdin": [{"pause": 1}], "expect": {"exit": 0}}]},
            "steps[0].stdin[0].pause",
        ),
        ({**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "files": ["a"]}}]}, "steps[0].expect.files"),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "files": {"a": ["("]}}}]},
            "steps[0].expect.files.a[0]",
        ),
    ],
)
def test_malformed_case_names_file_and_field(doc, field):
    with pytest.raises(CaseError) as err:
        parse_case("cases/bad.yaml", doc)
    assert str(err.value).startswith(f"cases/bad.yaml: {field}:")
