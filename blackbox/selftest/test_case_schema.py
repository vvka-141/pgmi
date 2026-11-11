import pytest

from pgmi_blackbox.case import CaseError, Fifo, Symlink, implementation, parse_case

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


def test_sql_steps_run_on_the_case_database_unless_told_maintenance():
    doc = {**MINIMAL, "steps": [{"sql": "SELECT 1"}, {"sql": "SELECT 2", "database": "maintenance"}]}
    steps = parse_case("cases/x.yaml", doc).steps
    assert [s.maintenance for s in steps] == [False, True]


def test_stdin_list_keeps_lines_and_waits():
    doc = {**MINIMAL, "steps": [{"pgmi": ["serve"], "stdin": ["a", {"wait": 2}, "b\n"], "expect": {"exit": 0}}]}
    assert parse_case("cases/x.yaml", doc).steps[0].stdin == ("a\n", 2.0, "b\n")


def test_project_files_take_text_hex_symlinks_and_fifos():
    files = {"a.sql": "SELECT 1;", "b.dat": {"hex": "00ff"}, "c.sql": {"symlink": "../x.sql"}, "d.sql": {"fifo": True}}
    project = parse_case("cases/x.yaml", {**MINIMAL, "project": {"files": files}}).project
    assert project.files == {
        "a.sql": "SELECT 1;",
        "b.dat": b"\x00\xff",
        "c.sql": Symlink("../x.sql"),
        "d.sql": Fifo(),
    }


def test_stdin_text_and_hex_repeat_without_newline():
    stdin = [{"text": "ab", "times": 2}, {"hex": "ff00"}, {"hex": "61", "times": 3}]
    doc = {**MINIMAL, "steps": [{"pgmi": ["serve"], "stdin": stdin, "expect": {"exit": 0}}]}
    assert parse_case("cases/x.yaml", doc).steps[0].stdin == ("abab", b"\xff\x00", b"aaa")


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
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "stdin": [{"hex": "zz"}], "expect": {"exit": 0}}]},
            "steps[0].stdin[0].hex",
        ),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "stdin": [{"hex": "61", "text": "a"}], "expect": {"exit": 0}}]},
            "steps[0].stdin[0]",
        ),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "stdin": [{"text": "a", "times": 0}], "expect": {"exit": 0}}]},
            "steps[0].stdin[0].times",
        ),
        ({**MINIMAL, "project": {"files": {"a": {"hex": "0"}}}}, "project.files.a.hex"),
        ({**MINIMAL, "project": {"files": {"a": {"hex": "00", "symlink": "b"}}}}, "project.files.a"),
        ({**MINIMAL, "project": {"files": {"a": {"fifo": "yes"}}}}, "project.files.a.fifo"),
        ({**MINIMAL, "project": {"files": {"a": 1}}}, "project.files.a"),
        ({**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "files": ["a"]}}]}, "steps[0].expect.files"),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "files": {"a": ["("]}}}]},
            "steps[0].expect.files.a[0]",
        ),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "rust": {"exitt": 10}}}]},
            "steps[0].expect.rust.exitt",
        ),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "rust": {"rust": {}}}}]},
            "steps[0].expect.rust.rust",
        ),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "expect": {"exit": 0, "rust": {"stderr": ["("]}}}]},
            "steps[0].expect.rust.stderr[0]",
        ),
        ({**MINIMAL, "steps": [{"sql": "SELECT 1", "expect": {"rust": {"exit": 1}}}]}, "steps[0].expect.rust.exit"),
        ({**MINIMAL, "steps": [{"sql": "SELECT 1", "database": "postgres"}]}, "steps[0].database"),
        ({**MINIMAL, "impl": "python"}, "impl"),
        (
            {**MINIMAL, "steps": [{"pgmi": ["x"], "database": "maintenance", "expect": {"exit": 0}}]},
            "steps[0].database",
        ),
    ],
)
def test_malformed_case_names_file_and_field(doc, field):
    with pytest.raises(CaseError) as err:
        parse_case("cases/bad.yaml", doc)
    assert str(err.value).startswith(f"cases/bad.yaml: {field}:")


RUST_OVERRIDE = {
    **MINIMAL,
    "steps": [
        {
            "pgmi": ["deploy"],
            "expect": {"exit": 11, "stderr": ["refused"], "stdout": ["done"], "rust": {"exit": 10, "stderr": ["key"]}},
        }
    ],
}


def test_rust_override_is_ignored_for_go():
    expect = parse_case("cases/x.yaml", RUST_OVERRIDE).steps[0].expect
    assert (expect.exit, expect.stderr, expect.stdout) == (11, ("refused",), ("done",))
    assert parse_case("cases/x.yaml", RUST_OVERRIDE, "go").steps[0].expect == expect


def test_rust_override_replaces_only_the_keys_it_sets():
    expect = parse_case("cases/x.yaml", RUST_OVERRIDE, "rust").steps[0].expect
    assert (expect.exit, expect.stderr, expect.stdout) == (10, ("key",), ("done",))


def test_a_case_runs_on_every_implementation_unless_it_names_one():
    assert parse_case("cases/x.yaml", MINIMAL).runs_on("go")
    rust_only = parse_case("cases/x.yaml", {**MINIMAL, "impl": "rust"})
    assert rust_only.runs_on("rust")
    assert not rust_only.runs_on("go")


@pytest.mark.parametrize(
    ("environ", "impl"), [({}, "go"), ({"PGMI_IMPL": "go"}, "go"), ({"PGMI_IMPL": "rust"}, "rust")]
)
def test_implementation_comes_from_pgmi_impl(environ, impl):
    assert implementation(environ) == impl


@pytest.mark.parametrize("value", ["", "Rust", "c"])
def test_unknown_implementation_fails_the_run(value):
    with pytest.raises(CaseError, match="PGMI_IMPL"):
        implementation({"PGMI_IMPL": value})
