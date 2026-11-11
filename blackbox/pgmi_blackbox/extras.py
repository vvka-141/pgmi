import subprocess
import uuid
from collections.abc import Callable
from contextlib import ExitStack
from pathlib import Path

from pgmi_blackbox.certs import AZURE_USER, CERT_USER, LAKEBASE_USER, MD5_USER, make_certs
from pgmi_blackbox.fakes import AZURE_TOKEN, LAKEBASE_TOKEN, start_fakes
from pgmi_blackbox.server import IMAGE, Server, wait_ready

KINDS = ("tls", "tls_only", "replica", "socket", "fakes")

_HBA = {
    "tls": (
        "local all all trust\n"
        f"host all {MD5_USER} all md5\n"
        f"hostssl all {CERT_USER} all scram-sha-256 clientcert=verify-ca\n"
        f"host all {CERT_USER} all reject\n"
        "host all all all scram-sha-256\n"
    ),
    "tls_only": "local all all trust\nhostssl all all all scram-sha-256\nhostnossl all all all reject\n",
}


def start(kind: str, certs: Path, stack: ExitStack) -> tuple[Server, dict[str, str]]:
    (certs / f"{kind}.hba").write_text(_HBA[kind])
    name = f"pgmi-blackbox-{kind}-{uuid.uuid4().hex[:8]}"
    script = (
        "mkdir -p /tls && cp /certs/server.crt /certs/server.key /certs/ca.crt /tls/ "
        f"&& cp /certs/{kind}.hba /tls/pg_hba.conf && chown -R postgres /tls && chmod 600 /tls/server.key "
        "&& exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tls/server.crt "
        "-c ssl_key_file=/tls/server.key -c ssl_ca_file=/tls/ca.crt -c hba_file=/tls/pg_hba.conf"
    )
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
            "-v",
            f"{certs}:/certs:ro",
            "-p",
            "127.0.0.1::5432",
            "--entrypoint",
            "bash",
            IMAGE,
            "-c",
            script,
        ],
        check=True,
        capture_output=True,
    )
    stack.callback(subprocess.run, ["docker", "rm", "-f", name], capture_output=True)
    mapped = subprocess.run(["docker", "port", name, "5432/tcp"], check=True, capture_output=True, text=True)
    port = int(mapped.stdout.splitlines()[0].rsplit(":", 1)[1])
    server = Server("127.0.0.1", port, "postgres", "postgres", "postgres", "sslmode=require")
    wait_ready(server)
    with server.connect() as c:
        c.execute(f"CREATE ROLE {CERT_USER} LOGIN SUPERUSER PASSWORD 'postgres'")
        c.execute("SET password_encryption = 'md5'")
        c.execute(f"CREATE ROLE {MD5_USER} LOGIN SUPERUSER PASSWORD 'postgres'")
        c.execute("SET password_encryption = 'scram-sha-256'")
        c.execute(f"CREATE ROLE {AZURE_USER} LOGIN SUPERUSER PASSWORD '{AZURE_TOKEN}'")
        c.execute(f"CREATE ROLE {LAKEBASE_USER} LOGIN SUPERUSER PASSWORD '{LAKEBASE_TOKEN}'")
    return server, {f"{{{kind}_port}}": str(port)}


def _run(*args: str) -> str:
    return subprocess.run(list(args), check=True, capture_output=True, text=True).stdout


def _port(name: str) -> int:
    return int(_run("docker", "port", name, "5432/tcp").splitlines()[0].rsplit(":", 1)[1])


