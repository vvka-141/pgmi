"""Differential run: the same case through two pgmi binaries, each in its own workspace and database.

Each step's exit code, stdout, stderr, --json record and the target database's catalog are compared after
normalising only what differs by design. A difference is accepted when the case's own expect.rust overrides
that field for that step, or when an entry of differential-allow.yaml names its row of the 0.20 table.
"""

import difflib
import json
import re
import subprocess
from dataclasses import dataclass, field, replace
from functools import cache
from pathlib import Path
from typing import Any

import psycopg
import yaml
from psycopg import sql

from pgmi_blackbox.case import Case, Step
from pgmi_blackbox.runner import Outcome, Workspace, play

CONTRACT = Path(__file__).resolve().parents[2] / "docs/design/behaviour-contract.md"
SECTION = "## Behaviour changes in 0.20"
FIELDS = ("exit", "stdout", "stderr", "json", "db", "error")

_EXPECT_KEYS = {
    "exit": ("exit",),
    "stdout": ("stdout", "stdout_not", "raw", "raw_not"),
    "stderr": ("stderr", "stderr_not", "stderr_within"),
    "json": ("json",),
    "db": ("sql",),
}
_DURATION = re.compile(r"\b(\d+(\.\d+)?(h|ms|µs|us|ns|m|s))+\b")
_BUILD = re.compile(r"^Commit: .*, Built: .*$", re.MULTILINE)
_UUID = re.compile(r"\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b")
_JSON_VOLATILE = {"durationMs", "startedAt", "finishedAt"}
_SERVER_NUMBER = re.compile(r"\b((?:[Pp]rocess|PID|pid|relation|database|transaction|OID|oid) )\d+\b")

_USER = r"n.nspname !~ '^(pg_|information_schema$)'"
_CATALOG = f"""
SELECT format('schema %I owner=%s acl=%s', n.nspname, n.nspowner::regrole, n.nspacl)
FROM pg_namespace n WHERE {_USER}
UNION ALL
SELECT format('relation %I.%I kind=%s acl=%s', n.nspname, c.relname, c.relkind, c.relacl)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE {_USER} AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
UNION ALL
SELECT format('column %I.%I.%I %s%s', n.nspname, c.relname, a.attname, format_type(a.atttypid, a.atttypmod),
              CASE WHEN a.attnotnull THEN ' not null' END)
FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE {_USER} AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND a.attnum > 0 AND NOT a.attisdropped
UNION ALL
SELECT format('function %I.%I(%s) returns %s kind=%s acl=%s', n.nspname, p.proname,
              pg_get_function_identity_arguments(p.oid), pg_get_function_result(p.oid), p.prokind, p.proacl)
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE {_USER}
UNION ALL
SELECT format('database acl=%s', datacl) FROM pg_database WHERE datname = current_database()
UNION ALL
SELECT format('role %I login=%s member of %s', r.rolname, r.rolcanlogin,
              ARRAY(SELECT m.roleid::regrole::text FROM pg_auth_members m WHERE m.member = r.oid ORDER BY 1))
FROM pg_roles r WHERE starts_with(r.rolname, current_database() || '_')
"""
_TABLES = f"""
SELECT n.nspname, c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE {_USER} AND c.relkind IN ('r', 'p')
"""


@dataclass(frozen=True, slots=True)
class Observation:
    step: str
    values: dict[str, str]


@dataclass(frozen=True, slots=True)
class Difference:
    subject: str
    step: str
    field: str
    go: str | None
    rust: str | None
    reason: str | None = None

    def __str__(self) -> str:
        lines = difflib.unified_diff(
            _shown(self.go).splitlines(), _shown(self.rust).splitlines(), "go", "rust", lineterm="", n=2
        )
        return f"{self.subject} {self.step} {self.field}\n" + "\n".join(lines)


def _shown(value: str | None) -> str:
    return "<absent>" if value is None else value


@cache
def _version(binary: Path) -> str:
    return subprocess.run([binary, "--version"], capture_output=True, text=True, check=True).stdout.split()[1]


