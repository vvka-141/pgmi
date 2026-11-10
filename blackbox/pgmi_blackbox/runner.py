import atexit
import json
import re
import signal
import socket
import subprocess
import threading
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import psycopg

from pgmi_blackbox.case import Case, Expect, Step
from pgmi_blackbox.server import Server

_silent: socket.socket | None = None


def silent_address() -> str:
    global _silent
    if _silent is None:
        _silent = socket.create_server(("127.0.0.1", 0), backlog=128)
        atexit.register(_silent.close)
    host, port = _silent.getsockname()
    return f"{host}:{port}"


@dataclass
class Workspace:
    server: Server
    binary: Path
    root: Path
    extras: dict[str, str] = field(default_factory=dict)
    extra_servers: list[Server] = field(default_factory=list)
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
            "{silent}": silent_address(),
        } | self.extras
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
    stderr_lines: list[tuple[float, str]] = field(default_factory=list)


def run_case(case: Case, ws: Workspace) -> None:
    ws.project.mkdir(parents=True)
    ws.home.mkdir()
    try:
        _prepare_project(case, ws)
        for rel, item in (case.home or {}).items():
            target = ws.home / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(ws.substitute(item.content))
            target.chmod(item.mode)
        pending: list[tuple[str, Step, Running]] = []
        failures: list[str] = []
        for i, step in enumerate(case.steps):
            label = f"steps[{i}]"
            if step.delay:
                time.sleep(step.delay)
            if step.sql is not None:
                _run_sql(ws, step.sql)
                failures += _check_sql(ws, step.expect, label) + _check_files(ws, step.expect)
                continue
            run = _start(ws, step)
            if step.background:
                pending.append((label, step, run))
                continue
            failures += _check(ws, step.expect, _finish(label, step, run))
        for label, step, run in pending:
            failures += _check(ws, step.expect, _finish(label, step, run))
        if failures:
            raise AssertionError("\n\n".join(failures))
    finally:
        _drop(ws)


def _drop(ws: Workspace) -> None:
    for extra in ws.extra_servers:
        with extra.connect() as c:
            c.execute(f'DROP DATABASE IF EXISTS "{ws.database}" WITH (FORCE)')
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
    g = case.project.generate
    for i in range(1, g.count + 1) if g else ():
        target = ws.project / g.path.replace("{i}", f"{i:05d}")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(g.content.replace("{i}", str(i)))
    for rel, content in case.project.files.items():
        target = ws.project / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(ws.substitute(content))


@dataclass
class Running:
    proc: subprocess.Popen
    argv: list[str]
    threads: list[threading.Thread]
    stdout: list[str]
    stderr: list[tuple[float, str]]
    started: float


def _start(ws: Workspace, step: Step) -> Running:
    argv = [str(ws.binary), *(ws.substitute(a) for a in step.args)]
    proc = subprocess.Popen(
        argv,
        cwd=ws.substitute(step.cwd),
        env=ws.env(step.env),
        stdin=subprocess.PIPE if step.stdin is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    run = Running(proc, argv, [], [], [], time.monotonic())
    run.threads.append(threading.Thread(target=lambda: run.stdout.append(proc.stdout.read()), daemon=True))
    run.threads.append(threading.Thread(target=_lines, args=(proc.stderr, run.stderr, run.started), daemon=True))
    if step.stdin is not None:
        chunks = [ws.substitute(c) if isinstance(c, str) else c for c in step.stdin]
        run.threads.append(threading.Thread(target=_feed, args=(proc.stdin, chunks), daemon=True))
    for t in run.threads:
        t.start()
    for sig in step.signals:
        time.sleep(max(0.0, run.started + sig.after - time.monotonic()))
        proc.send_signal(getattr(signal, sig.name))
    return run


def _lines(stream, sink: list[tuple[float, str]], started: float) -> None:
    for line in stream:
        sink.append((time.monotonic() - started, line))


def _feed(stdin, chunks: list[str | float]) -> None:
    try:
        for chunk in chunks:
            if isinstance(chunk, str):
                stdin.write(chunk)
                stdin.flush()
            else:
                time.sleep(chunk)
    except BrokenPipeError:
        pass
    finally:
        try:
            stdin.close()
        except BrokenPipeError:
            pass


def _finish(label: str, step: Step, run: Running) -> Outcome:
    try:
        run.proc.wait(timeout=step.timeout)
    except subprocess.TimeoutExpired:
        run.proc.kill()
        run.proc.wait()
        for t in run.threads:
            t.join(5)
        run.proc.stdout.close()
        run.proc.stderr.close()
        raise AssertionError(
            f"{label}: still running after {step.timeout}s: {' '.join(run.argv)}\n{''.join(t for _, t in run.stderr)}"
        ) from None
    for t in run.threads:
        t.join(10)
    run.proc.stdout.close()
    run.proc.stderr.close()
    return Outcome(
        label, run.argv, run.proc.returncode, "".join(run.stdout), "".join(t for _, t in run.stderr), run.stderr
    )


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
    for pattern, seconds in expect.stderr_within:
        at = next((t for t, line in o.stderr_lines if re.search(pattern, line)), None)
        if at is None:
            problems.append(f"stderr lacks /{pattern}/")
        elif at > seconds:
            problems.append(f"stderr /{pattern}/ arrived after {at:.1f}s, expected within {seconds}s")
    if expect.json is not None:
        try:
            problems += _json_subset(json.loads(o.stdout), _substitute_tree(ws, expect.json), "$")
        except json.JSONDecodeError as e:
            problems.append(f"stdout is not JSON: {e}")
    problems += _check_sql(ws, expect, o.step)
    problems += _check_files(ws, expect)
    if not problems:
        return []
    report = (
        f"{o.step}: {' '.join(o.argv)}\n  "
        + "\n  ".join(problems)
        + f"\n--- stdout ---\n{o.stdout.rstrip()}\n--- stderr ---\n{o.stderr.rstrip()}"
    )
    pw = ws.server.password
    if pw:
        for secret in (f":{pw}@", f"password={pw}", f"Password={pw}", f'"password":"{pw}"'):
            report = report.replace(secret, secret.replace(pw, "***"))
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


def _check_files(ws: Workspace, expect: Expect) -> list[str]:
    problems = []
    for rel, patterns in expect.files.items():
        path = ws.root / ws.substitute(rel)
        if not path.is_file():
            problems.append(f"file {rel} does not exist")
            continue
        text = path.read_text(errors="replace")
        problems += [f"file {rel} lacks /{p}/" for p in patterns if not re.search(p, text, re.MULTILINE)]
    problems += [f"file {rel} exists" for rel in expect.absent if (ws.root / ws.substitute(rel)).exists()]
    return problems


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
                expected = [tuple(_substitute_tree(ws, v) for v in row) for row in check.rows]
                if rows != expected:
                    problems.append(f"{label}: {check.query.strip()}\n  returned {rows}\n  expected {expected}")
    except psycopg.OperationalError as e:
        problems.append(f"{label}: cannot connect to {ws.database} for the sql checks: {e}")
    return problems
