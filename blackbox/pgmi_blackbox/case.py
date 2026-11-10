import re
import signal
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml

TEMPLATES = ("minimal", "basic", "advanced")


class CaseError(ValueError):
    pass


@dataclass(frozen=True, slots=True)
class SqlCheck:
    query: str
    rows: tuple[tuple[Any, ...], ...]


@dataclass(frozen=True, slots=True)
class Expect:
    exit: int | None
    stdout: tuple[str, ...]
    stdout_not: tuple[str, ...]
    stderr: tuple[str, ...]
    stderr_not: tuple[str, ...]
    json: dict[str, Any] | None
    sql: tuple[SqlCheck, ...]
    files: dict[str, tuple[str, ...]]
    absent: tuple[str, ...]


@dataclass(frozen=True, slots=True)
class Signal:
    name: str
    after: float


@dataclass(frozen=True, slots=True)
class Step:
    args: tuple[str, ...] | None
    sql: str | None
    env: dict[str, str]
    stdin: tuple[str | float, ...] | None
    delay: float
    background: bool
    signal: Signal | None
    timeout: float
    cwd: str
    expect: Expect


@dataclass(frozen=True, slots=True)
class Project:
    init: str | None
    files: dict[str, str]


@dataclass(frozen=True, slots=True)
class Case:
    path: str
    description: str
    project: Project
    steps: tuple[Step, ...]


class _Reader:
    def __init__(self, path: str) -> None:
        self.path = path

    def fail(self, field: str, message: str) -> CaseError:
        return CaseError(f"{self.path}: {field}: {message}")

    def mapping(self, value: Any, field: str, allowed: set[str], required: set[str] = frozenset()) -> dict:
        if not isinstance(value, dict):
            raise self.fail(field, "must be a mapping")
        for key in value:
            if key not in allowed:
                raise self.fail(
                    f"{field}.{key}" if field else str(key), f"unknown key; allowed: {', '.join(sorted(allowed))}"
                )
        for key in sorted(required):
            if key not in value:
                raise self.fail(f"{field}.{key}" if field else key, "is required")
        return value

    def string(self, value: Any, field: str) -> str:
        if not isinstance(value, str):
            raise self.fail(field, "must be a string")
        return value

    def number(self, value: Any, field: str) -> float:
        if isinstance(value, bool) or not isinstance(value, int | float) or value < 0:
            raise self.fail(field, "must be a non-negative number")
        return float(value)

    def strings(self, value: Any, field: str) -> tuple[str, ...]:
        if not isinstance(value, list) or not all(isinstance(v, str) for v in value):
            raise self.fail(field, "must be a list of strings")
        return tuple(value)

    def patterns(self, value: Any, field: str) -> tuple[str, ...]:
        items = self.strings(value, field)
        for i, pattern in enumerate(items):
            try:
                re.compile(pattern)
            except re.error as e:
                raise self.fail(f"{field}[{i}]", f"invalid regex: {e}") from e
        return items

    def string_map(self, value: Any, field: str) -> dict[str, str]:
        if not isinstance(value, dict) or not all(isinstance(k, str) and isinstance(v, str) for k, v in value.items()):
            raise self.fail(field, "must map strings to strings")
        return dict(value)


def load_cases(root: Path, directory: Path) -> list[Case]:
    cases = []
    for file in sorted(directory.rglob("*.yaml")):
        rel = str(file.relative_to(root))
        try:
            doc = yaml.safe_load(file.read_text())
        except yaml.YAMLError as e:
            raise CaseError(f"{rel}: invalid YAML: {e}") from e
        cases.append(parse_case(rel, doc))
    return cases


def parse_case(path: str, doc: Any) -> Case:
    r = _Reader(path)
    doc = r.mapping(doc, "", {"description", "project", "steps"}, {"description", "steps"})
    description = r.string(doc["description"], "description")
    project = _project(r, doc.get("project", {}))
    steps = doc["steps"]
    if not isinstance(steps, list) or not steps:
        raise r.fail("steps", "must be a non-empty list")
    return Case(path, description, project, tuple(_step(r, s, f"steps[{i}]") for i, s in enumerate(steps)))


def _project(r: _Reader, value: Any) -> Project:
    value = r.mapping(value, "project", {"init", "files"})
    init = value.get("init")
    if init is not None and init not in TEMPLATES:
        raise r.fail("project.init", f"must be one of {', '.join(TEMPLATES)}")
    return Project(init, r.string_map(value.get("files", {}), "project.files"))