def start_replica(stack: ExitStack) -> tuple[Server, dict[str, str]]:
    tag = uuid.uuid4().hex[:8]
    net, primary, standby = f"pgmi-bb-{tag}", f"pgmi-bb-primary-{tag}", f"pgmi-bb-standby-{tag}"
    _run("docker", "network", "create", net)
    stack.callback(subprocess.run, ["docker", "network", "rm", net], capture_output=True)
    _run(
        "docker",
        "run",
        "-d",
        "--rm",
        "--name",
        primary,
        "--network",
        net,
        "-e",
        "POSTGRES_PASSWORD=postgres",
        "-p",
        "127.0.0.1::5432",
        IMAGE,
        "postgres",
        "-c",
        "wal_level=replica",
        "-c",
        "max_wal_senders=5",
    )
    stack.callback(subprocess.run, ["docker", "rm", "-f", primary], capture_output=True)
    primary_server = Server("127.0.0.1", _port(primary), "postgres", "postgres", "postgres")
    wait_ready(primary_server)
    _run(
        "docker",
        "exec",
        primary,
        "bash",
        "-c",
        'echo "host replication all all scram-sha-256" >> "$PGDATA/pg_hba.conf"',
    )
    with primary_server.connect() as c:
        c.execute("SELECT pg_reload_conf()")
    script = (
        'mkdir -p "$PGDATA" && chown postgres "$PGDATA" && chmod 700 "$PGDATA" && '
        "until gosu postgres env PGPASSWORD=postgres "
        f'pg_basebackup -h {primary} -U postgres -D "$PGDATA" -R -X stream; '
        'do rm -rf "$PGDATA"/*; sleep 1; done && exec gosu postgres postgres -c hot_standby=on'
    )
    _run(
        "docker",
        "run",
        "-d",
        "--rm",
        "--name",
        standby,
        "--network",
        net,
        "-p",
        "127.0.0.1::5432",
        "--entrypoint",
        "bash",
        IMAGE,
        "-c",
        script,
    )
    stack.callback(subprocess.run, ["docker", "rm", "-f", standby], capture_output=True)
    standby_server = Server("127.0.0.1", _port(standby), "postgres", "postgres", "postgres")
    wait_ready(standby_server, 120)
    return primary_server, {"{primary_port}": str(primary_server.port), "{standby_port}": str(standby_server.port)}


def start_socket(root: Path, stack: ExitStack) -> tuple[Server, dict[str, str]]:
    sockets = root / "sockets"
    sockets.mkdir(parents=True, exist_ok=True)
    sockets.chmod(0o777)
    name = f"pgmi-bb-socket-{uuid.uuid4().hex[:8]}"
    _run(
        "docker",
        "run",
        "-d",
        "--rm",
        "--name",
        name,
        "-e",
        "POSTGRES_PASSWORD=postgres",
        "-v",
        f"{sockets}:/var/run/postgresql",
        "-p",
        "127.0.0.1::5432",
        IMAGE,
    )
    stack.callback(subprocess.run, ["docker", "stop", "-t", "20", name], capture_output=True)
    server = Server("127.0.0.1", _port(name), "postgres", "postgres", "postgres")
    wait_ready(server)
    return server, {"{socket_dir}": str(sockets)}


class Extras:
    def __init__(self, root: Path, stack: ExitStack) -> None:
        self._root = root
        self._stack = stack
        self._values: dict[str, str] = {}
        self._servers: dict[str, Server | None] = {}

    def ensure(self, kinds: tuple[str, ...]) -> tuple[dict[str, str], list[Server]]:
        if kinds and "{ca}" not in self._values:
            self._values |= make_certs(self._root / "certs")
        for kind in kinds:
            if kind not in self._servers:
                if kind == "replica":
                    server, values = start_replica(self._stack)
                elif kind == "socket":
                    server, values = start_socket(self._root, self._stack)
                elif kind == "fakes":
                    self._values |= start_fakes(self._root / "fakes", self._root / "certs")
                    self._servers[kind] = None
                    continue
                else:
                    server, values = start(kind, self._root / "certs", self._stack)
                self._servers[kind] = server
                self._values |= values
        return dict(self._values), [self._servers[k] for k in kinds if self._servers[k] is not None]


EnsureExtras = Callable[[tuple[str, ...]], tuple[dict[str, str], list[Server]]]
