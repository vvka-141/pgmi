import os
import tempfile
from collections import Counter
from pathlib import Path

import pytest
import yaml
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

from pgmi_blackbox.case import Case, load_cases, parse_case
from pgmi_blackbox.differential import Difference, compare, load_allow
from pgmi_blackbox.runner import Workspace

HERE = Path(__file__).parent
GO, RUST = os.environ.get("PGMI_BIN"), os.environ.get("PGMI_BIN_OTHER")
pytestmark = pytest.mark.skipif(not (GO and RUST), reason="needs PGMI_BIN (Go) and PGMI_BIN_OTHER (Rust)")
ALLOW = load_allow(HERE / "differential-allow.yaml")
CASES = list(zip(load_cases(HERE, HERE / "cases", "go"), load_cases(HERE, HERE / "cases", "rust"), strict=True))
VARIANTS = settings(
    max_examples=int(os.environ.get("PGMI_DIFF_EXAMPLES", "20")),
    derandomize=True,
    database=None,
    deadline=None,
    suppress_health_check=[HealthCheck.function_scoped_fixture, HealthCheck.too_slow],
)


@pytest.fixture(scope="session")
def tally(request):
    counts = Counter()
    yield counts
    reporter = request.config.pluginmanager.get_plugin("terminalreporter")
    reporter.write_line("differential: " + ", ".join(f"{k} {v}" for k, v in sorted(counts.items())))


@pytest.fixture
def run(server, extras, tmp_path, tally):
    def go(go_case, rust_case=None) -> list[Difference]:
        values, servers = extras(go_case.servers)
        root = Path(tempfile.mkdtemp(dir=tmp_path))
        go_ws, rust_ws = (
            Workspace(
                server=server,
                binary=Path(b).resolve(),
                root=root / name,
                extras=values,
                extra_servers=servers,
                reference=Path(GO).resolve(),
            )
            for name, b in (("go", GO), ("rust", RUST))
        )
        found = compare(go_case, rust_case or go_case, go_ws, rust_ws, ALLOW)
        tally["accepted differences"] += sum(d.reason is not None for d in found)
        unexplained = [d for d in found if d.reason is None]
        tally["unexplained differences"] += len(unexplained)
        return unexplained

    return go


def _fail(found: list[Difference]) -> None:
    if found:
        pytest.fail("\n\n".join(map(str, found)), pytrace=False)


@pytest.mark.parametrize("pair", CASES, ids=[g.path for g, _ in CASES])
def test_case(pair, run, tally):
    go_case, rust_case = pair
    if go_case.only:
        tally["cases skipped"] += 1
        pytest.skip(f"impl: {go_case.only}, scope the other binary does not have")
    if go_case.path in ALLOW.skip:
        tally["cases skipped"] += 1
        pytest.skip(ALLOW.skip[go_case.path])
    tally["cases compared"] += 1
    _fail(run(go_case, rust_case))


def _variant(name: str, files: dict[str, str], steps: list[dict]) -> Case:
    for s in steps:
        s["expect"] = {"exit": 0}
    return parse_case(f"variant/{name}", {"description": name, "project": {"files": files}, "steps": steps})


def _explore(strategy, build, run, tally) -> None:
    found: list[Difference] = []

    @VARIANTS
    @given(strategy)
    def check(value):
        tally["variants"] += 1
        found.extend(run(build(value)))

    check()
    _fail(found)


def test_connection_strings(server, run, tally):
    def build(v):
        uri, host, port, sslmode, extra = v
        pairs = [*extra, *([f"sslmode={sslmode}"] if sslmode else [])]
        if uri:
            conn = f"postgresql://{server.user}:{server.password}@{host}:{port}/{server.maintenance_db}"
            conn += "?" + "&".join(pairs) if pairs else ""
        else:
            conn = " ".join(
                [
                    f"host={host}",
                    f"port={port}",
                    f"user={server.user}",
                    f"password={server.password}",
                    f"dbname={server.maintenance_db}",
                    *pairs,
                ]
            )
        return _variant(
            f"connection: {conn}",
            {"deploy.sql": "CREATE TABLE marker AS SELECT current_setting('application_name') AS app;\n"},
            [{"pgmi": ["deploy", "{project}", "--connection", conn, "-d", "{db}", "--timeout", "5s"]}],
        )

    strategy = st.tuples(
        st.booleans(),
        st.sampled_from([server.host, "localhost"]),
        st.sampled_from([str(server.port), str(server.port), "1", "0", "70000", "port"]),
        st.sampled_from([None, "disable", "allow", "prefer", "require", "verify-ca", "verify-full", "bogus"]),
        st.lists(
            st.sampled_from(
                [
                    "application_name=bb",
                    "connect_timeout=5",
                    "target_session_attrs=any",
                    "frobnicate=1",
                    "options=-csearch_path=public",
                    "connect_timeout=x",
                ]
            ),
            max_size=2,
            unique=True,
        ),
    )
    _explore(strategy, build, run, tally)


