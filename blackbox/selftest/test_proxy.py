import socket
import struct
import threading
import time

import pytest

from pgmi_blackbox.case import parse_case
from pgmi_blackbox.proxy import Proxy


class FakeServer:
    """Answers a startup with AuthenticationOk and ReadyForQuery, and each Query or Sync with ReadyForQuery."""

    def __init__(self) -> None:
        self.listener = socket.create_server(("127.0.0.1", 0))
        self.port = self.listener.getsockname()[1]
        self.seen: list[tuple[float, bytes]] = []
        threading.Thread(target=self._accept, daemon=True).start()

    def _accept(self) -> None:
        while True:
            try:
                conn, _ = self.listener.accept()
            except OSError:
                return
            threading.Thread(target=self._serve, args=(conn,), daemon=True).start()

    def _serve(self, conn: socket.socket) -> None:
        with conn:
            try:
                length = struct.unpack("!i", _exact(conn, 4))[0]
                _exact(conn, length - 4)
                conn.sendall(b"R" + struct.pack("!ii", 8, 0) + _ready())
                while True:
                    head = _exact(conn, 5)
                    body = _exact(conn, struct.unpack("!i", head[1:])[0] - 4)
                    self.seen.append((time.monotonic(), head[:1] + body))
                    if head[:1] == b"X":
                        return
                    if head[:1] == b"Q" and b"three" in body:
                        conn.sendall(_row(b"a", None, b"c"))
                    if head[:1] in (b"Q", b"S"):
                        conn.sendall(b"C" + struct.pack("!i", 11) + b"SELECT\0" + _ready())
            except (OSError, ConnectionError):
                self.seen.append((time.monotonic(), b"EOF"))

    def queries(self) -> list[bytes]:
        return [m for _, m in self.seen if m[:1] in (b"Q", b"P")]


def _row(*cells: bytes | None) -> bytes:
    body = struct.pack("!h", len(cells)) + b"".join(
        struct.pack("!i", -1) if c is None else struct.pack("!i", len(c)) + c for c in cells
    )
    return b"D" + struct.pack("!i", len(body) + 4) + body


def _ready(status: bytes = b"I") -> bytes:
    return b"Z" + struct.pack("!i", 5) + status


def _exact(sock: socket.socket, n: int) -> bytes:
    data = b""
    while len(data) < n:
        chunk = sock.recv(n - len(data))
        if not chunk:
            raise ConnectionError("closed")
        data += chunk
    return data


def _message(sock: socket.socket) -> tuple[bytes, bytes]:
    head = _exact(sock, 5)
    return head[:1], _exact(sock, struct.unpack("!i", head[1:])[0] - 4)


def _query(sql: str) -> bytes:
    body = sql.encode() + b"\0"
    return b"Q" + struct.pack("!i", len(body) + 4) + body


def _startup(sock: socket.socket) -> None:
    body = struct.pack("!i", 196608) + b"user\0u\0database\0d\0\0"
    sock.sendall(struct.pack("!i", len(body) + 4) + body)


def _connect(port: int, timeout: float = 3) -> socket.socket:
    sock = socket.create_connection(("127.0.0.1", port), timeout=timeout)
    _startup(sock)
    assert _message(sock)[0] == b"R"
    assert _message(sock) == (b"Z", b"I")
    return sock


def _faults(*rules: dict):
    return parse_case("cases/x.yaml", {"description": "x", "proxy": list(rules), "steps": [{"sql": "SELECT 1"}]}).proxy


@pytest.fixture
def proxy(tmp_path):
    server = FakeServer()
    started: list[Proxy] = []

    def make(*rules: dict) -> tuple[Proxy, FakeServer]:
        p = Proxy(_faults(*rules), ("127.0.0.1", server.port), tmp_path / "proxy.log").start()
        started.append(p)
        return p, server

    yield make
    for p in started:
        p.stop()
    server.listener.close()


def test_an_empty_plan_forwards_and_answers_ssl_requests_itself(proxy):
    p, server = proxy()
    with socket.create_connection(("127.0.0.1", p.port), timeout=3) as sock:
        sock.sendall(struct.pack("!ii", 8, 80877103))
        assert _exact(sock, 1) == b"N"
        _startup(sock)
        assert _message(sock)[0] == b"R"
        _message(sock)
        sock.sendall(_query("SELECT 1"))
        assert _message(sock)[0] == b"C"
    assert server.queries() == [b"QSELECT 1\0"]
    assert "conn 1 query 'SELECT 1'" in p.log_path.read_text()


def test_the_first_connections_are_reset_then_forwarded(proxy):
    p, _ = proxy({"at": "accept", "do": "reset", "times": 2})
    for _ in range(2):
        with socket.create_connection(("127.0.0.1", p.port), timeout=3) as sock:
            with pytest.raises((ConnectionError, OSError)):
                _startup(sock)
                _message(sock)
    _connect(p.port).close()


def test_a_startup_is_answered_with_the_chosen_error_and_closed(proxy):
    p, _ = proxy({"at": "startup", "do": {"error": "57P03"}, "times": 1})
    with socket.create_connection(("127.0.0.1", p.port), timeout=3) as sock:
        _startup(sock)
        kind, body = _message(sock)
        assert kind == b"E"
        assert b"SFATAL\0" in body and b"C57P03\0" in body and b"starting up" in body
        assert sock.recv(1) == b""
    _connect(p.port).close()


