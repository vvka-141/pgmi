import json
import re
import signal
import subprocess
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import psycopg

from pgmi_blackbox.case import Case, Expect, Step
from pgmi_blackbox.server import Server


@dataclass
class Workspace:
    server: Server
    binary: Path
    root: Path
    database: str = field(default_factory=lambda: f"bb_{uuid.uuid4().hex[:12]}")

    @property
    def project(self) -> Path:
        return self.root / "project"

    @property
    def home(self) -> Path:
        return self.root / "home"

    def substitute(self, text: str) -> str:
        values = {
            "{project}": str(self.project),
            "{home}": str(self.home),
            "{workdir}": str(self.root),
            "{db}": self.database,
            "{conn}": self.server.uri(),
            "{host}": self.server.host,
            "{port}": str(self.server.port),
            "{user}": self.server.user,
            "{password}": self.server.password,
        }
        for key, value in values.items():
            text = text.replace(key, value)
        return text

    def env(self, extra: dict[str, str]) -> dict[str, str]:
        base = {"PATH": "/usr/bin:/bin", "HOME": str(self.home), "LANG": "C.UTF-8"}
        return base | {k: self.substitute(v) for k, v in extra.items()}


@dataclass
class Outcome:
    step: str
    argv: list[str]
    code: int
    stdout: str
    stderr: str


def run_case(case: Case, ws: Workspace) -> None:
    ws.project.mkdir(parents=True)
    ws.home.mkdir()
    try:
        _prepare_project(case, ws)
        pending: list[tuple[str, Step, subprocess.Popen, list[str]]] = []
        failures: list[str] = []
        for i, step in enumerate(case.steps):
            label = f"steps[{i}]"
            if step.delay:
                time.sleep(step.delay)
            if step.sql is not None:
                _run_sql(ws, step.sql)
                failures += _check_sql(ws, step.expect, label)
                continue
            proc, argv = _start(ws, step)
            if step.background:
                pending.append((label, step, proc, argv))
                continue
            failures += _check(ws, step.expect, _finish(label, step, proc, argv))
        for label, step, proc, argv in pending:
            failures += _check(ws, step.expect, _finish(label, step, proc, argv))
        if failures:
            raise AssertionError("\n\n".join(failures))
    finally:
        _drop(ws)


def _drop(ws: Workspace) -> None:
    with ws.server.connect() as c:
        c.execute(f'DROP DATABASE IF EXISTS "{ws.database}" WITH (FORCE)')
        roles = c.execute(
            "SELECT rolname FROM pg_roles WHERE starts_with(rolname, %s) ORDER BY rolname DESC",
            (ws.database + "_",),
        ).fetchall()
        for (role,) in roles:
            c.execute(f'DROP ROLE IF EXISTS "{role}"')


def _prepare_project(case: Case, ws: Workspace) -> None:
    if case.project.init:
        argv = [str(ws.binary), "init", str(ws.project), "-t", case.project.init]
        done = subprocess.run(
            argv, env=ws.env({}), capture_output=True, text=True, stdin=subprocess.DEVNULL, timeout=60
        )
        if done.returncode != 0:
            raise AssertionError(f"project.init failed ({done.returncode}):\n{done.stderr}")
    for rel, content in case.project.files.items():
        target = ws.project / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(ws.substitute(content))


def _start(ws: Workspace, step: Step) -> tuple[subprocess.Popen, list[str]]:
    argv = [str(ws.binary), *(ws.substitute(a) for a in step.args)]
    proc = subprocess.Popen(
        argv,
        cwd=ws.root,
        env=ws.env(step.env),
        stdin=subprocess.PIPE if step.stdin is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if step.signal:
        time.sleep(step.signal.after)
        proc.send_signal(getattr(signal, step.signal.name))
    return proc, argv


def _finish(label: str, step: Step, proc: subprocess.Popen, argv: list[str]) -> Outcome:
    stdin = step.stdin if step.stdin is not None else None
    try:
        out, err = proc.communicate(input=stdin, timeout=step.timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        out, err = proc.communicate()
        raise AssertionError(f"{label}: still running after {step.timeout}s: {' '.join(argv)}\n{err}") from None
    return Outcome(label, argv, proc.returncode, out, err)


def _check(ws: Workspace, expect: Expect, o: Outcome) -> list[str]:
    problems: list[str] = []
    if o.code != expect.exit:
        problems.append(f"exit code {o.code}, expected {expect.exit}")
    for name, text, present, absent in (
        ("stdout", o.stdout, expect.stdout, expect.stdout_not),
        ("stderr", o.stderr, expect.stderr, expect.stderr_not),
    ):
        problems += [f"{name} lacks /{p}/" for p in present if not re.search(p, text, re.MULTILINE)]
        problems += [f"{name} has /{p}/" for p in absent if re.search(p, text, re.MULTILINE)]
    if expect.json is not None:
        try:
            problems += _json_subset(json.loads(o.stdout), _substitute_tree(ws, expect.json), "$")
        except json.JSONDecodeError as e:
            problems.append(f"stdout is not JSON: {e}")
    problems += _check_sql(ws, expect, o.step)
    if not problems:
        return []
    report = (
        f"{o.step}: {' '.join(o.argv)}\n  "
        + "\n  ".join(problems)
        + f"\n--- stdout ---\n{o.stdout.rstrip()}\n--- stderr ---\n{o.stderr.rstrip()}"
    )
    if ws.server.password:
        report = report.replace(ws.server.password, "***")
    return [report]


def _substitute_tree(ws: Workspace, value: Any) -> Any:
    if isinstance(value, str):
        return ws.substitute(value)
    if isinstance(value, dict):
        return {k: _substitute_tree(ws, v) for k, v in value.items()}
    if isinstance(value, list):
        return [_substitute_tree(ws, v) for v in value]
    return value


def _json_subset(actual: Any, expected: Any, path: str) -> list[str]:
    if isinstance(expected, dict):
        if not isinstance(actual, dict):
            return [f"{path} is {actual!r}, expected an object"]
        problems = []
        for key, value in expected.items():
            if key not in actual:
                problems.append(f"{path}.{key} is missing")
            else:
                problems += _json_subset(actual[key], value, f"{path}.{key}")
        return problems
    return [] if actual == expected else [f"{path} is {actual!r}, expected {expected!r}"]


def _run_sql(ws: Workspace, sql: str) -> None:
    with ws.server.connect(ws.database) as c:
        c.execute(ws.substitute(sql))


def _check_sql(ws: Workspace, expect: Expect, label: str) -> list[str]:
    if not expect.sql:
        return []
    problems = []
    try:
        with ws.server.connect(ws.database) as c:
            for check in expect.sql:
                try:
                    rows = [tuple(r) for r in c.execute(ws.substitute(check.query)).fetchall()]
                except psycopg.Error as e:
                    problems.append(f"{label}: {check.query.strip()}\n  failed: {e}")
                    continue
                if rows != list(check.rows):
                    problems.append(f"{label}: {check.query.strip()}\n  returned {rows}\n  expected {list(check.rows)}")
    except psycopg.OperationalError as e:
        problems.append(f"{label}: cannot connect to {ws.database} for the sql checks: {e}")
    return problems