def normalise(text: str, ws: Workspace) -> str:
    for binary in {ws.binary, ws.reference} - {None}:
        text = text.replace(_version(binary), "<version>")
    for value, name in (
        (str(ws.project), "{project}"),
        (str(ws.home), "{home}"),
        (str(ws.root), "{workdir}"),
        (ws.database, "{db}"),
    ):
        text = text.replace(value, name)
    text = _SERVER_NUMBER.sub(r"\1<n>", text)
    return _DURATION.sub("<duration>", _UUID.sub("<uuid>", _BUILD.sub("<build>", text)))


def _normalise_json(value: Any, ws: Workspace) -> Any:
    if isinstance(value, dict):
        return {k: "<volatile>" if k in _JSON_VOLATILE else _normalise_json(v, ws) for k, v in value.items()}
    if isinstance(value, list):
        return [_normalise_json(v, ws) for v in value]
    return normalise(value, ws) if isinstance(value, str) else value


def snapshot(ws: Workspace) -> list[str]:
    try:
        with ws.server.connect(ws.database) as c:
            lines = [r[0] for r in c.execute(_CATALOG)]
            for schema, table in c.execute(_TABLES).fetchall():
                query = sql.SQL("SELECT count(*) FROM {}.{}").format(sql.Identifier(schema), sql.Identifier(table))
                lines.append(f"rows {schema}.{table} {c.execute(query).fetchone()[0]}")
    except psycopg.OperationalError:
        return ["<no database>"]
    return sorted(normalise(line, ws) for line in lines)


def _fields(ws: Workspace, step: Step, o: Outcome) -> dict[str, str]:
    values = {"exit": str(o.code), "stderr": normalise(o.stderr, ws), "db": "\n".join(snapshot(ws))}
    try:
        record = json.loads(o.stdout) if any(a.startswith("--json") for a in step.args) else None
    except json.JSONDecodeError:
        record = None
    if record is None:
        values["stdout"] = _stdout(o.stdout, ws, sort=step.args[0] == "serve")
    else:
        values["json"] = _canonical(record, ws, indent=1)
    return values


def _canonical(value: Any, ws: Workspace, indent: int | None = None) -> str:
    return json.dumps(_normalise_json(value, ws), indent=indent, sort_keys=True, ensure_ascii=False)


def _stdout(text: str, ws: Workspace, sort: bool) -> str:
    try:
        return _canonical(json.loads(text), ws, indent=1)
    except json.JSONDecodeError:
        pass
    lines = [_json_line(line, ws) for line in text.splitlines()]
    return "\n".join(sorted(lines) if sort else lines) + ("\n" if text.endswith("\n") else "")


def _json_line(line: str, ws: Workspace) -> str:
    if line.startswith(("{", "[")):
        try:
            return _canonical(json.loads(line), ws)
        except json.JSONDecodeError:
            pass
    return normalise(line, ws)


def observe(case: Case, ws: Workspace) -> list[Observation]:
    seen: list[Observation] = []

    def record(label: str, step: Step, outcome: Outcome | None) -> list[str]:
        if outcome is not None:
            seen.append(Observation(label, _fields(ws, step, outcome)))
        return []

    try:
        play(case, ws, record)
    except Exception as e:
        seen.append(Observation("run", {"error": normalise(f"{type(e).__name__}: {e}", ws)}))
    return seen


def overridden(go: Case, rust: Case) -> dict[str, set[str]]:
    return {
        f"steps[{i}]": {
            name
            for name, keys in _EXPECT_KEYS.items()
            if any(getattr(g.expect, k) != getattr(r.expect, k) for k in keys)
        }
        for i, (g, r) in enumerate(zip(go.steps, rust.steps, strict=True))
    }


def differences(subject: str, go: list[Observation], rust: list[Observation]) -> list[Difference]:
    by_step = {o.step: [o.values, {}] for o in go}
    for o in rust:
        by_step.setdefault(o.step, [{}, {}])[1] = o.values
    return [
        Difference(subject, step, name, g.get(name), r.get(name))
        for step, (g, r) in by_step.items()
        for name in FIELDS
        if g.get(name) != r.get(name)
    ]


