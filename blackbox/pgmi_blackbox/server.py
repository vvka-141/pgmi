import os
import subprocess
import time
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path

import psycopg
from psycopg.conninfo import conninfo_to_dict

IMAGE = "postgres:18"
REPO = Path(__file__).resolve().parents[2]


@dataclass(frozen=True, slots=True)
class Server:
    host: str
    port: int
    user: str
    password: str
    maintenance_db: str

    def uri(self, database: str | None = None) -> str:
        return f"postgresql://{self.user}:{self.password}@{self.host}:{self.port}/{database or self.maintenance_db}"

    def connect(self, database: str | None = None) -> psycopg.Connection:
        return psycopg.connect(self.uri(database), autocommit=True, connect_timeout=10)


@contextmanager
def postgres() -> Iterator[Server]:
    conn = os.environ.get("PGMI_TEST_CONN")
    if conn:
        p = conninfo_to_dict(conn)
        yield Server(
            host=p.get("host", "127.0.0.1"),
            port=int(p.get("port", 5432)),
            user=p.get("user", "postgres"),
            password=p.get("password", ""),
            maintenance_db=p.get("dbname", "postgres"),
        )
        return
    name = f"pgmi-blackbox-{os.getpid()}"
    subprocess.run(
        [
            "docker",
            "run",
            "-d",
            "--rm",
            "--name",
            name,
            "-e",
            "POSTGRES_PASSWORD=postgres",
            "-p",
            "127.0.0.1::5432",
            IMAGE,
        ],
        check=True,
        capture_output=True,
    )
    try:
        mapped = subprocess.run(["docker", "port", name, "5432/tcp"], check=True, capture_output=True, text=True)
        port = int(mapped.stdout.splitlines()[0].rsplit(":", 1)[1])
        server = Server("127.0.0.1", port, "postgres", "postgres", "postgres")
        _wait_ready(server)
        yield server
    finally:
        subprocess.run(["docker", "rm", "-f", name], capture_output=True)


def _wait_ready(server: Server, seconds: float = 60) -> None:
    deadline = time.monotonic() + seconds
    while True:
        try:
            with server.connect() as c:
                c.execute("SELECT 1")
            return
        except psycopg.OperationalError:
            if time.monotonic() > deadline:
                raise
            time.sleep(0.5)


def pgmi_binary(build_dir: Path) -> Path:
    given = os.environ.get("PGMI_BIN")
    if given:
        path = Path(given).resolve()
        if not path.is_file():
            raise FileNotFoundError(f"PGMI_BIN={given} is not a file")
        return path
    out = build_dir / "pgmi"
    subprocess.run(["go", "build", "-o", str(out), "./cmd/pgmi"], cwd=REPO, check=True)
    return out
