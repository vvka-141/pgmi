"""A fault-injecting TCP proxy in front of PostgreSQL, driven by a case's `proxy:` plan.

It reads the frontend protocol far enough to see the startup packet, each Query
or Parse text and the Terminate, and the backend's ReadyForQuery status; every
byte is otherwise passed through untouched. An SSLRequest or GSSENCRequest is
answered `N` by the proxy itself, so connections through it are plaintext.
"""

import asyncio
import re
import socket
import struct
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path

from pgmi_blackbox.case import Fault

_SSL, _GSS, _CANCEL = 80877103, 80877104, 80877102

MESSAGES = {
    "57P01": "terminating connection due to administrator command",
    "57P03": "the database system is starting up",
    "53300": "sorry, too many clients already",
    "25006": "cannot execute INSERT in a read-only transaction",
    "08P01": "invalid frontend message type 0",
}


def error_response(sqlstate: str, severity: str, message: str) -> bytes:
    text = message or MESSAGES.get(sqlstate, "injected by the black-box proxy")
    fields = ((b"S", severity), (b"V", severity), (b"C", sqlstate), (b"M", text))
    body = b"".join(k + v.encode() + b"\0" for k, v in fields) + b"\0"
    return b"E" + struct.pack("!i", len(body) + 4) + body


@dataclass
class _Conn:
    number: int
    client: asyncio.StreamWriter
    upstream: asyncio.StreamWriter | None = None
    status: bytes = b"I"
    discard_to_sync: bytes | None = None
    tasks: list[asyncio.Task] = field(default_factory=list)