def _step(r: _Reader, value: Any, field: str) -> Step:
    value = r.mapping(
        value, field, {"pgmi", "sql", "env", "stdin", "delay", "background", "signal", "timeout", "cwd", "expect"}
    )
    if ("pgmi" in value) == ("sql" in value):
        raise r.fail(field, "needs exactly one of pgmi or sql")
    args = r.strings(value["pgmi"], f"{field}.pgmi") if "pgmi" in value else None
    sql = r.string(value["sql"], f"{field}.sql") if "sql" in value else None
    if args is not None and "expect" not in value:
        raise r.fail(f"{field}.expect", "is required for a pgmi step")
    background = value.get("background", False)
    if not isinstance(background, bool):
        raise r.fail(f"{field}.background", "must be true or false")
    return Step(
        args=args,
        sql=sql,
        env=r.string_map(value.get("env", {}), f"{field}.env"),
        stdin=_stdin(r, value["stdin"], f"{field}.stdin") if "stdin" in value else None,
        delay=r.number(value.get("delay", 0), f"{field}.delay"),
        background=background,
        signal=_signal(r, value["signal"], f"{field}.signal") if "signal" in value else None,
        timeout=r.number(value.get("timeout", 120), f"{field}.timeout"),
        cwd=r.string(value.get("cwd", "{workdir}"), f"{field}.cwd"),
        expect=_expect(r, value.get("expect", {}), f"{field}.expect", pgmi=args is not None),
    )


def _stdin(r: _Reader, value: Any, field: str) -> tuple[str | float, ...]:
    if isinstance(value, str):
        return (value,)
    if not isinstance(value, list):
        raise r.fail(field, "must be a string or a list of lines and {wait: seconds}")
    chunks: list[str | float] = []
    for i, item in enumerate(value):
        if isinstance(item, str):
            chunks.append(item if item.endswith("\n") else item + "\n")
        else:
            item = r.mapping(item, f"{field}[{i}]", {"wait"}, {"wait"})
            chunks.append(r.number(item["wait"], f"{field}[{i}].wait"))
    return tuple(chunks)


def _signal(r: _Reader, value: Any, field: str) -> Signal:
    value = r.mapping(value, field, {"name", "after"}, {"name", "after"})
    name = r.string(value["name"], f"{field}.name")
    if not isinstance(getattr(signal, name, None), signal.Signals):
        raise r.fail(f"{field}.name", "must be a signal name such as SIGINT")
    return Signal(name, r.number(value["after"], f"{field}.after"))


def _expect(r: _Reader, value: Any, field: str, *, pgmi: bool) -> Expect:
    allowed = {"exit", "stdout", "stdout_not", "stderr", "stderr_not", "json", "sql", "files", "absent"}
    if not pgmi:
        allowed = {"sql", "files", "absent"}
    value = r.mapping(value, field, allowed, {"exit"} if pgmi else set())
    exit_code = value.get("exit")
    if pgmi and (isinstance(exit_code, bool) or not isinstance(exit_code, int)):
        raise r.fail(f"{field}.exit", "must be an integer")
    json_expect = value.get("json")
    if json_expect is not None and not isinstance(json_expect, dict):
        raise r.fail(f"{field}.json", "must be a mapping")
    return Expect(
        exit=exit_code,
        stdout=r.patterns(value.get("stdout", []), f"{field}.stdout"),
        stdout_not=r.patterns(value.get("stdout_not", []), f"{field}.stdout_not"),
        stderr=r.patterns(value.get("stderr", []), f"{field}.stderr"),
        stderr_not=r.patterns(value.get("stderr_not", []), f"{field}.stderr_not"),
        json=json_expect,
        sql=tuple(_sql_check(r, c, f"{field}.sql[{i}]") for i, c in enumerate(value.get("sql", []))),
        files=_files(r, value.get("files", {}), f"{field}.files"),
        absent=r.strings(value.get("absent", []), f"{field}.absent"),
    )


def _files(r: _Reader, value: Any, field: str) -> dict[str, tuple[str, ...]]:
    if not isinstance(value, dict):
        raise r.fail(field, "must map paths to lists of regexes")
    return {path: r.patterns(patterns, f"{field}.{path}") for path, patterns in value.items()}


def _sql_check(r: _Reader, value: Any, field: str) -> SqlCheck:
    value = r.mapping(value, field, {"query", "rows"}, {"query", "rows"})
    rows = value["rows"]
    if not isinstance(rows, list) or not all(isinstance(row, list) for row in rows):
        raise r.fail(f"{field}.rows", "must be a list of rows, each a list of values")
    return SqlCheck(r.string(value["query"], f"{field}.query"), tuple(tuple(row) for row in rows))
