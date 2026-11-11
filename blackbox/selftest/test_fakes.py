import json
import socket
import ssl
import struct
import urllib.error
import urllib.request

import pytest

from pgmi_blackbox.certs import make_certs, openssl
from pgmi_blackbox.fakes import CLOUD_SQL_PORT, GOOGLE_TOKEN, start_fakes


@pytest.fixture(scope="module")
def fakes(tmp_path_factory):
    root = tmp_path_factory.mktemp("fakes")
    make_certs(root / "certs")
    values = start_fakes(root / "fakes", root / "certs")
    if "{google_instance}" not in values:
        pytest.skip("no loopback address free on the Cloud SQL port")
    return values, root


def _call(url: str, body: dict | None = None) -> dict:
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request) as answer:
        return json.load(answer)


def _api(values: dict) -> str:
    return f"{values['{fake_http}']}/sql/v1beta4/projects/fake-project/instances/fake-instance"


def test_google_token_and_unknown_instance(fakes):
    values, _ = fakes
    adc = json.loads(open(values["{google_credentials}"]).read())
    assert _call(adc["token_uri"], {})["access_token"] == GOOGLE_TOKEN
    with pytest.raises(urllib.error.HTTPError) as e:
        _call(_api(values).replace("fake-instance", "other") + "/connectSettings")
    assert e.value.code == 404


def test_instance_port_needs_the_ephemeral_certificate(fakes):
    values, root = fakes
    settings = _call(_api(values) + "/connectSettings")
    ip = settings["ipAddresses"][0]["ipAddress"]
    assert settings["serverCaCert"]["cert"] == open(values["{cloud_sql_ca}"]).read()
    work = root / "client"
    work.mkdir()
    openssl("genrsa", "-out", "client.key", "2048", cwd=work)
    openssl("rsa", "-in", "client.key", "-pubout", "-out", "client.pub", cwd=work)
    signed = _call(_api(values) + ":generateEphemeralCert", {"public_key": (work / "client.pub").read_text()})
    (work / "client.crt").write_text(signed["ephemeralCert"]["cert"])

    def handshake(with_cert: bool) -> bytes:
        context = ssl.create_default_context(cafile=values["{cloud_sql_ca}"])
        context.check_hostname = False
        if with_cert:
            context.load_cert_chain(work / "client.crt", work / "client.key")
        with socket.create_connection((ip, CLOUD_SQL_PORT)) as raw, context.wrap_socket(raw) as tls:
            startup = b"\0\3\0\0user\0someone\0\0"
            tls.sendall(struct.pack("!i", len(startup) + 4) + startup)
            return tls.recv(9)

    assert handshake(True) == b"R" + struct.pack("!ii", 8, 3)
    with pytest.raises((ssl.SSLError, ConnectionError)):
        handshake(False)
