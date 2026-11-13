import subprocess
from pathlib import Path

CERT_USER = "certuser"
MD5_USER = "md5user"
AZURE_USER = "azureuser"
LAKEBASE_USER = "lakebaseuser"
KEY_PASSWORD = "key-passphrase"


def openssl(*args: str, cwd: Path) -> None:
    subprocess.run(["openssl", *args], cwd=cwd, check=True, capture_output=True)


def make_certs(d: Path) -> dict[str, str]:
    d.mkdir(parents=True, exist_ok=True)
    for ca in ("ca", "wrong_ca"):
        openssl(
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "2",
            "-subj",
            f"/CN=pgmi-blackbox-{ca}",
            "-keyout",
            f"{ca}.key",
            "-out",
            f"{ca}.crt",
            cwd=d,
        )
    leaves = {"server": ("localhost", "subjectAltName=DNS:localhost"), "client": (CERT_USER, None)}
    for name, (cn, ext) in leaves.items():
        openssl(
            "req",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-subj",
            f"/CN={cn}",
            "-keyout",
            f"{name}.key",
            "-out",
            f"{name}.csr",
            cwd=d,
        )
        args = [
            "x509",
            "-req",
            "-in",
            f"{name}.csr",
            "-CA",
            "ca.crt",
            "-CAkey",
            "ca.key",
            "-CAcreateserial",
            "-days",
            "2",
            "-out",
            f"{name}.crt",
        ]
        if ext:
            (d / f"{name}.ext").write_text(ext + "\n")
            args += ["-extfile", f"{name}.ext"]
        openssl(*args, cwd=d)
    openssl(
        "rsa",
        "-in",
        "client.key",
        "-aes256",
        "-traditional",
        "-passout",
        f"pass:{KEY_PASSWORD}",
        "-out",
        "client_enc.key",
        cwd=d,
    )
    openssl(
        "pkcs8",
        "-topk8",
        "-v1",
        "PBE-SHA1-3DES",
        "-in",
        "client.key",
        "-passout",
        f"pass:{KEY_PASSWORD}",
        "-out",
        "client_pbe3des.key",
        cwd=d,
    )
    openssl(
        "pkcs8",
        "-topk8",
        "-v2",
        "aes-256-cbc",
        "-in",
        "client.key",
        "-passout",
        f"pass:{KEY_PASSWORD}",
        "-out",
        "client_pbes2.key",
        cwd=d,
    )
    for key in d.glob("*.key"):
        key.chmod(0o600)
    openssl(
        "req", "-new", "-x509", "-days", "2", "-nodes", "-text", "-subj", "/CN=localhost",
        "-addext", "subjectAltName=DNS:localhost", "-out", "self_signed.crt", "-keyout", "self_signed.key",
        cwd=d,
    )
    return {
        "{self_signed}": str(d / "self_signed.crt"),
        "{ca}": str(d / "ca.crt"),
        "{wrong_ca}": str(d / "wrong_ca.crt"),
        "{client_cert}": str(d / "client.crt"),
        "{client_key}": str(d / "client.key"),
        "{ca_pem}": (d / "ca.crt").read_text(),
        "{wrong_ca_pem}": (d / "wrong_ca.crt").read_text(),
        "{client_cert_pem}": (d / "client.crt").read_text(),
        "{client_key_pem}": (d / "client.key").read_text(),
        "{cert_user}": CERT_USER,
        "{md5_user}": MD5_USER,
        "{azure_user}": AZURE_USER,
        "{lakebase_user}": LAKEBASE_USER,
        "{client_key_encrypted}": str(d / "client_enc.key"),
        "{client_key_pbe_sha1_3des}": str(d / "client_pbe3des.key"),
        "{client_key_pbes2}": str(d / "client_pbes2.key"),
        "{key_password}": KEY_PASSWORD,
    }
