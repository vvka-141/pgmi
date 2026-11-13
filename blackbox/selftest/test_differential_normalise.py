from pathlib import Path
from types import SimpleNamespace

from pgmi_blackbox.differential import _stdout, normalise

WS = SimpleNamespace(binary=None, reference=None, project=Path("/p"), home=Path("/h"), root=Path("/r"), database="db1")


def test_server_numbers_are_normalised():
    text = "DETAIL: Process 121 waits for AccessExclusiveLock on relation 16862 of database 16706; blocked by process 122."
    assert normalise(text, WS) == (
        "DETAIL: Process <n> waits for AccessExclusiveLock on relation <n> of database <n>; blocked by process <n>."
    )


def test_session_temp_schema_numbers_are_normalised():
    ws = SimpleNamespace(**{**vars(WS), "project": Path("/proj"), "home": Path("/home-dir"), "root": Path("/root-dir")})
    text = "WHERE: PL/pgSQL function pg_temp_96.pgmi_run_file(text) line 9 at EXECUTE"
    assert normalise(text, ws) == "WHERE: PL/pgSQL function pg_temp_<n>.pgmi_run_file(text) line 9 at EXECUTE"


def test_json_lines_compare_as_data():
    go = '{"b":1,"a":{"y":"\\u003e","x":2},"durationMs":5}\n'
    rust = '{"a":{"x":2,"y":">"},"b":1,"durationMs":9}\n'
    assert _stdout(go, WS, sort=False) == _stdout(rust, WS, sort=False)
    assert _stdout("plain\n", WS, sort=False) == "plain\n"