def test_a_query_is_reset_before_the_server_sees_it(proxy):
    p, server = proxy({"query": "marker", "do": "reset"})
    sock = _connect(p.port)
    sock.sendall(_query("SELECT 'marker'"))
    with pytest.raises((ConnectionError, OSError)):
        _message(sock)
    assert server.queries() == []


def test_a_reset_after_a_delay_lets_the_query_through_first(proxy):
    p, server = proxy({"query": "marker", "do": "reset", "after": 0.3})
    sock = _connect(p.port)
    sent = time.monotonic()
    sock.sendall(_query("SELECT 'marker'"))
    assert _message(sock)[0] == b"C"
    _message(sock)
    with pytest.raises((ConnectionError, OSError)):
        _message(sock)
    assert time.monotonic() - sent >= 0.3
    assert server.queries() == [b"QSELECT 'marker'\0"]


def test_an_error_answers_a_query_in_the_servers_place_and_the_session_goes_on(proxy):
    p, server = proxy({"query": "INSERT", "do": {"error": "25006", "severity": "ERROR"}})
    sock = _connect(p.port)
    sock.sendall(_query("INSERT INTO t VALUES (1)"))
    kind, body = _message(sock)
    assert kind == b"E" and b"SERROR\0" in body and b"C25006\0" in body
    assert _message(sock) == (b"Z", b"I")
    sock.sendall(_query("SELECT 2"))
    assert _message(sock)[0] == b"C"
    assert server.queries() == [b"QSELECT 2\0"]


def test_an_error_for_an_extended_query_comes_at_its_sync(proxy):
    p, server = proxy({"query": "INSERT", "do": {"error": "25006", "severity": "ERROR"}})
    sock = _connect(p.port)
    parse = b"\0INSERT INTO t VALUES ($1)\0\0\0"
    sock.sendall(b"P" + struct.pack("!i", len(parse) + 4) + parse + b"S" + struct.pack("!i", 4))
    assert _message(sock)[0] == b"E"
    assert _message(sock) == (b"Z", b"I")
    assert server.queries() == []


def test_a_fatal_error_after_a_delay_ends_a_running_query(proxy):
    p, server = proxy({"query": "pg_sleep", "do": {"error": "57P01"}, "after": 0.2})
    sock = _connect(p.port)
    sock.sendall(_query("SELECT pg_sleep(9)"))
    assert _message(sock)[0] == b"C"
    _message(sock)
    kind, body = _message(sock)
    assert kind == b"E" and b"C57P01\0" in body
    assert sock.recv(1) == b""
    assert server.queries() == [b"QSELECT pg_sleep(9)\0"]


def test_cut_client_ends_the_client_side_and_leaves_the_server_side_open(proxy):
    p, server = proxy({"query": "pg_sleep", "do": "cut-client", "after": 0.2})
    sock = _connect(p.port)
    sock.sendall(_query("SELECT pg_sleep(9)"))
    assert _message(sock)[0] == b"C"
    _message(sock)
    with pytest.raises((ConnectionError, OSError)):
        _message(sock)
    time.sleep(0.3)
    assert [m for _, m in server.seen if m == b"EOF"] == []


def test_freeze_answers_nothing_more_on_any_connection(proxy):
    p, server = proxy({"query": "freeze", "do": "freeze"})
    sock = _connect(p.port, timeout=0.5)
    sock.sendall(_query("SELECT 'freeze'"))
    with pytest.raises(TimeoutError):
        _message(sock)
    with socket.create_connection(("127.0.0.1", p.port), timeout=0.5) as other:
        _startup(other)
        with pytest.raises(TimeoutError):
            _message(other)
    assert server.queries() == []


def test_delay_holds_the_matching_query(proxy):
    p, _ = proxy({"query": "slow", "do": {"delay": 0.4}})
    sock = _connect(p.port)
    sent = time.monotonic()
    sock.sendall(_query("SELECT 'slow'"))
    assert _message(sock)[0] == b"C"
    assert time.monotonic() - sent >= 0.4


def test_a_slow_close_reaches_the_server_late(proxy):
    p, server = proxy({"at": "close", "do": {"delay": 0.5}})
    sock = _connect(p.port)
    closed = time.monotonic()
    sock.sendall(b"X" + struct.pack("!i", 4))
    sock.close()
    deadline = time.monotonic() + 3
    while not any(m == b"X" for _, m in server.seen) and time.monotonic() < deadline:
        time.sleep(0.02)
    at = next(t for t, m in server.seen if m == b"X")
    assert at - closed >= 0.5


def test_a_rule_can_name_the_step(proxy):
    p, _ = proxy({"at": "startup", "do": {"error": "53300"}, "step": 2})
    _connect(p.port).close()
    p.step = 2
    with socket.create_connection(("127.0.0.1", p.port), timeout=3) as sock:
        _startup(sock)
        assert b"C53300\0" in _message(sock)[1]
    p.step = 3
    _connect(p.port).close()


def test_a_value_rewrites_one_cell_of_the_first_row_of_the_matched_query_only(proxy):
    p, _ = proxy({"query": "three", "do": {"value": "zz", "column": 1}, "times": 1})
    sock = _connect(p.port)
    for expected in (_row(b"a", b"zz", b"c"), _row(b"a", None, b"c")):
        sock.sendall(_query("SELECT three"))
        kind, body = _message(sock)
        assert kind + struct.pack("!i", len(body) + 4) + body == expected
        assert _message(sock)[0] == b"C"
        assert _message(sock) == (b"Z", b"I")