class Proxy:
    def __init__(self, plan: tuple[Fault, ...], upstream: tuple[str, int], log: Path) -> None:
        self.plan = plan
        self.upstream = upstream
        self.log_path = log
        self.port = 0
        self._hits = [0] * len(plan)
        self._count = 0
        self.step = 0
        self._started = time.monotonic()
        self._loop = asyncio.new_event_loop()
        self._thread = threading.Thread(target=self._loop.run_forever, daemon=True)
        self._frozen: asyncio.Event | None = None
        self._server: asyncio.Server | None = None
        self._conns: list[_Conn] = []

    def start(self) -> "Proxy":
        self.log_path.write_text("")
        self._thread.start()
        asyncio.run_coroutine_threadsafe(self._listen(), self._loop).result(10)
        return self

    def stop(self) -> None:
        asyncio.run_coroutine_threadsafe(self._close(), self._loop).result(10)
        self._loop.call_soon_threadsafe(self._loop.stop)
        self._thread.join(10)
        self._loop.close()

    async def _listen(self) -> None:
        self._frozen = asyncio.Event()
        self._server = await asyncio.start_server(self._accept, "127.0.0.1", 0)
        self.port = self._server.sockets[0].getsockname()[1]

    async def _close(self) -> None:
        self._server.close()
        for conn in self._conns:
            for task in conn.tasks:
                task.cancel()
            for w in (conn.client, conn.upstream):
                if w:
                    w.transport.abort()

    def _log(self, conn: _Conn, text: str) -> None:
        with self.log_path.open("a") as f:
            f.write(f"{time.monotonic() - self._started:8.3f} conn {conn.number} {text}\n")

    def _match(self, conn: _Conn, at: str, sql: str = "") -> tuple[int, Fault] | None:
        for i, fault in enumerate(self.plan):
            if fault.at != at or fault.step not in (None, self.step):
                continue
            if at == "query" and not re.search(fault.query, sql):
                continue
            if fault.times is not None and self._hits[i] >= fault.times:
                continue
            self._hits[i] += 1
            self._log(conn, f"rule {i}: {fault.action}")
            return i, fault
        return None

    async def _hold(self) -> None:
        if self._frozen.is_set():
            await asyncio.Future()

    async def _accept(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        self._count += 1
        conn = _Conn(self._count, writer)
        self._conns.append(conn)
        conn.tasks.append(asyncio.current_task())
        self._log(conn, "accept")
        try:
            await self._hold()
            if await self._event(conn, "accept", b""):
                return
            await self._startup(conn, reader)
        except (OSError, asyncio.IncompleteReadError):
            self._log(conn, "gone")
            self._abort(conn)

    async def _event(self, conn: _Conn, at: str, raw: bytes, sql: str = "") -> bool:
        """Applies the first matching rule; True when the triggering message is consumed, not forwarded."""
        hit = self._match(conn, at, sql)
        if hit is None:
            return False
        _, fault = hit
        if fault.action == "delay":
            await asyncio.sleep(fault.seconds)
            return False
        if fault.after is not None:
            conn.tasks.append(asyncio.create_task(self._later(conn, fault)))
            return False
        await self._act(conn, fault, raw)
        return True

    async def _later(self, conn: _Conn, fault: Fault) -> None:
        await asyncio.sleep(fault.after)
        await self._act(conn, fault, b"")

    async def _act(self, conn: _Conn, fault: Fault, raw: bytes) -> None:
        if fault.action == "reset":
            self._abort(conn)
        elif fault.action == "freeze":
            self._frozen.set()
            await asyncio.Future()
        elif fault.severity == "ERROR" and raw[:1] == b"P":
            conn.discard_to_sync = error_response(fault.sqlstate, fault.severity, fault.message)
        elif fault.severity == "ERROR":
            after = b"E" if conn.status in (b"T", b"E") else b"I"
            conn.client.write(error_response(fault.sqlstate, fault.severity, fault.message) + _ready(after))
        else:
            conn.client.write(error_response(fault.sqlstate, fault.severity, fault.message))
            await conn.client.drain()
            conn.client.close()
            if conn.upstream:
                conn.upstream.transport.abort()

    def _abort(self, conn: _Conn) -> None:
        for w in (conn.client, conn.upstream):
            if w is None:
                continue
            sock = w.get_extra_info("socket")
            if sock is not None:
                sock.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
            w.transport.abort()

    async def _startup(self, conn: _Conn, reader: asyncio.StreamReader) -> None:
        while True:
            head = await reader.readexactly(8)
            length, code = struct.unpack("!ii", head)
            body = await reader.readexactly(length - 8)
            if code in (_SSL, _GSS):
                conn.client.write(b"N")
                continue
            break
        raw = head + body
        if code == _CANCEL:
            self._log(conn, "cancel")
        else:
            params = body.split(b"\0")
            pairs = {k.decode(): v.decode() for k, v in zip(params[0::2], params[1::2], strict=False)}
            self._log(conn, f"startup user={pairs.get('user', '')} database={pairs.get('database', '')}")
            if await self._event(conn, "startup", b""):
                return
        await self._hold()
        up_reader, conn.upstream = await asyncio.open_connection(*self.upstream)
        conn.upstream.write(raw)
        conn.tasks.append(asyncio.create_task(self._backend(conn, up_reader)))
        await self._frontend(conn, reader)

    async def _frontend(self, conn: _Conn, reader: asyncio.StreamReader) -> None:
        while True:
            try:
                head = await reader.readexactly(5)
            except asyncio.IncompleteReadError:
                await self._closing(conn, b"")
                return
            kind = head[:1]
            raw = head + await reader.readexactly(struct.unpack("!i", head[1:])[0] - 4)
            await self._hold()
            if conn.discard_to_sync is not None:
                if kind == b"S":
                    conn.client.write(conn.discard_to_sync + _ready(b"I" if conn.status == b"I" else b"E"))
                    conn.discard_to_sync = None
                continue
            if kind == b"X":
                await self._closing(conn, raw)
                return
            if kind in (b"Q", b"P"):
                sql = _text(raw)
                self._log(conn, f"query {sql[:160]!r}")
                if await self._event(conn, "query", raw, sql):
                    continue
                await self._hold()
            conn.upstream.write(raw)

    async def _closing(self, conn: _Conn, raw: bytes) -> None:
        self._log(conn, "close")
        if await self._event(conn, "close", raw):
            return
        await self._hold()
        if raw:
            conn.upstream.write(raw)
        conn.upstream.close()
        conn.client.close()

    async def _backend(self, conn: _Conn, reader: asyncio.StreamReader) -> None:
        try:
            while True:
                head = await reader.readexactly(5)
                raw = head + await reader.readexactly(struct.unpack("!i", head[1:])[0] - 4)
                await self._hold()
                if head[:1] == b"Z":
                    conn.status = raw[5:6]
                conn.client.write(raw)
        except (OSError, asyncio.IncompleteReadError):
            await self._hold()
            conn.client.close()


def _ready(status: bytes) -> bytes:
    return b"Z" + struct.pack("!i", 5) + status


def _text(raw: bytes) -> str:
    body = raw[5:]
    if raw[:1] == b"P":
        body = body.split(b"\0", 1)[1]
    return body.split(b"\0", 1)[0].decode(errors="replace")