PARAMS_DEPLOY = """DO $$
DECLARE r record;
BEGIN
    FOR r IN SELECT key, value FROM pg_temp.pgmi_parameter_view ORDER BY key LOOP
        RAISE NOTICE 'param %=[%]', r.key, r.value;
    END LOOP;
END $$;
"""


def test_pgmi_yaml(server, run, tally):
    def build(doc):
        unreachable = doc["connection"]["port"] == 1 and "timeout" not in doc
        return _variant(
            f"pgmi.yaml: {doc}",
            {"pgmi.yaml": yaml.safe_dump(doc), "deploy.sql": PARAMS_DEPLOY},
            [
                {
                    "pgmi": ["deploy", "{project}", *(["--timeout", "5s"] if unreachable else [])],
                    "env": {"PGPASSWORD": server.password},
                }
            ],
        )

    connection = st.fixed_dictionaries(
        {
            "host": st.sampled_from([server.host, "localhost"]),
            "port": st.sampled_from([server.port, server.port, str(server.port), 1, "x", -5]),
            "username": st.just(server.user),
        },
        optional={
            "database": st.just("{db}"),
            "sslmode": st.sampled_from(["disable", "prefer", "require", "bogus"]),
            "maintenance_database": st.sampled_from(["postgres", "nope"]),
            "usernmae": st.just("typo"),
        },
    )
    params = st.dictionaries(
        st.from_regex(r"[a-zA-Z_][a-zA-Z0-9_.-]{0,8}", fullmatch=True),
        st.one_of(st.text(st.sampled_from("ab ='\"$#\\é\n"), max_size=6), st.integers(), st.booleans(), st.none()),
        max_size=3,
    )
    timeout = st.sampled_from(["30s", "5m", "1h", "0s", "-1s", "abc", 30, "", "1d"])
    strategy = st.fixed_dictionaries(
        {"connection": connection},
        optional={"params": params, "timeout": timeout, "unknown": st.just(1)},
    )
    _explore(strategy, build, run, tally)


IGNORE_FILES = (
    "a.sql",
    "b/c.sql",
    "b/d.txt",
    "b/e/f.sql",
    "docs/x.md",
    "notes.txt",
    "deep/x/y/z.sql",
    "sp ace.sql",
    "[x].sql",
    "UP.SQL",
)
IGNORE_PATTERNS = (
    "*.txt",
    "b/",
    "!b/c.sql",
    "/a.sql",
    "**/f.sql",
    "b/*",
    "[[:space:]]*",
    "# comment",
    "\\#x",
    "*.sql",
    "!deploy.sql",
    "docs",
    "deep/**/z.sql",
    "?.sql",
    "\\[x\\].sql",
    "",
    "  ",
    "!*.txt",
    "b/**",
    "*.SQL",
    "deploy.sql",
    "a.sql/",
    "[a-c]*",
    "[!a]*.sql",
)
SOURCES_DEPLOY = """DO $$
DECLARE r record;
BEGIN
    FOR r IN SELECT path FROM pg_temp.pgmi_source_view ORDER BY path LOOP
        RAISE NOTICE 'source %', r.path;
    END LOOP;
END $$;
"""


def test_pgmiignore(run, tally):
    def build(v):
        files, patterns = v
        tree = {f: "SELECT 1;\n" for f in files} | {"deploy.sql": SOURCES_DEPLOY, ".pgmiignore": "\n".join(patterns)}
        return _variant(
            f"pgmiignore: {patterns} over {files}",
            tree,
            [
                {"pgmi": ["info", "{project}", "--json"]},
                {"pgmi": ["metadata", "plan", "{project}", "--json"]},
                {"pgmi": ["metadata", "validate", "{project}"]},
                {"pgmi": ["deploy", "{project}", "--connection", "{conn}", "-d", "{db}"]},
            ],
        )

    strategy = st.tuples(
        st.lists(st.sampled_from(IGNORE_FILES), unique=True, max_size=6),
        st.lists(st.sampled_from(IGNORE_PATTERNS), max_size=4),
    )
    _explore(strategy, build, run, tally)