@dataclass(frozen=True, slots=True)
class Allowance:
    row: str
    case: re.Pattern[str]
    step: str | None
    field: str
    go: re.Pattern[str] | None
    rust: re.Pattern[str] | None
    go_as: str | None = None
    rust_as: str | None = None

    @property
    def rewrites(self) -> bool:
        return self.go_as is not None or self.rust_as is not None

    def applies(self, d: Difference) -> bool:
        return self.case.fullmatch(d.subject) is not None and self.step in (None, d.step) and self.field == d.field

    def accepts(self, d: Difference) -> bool:
        return (
            self.applies(d)
            and (self.go is None or self.go.search(_shown(d.go)) is not None)
            and (self.rust is None or self.rust.search(_shown(d.rust)) is not None)
        )

    def rewrite(self, d: Difference) -> Difference:
        go, rust = d.go, d.rust
        if self.go_as is not None and go is not None:
            go = self.go.sub(self.go_as, go)
        if self.rust_as is not None and rust is not None:
            rust = self.rust.sub(self.rust_as, rust)
        return replace(d, go=go, rust=rust)


@dataclass(frozen=True, slots=True)
class AllowList:
    skip: dict[str, str] = field(default_factory=dict)
    accept: tuple[Allowance, ...] = ()

    def reason(self, d: Difference) -> str | None:
        plain = next((a.row for a in self.accept if not a.rewrites and a.accepts(d)), None)
        if plain is not None:
            return f"0.20: {plain}"
        rows: list[str] = []
        for a in self.accept:
            if a.rewrites and a.accepts(d):
                d = a.rewrite(d)
                rows.append(a.row)
        if d.go == d.rust:
            return "0.20: " + "; ".join(dict.fromkeys(rows))
        row = next((a.row for a in self.accept if not a.rewrites and a.accepts(d)), None)
        return None if row is None else "0.20: " + "; ".join(dict.fromkeys([*rows, row]))


def contract_rows(text: str) -> set[str]:
    section = text.split(SECTION, 1)[1].split("\n## ", 1)[0]
    rows = {line.split("|")[1].strip() for line in section.splitlines() if line.startswith("| ")}
    return rows | {line[4:].strip() for line in section.splitlines() if line.startswith("### ")}


def load_allow(path: Path, contract: Path = CONTRACT) -> AllowList:
    doc = yaml.safe_load(path.read_text()) or {}
    rows = contract_rows(contract.read_text())
    accept = []
    for i, entry in enumerate(doc.get("accept") or []):
        at = f"{path.name}: accept[{i}]"
        keys = {"row", "case", "step", "field", "go", "rust", "go_as", "rust_as"}
        if set(entry) - keys or not {"row", "case", "field"} <= set(entry):
            raise ValueError(f"{at}: needs row, case and field; may have step, go, rust, go_as and rust_as")
        if entry["row"] not in rows:
            raise ValueError(f"{at}: row {entry['row']!r} is not a row or heading of '{SECTION}' in {contract.name}")
        if entry["field"] not in FIELDS:
            raise ValueError(f"{at}: field must be one of {', '.join(FIELDS)}")
        if "go" not in entry and "rust" not in entry:
            raise ValueError(f"{at}: needs a go or a rust pattern, so it matches narrowly")
        for side in ("go", "rust"):
            if f"{side}_as" in entry and side not in entry:
                raise ValueError(f"{at}: {side}_as rewrites what {side} matches, so it needs {side}")
        accept.append(
            Allowance(
                entry["row"],
                re.compile(entry["case"]),
                entry.get("step"),
                entry["field"],
                re.compile(entry["go"], re.MULTILINE) if "go" in entry else None,
                re.compile(entry["rust"], re.MULTILINE) if "rust" in entry else None,
                entry.get("go_as"),
                entry.get("rust_as"),
            )
        )
    return AllowList(dict(doc.get("skip") or {}), tuple(accept))


def compare(go_case: Case, rust_case: Case, go_ws: Workspace, rust_ws: Workspace, allow: AllowList) -> list[Difference]:
    accepted = overridden(go_case, rust_case)
    found = differences(go_case.path, observe(go_case, go_ws), observe(go_case, rust_ws))
    return [
        replace(d, reason=f"{go_case.path} expect.rust" if d.field in accepted.get(d.step, ()) else allow.reason(d))
        for d in found
    ]
