use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

use serde_json::{Value, json};

use super::embedded;

const STUB_PSYCOPG: &str = r#"import enum


class _Err(Exception):
    pass


class errors:
    SerializationFailure = _Err
    DeadlockDetected = _Err
    OperationalError = _Err
    QueryCanceled = _Err


ProgrammingError = _Err


class conninfo:
    @staticmethod
    def conninfo_to_dict(conninfo):
        return {}


class IsolationLevel(enum.IntEnum):
    READ_COMMITTED = 1
    REPEATABLE_READ = 2
    SERIALIZABLE = 3


def connect(*args, **kwargs):
    raise RuntimeError("stub psycopg: this test must not reach the database")
"#;

const STUB_JWT: &str = "class PyJWTError(Exception):\n    pass\n";

const STUB_PSYCOPG_POOL: &str = r#"class PoolTimeout(Exception):
    pass


class ConnectionPool:
    def __init__(self, *args, **kwargs):
        pass

    @staticmethod
    def check_connection(conn):
        pass

    def connection(self):
        raise RuntimeError("stub psycopg_pool: this test must not reach the database")

    def close(self):
        pass
"#;

const LOADER: &str = r#"import importlib.abc, importlib.util, json, sys
sys.dont_write_bytecode = True
_modules = json.loads(sys.stdin.readline())
class _Embedded(importlib.abc.MetaPathFinder, importlib.abc.Loader):
    def find_spec(self, name, path, target=None):
        return importlib.util.spec_from_loader(name, self) if name in _modules else None
    def create_module(self, spec):
        return None
    def exec_module(self, module):
        exec(compile(_modules[module.__name__], module.__name__ + ".py", "exec"), module.__dict__)
sys.meta_path.insert(0, _Embedded())
"#;

fn python() -> Option<&'static str> {
    ["python3", "python"].into_iter().find(|p| {
        Command::new(p)
            .arg("-V")
            .output()
            .is_ok_and(|o| o.status.success())
    })
}

fn modules() -> String {
    let mut m = serde_json::Map::new();
    m.insert("psycopg".into(), json!(STUB_PSYCOPG));
    m.insert("jwt".into(), json!(STUB_JWT));
    m.insert("psycopg_pool".into(), json!(STUB_PSYCOPG_POOL));
    for name in [
        "gateway",
        "login_transport",
        "channel",
        "interceptors",
        "resource_server",
    ] {
        m.insert(
            name.into(),
            json!(embedded(&format!(
                "templates/advanced/gateways/python/{name}.py"
            ))),
        );
    }
    Value::Object(m).to_string()
}

fn spawn(python: &str, script: &str, args: &[&str], env: &[(&str, &str)]) -> Child {
    let mut child = Command::new(python)
        .args(["-u", "-c", &format!("{LOADER}{script}")])
        .args(args)
        .env("DATABASE_URL", "postgresql://stub:stub@127.0.0.1:1/stub")
        .env("PYTHONUNBUFFERED", "1")
        .envs(env.iter().copied())
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    writeln!(stdin, "{}", modules()).unwrap();
    child
}

fn probe(python: &str, script: &str, input: &Value) -> Value {
    let out = spawn(python, script, &[&input.to_string()], &[])
        .wait_with_output()
        .unwrap();
    assert!(
        out.status.success(),
        "gateway probe failed:\n{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap_or_else(|e| {
        panic!(
            "decode probe output {:?}: {e}",
            String::from_utf8_lossy(&out.stdout)
        )
    })
}

#[test]
fn mcp_gateway_accept_honours_q_values() {
    let Some(python) = python() else {
        eprintln!("skipped: python not on PATH");
        return;
    };
    let cases = [
        ("application/json", true),
        ("application/*", true),
        ("*/*", true),
        ("application/json;q=0.5", true),
        ("text/html, application/json;q=0.9", true),
        ("APPLICATION/JSON", true),
        ("application/json ; q=0.3", true),
        ("application/json;q=0", false),
        ("application/json;q=0.0", false),
        ("*/*;q=0", false),
        ("application/*;q=0", false),
        ("text/html", false),
        ("application/json-patch+json", false),
        ("text/html, application/json;q=0", false),
        ("application/json;q=0, */*", false),
        ("application/*;q=0, application/json", true),
    ];
    let inputs: Vec<&str> = cases.iter().map(|(a, _)| *a).collect();
    let got = probe(
        python,
        "import gateway\nprint(json.dumps([gateway._accepts_json(a) for a in json.loads(sys.argv[1])]))",
        &json!(inputs),
    );
    for (i, (accept, want)) in cases.iter().enumerate() {
        assert_eq!(got[i], json!(want), "_accepts_json({accept:?})");
    }
}

#[test]
fn mcp_gateway_forwards_only_the_connection() {
    let Some(python) = python() else {
        eprintln!("skipped: python not on PATH");
        return;
    };
    let cases = [
        ("none", json!({}), json!({})),
        (
            "connection",
            json!({"X-Connection": "cn_abc"}),
            json!({"connection": "cn_abc"}),
        ),
        (
            "lower case",
            json!({"x-connection": "cn_abc"}),
            json!({"connection": "cn_abc"}),
        ),
        (
            "bearer handle",
            json!({"Authorization": "Bearer cn_def"}),
            json!({"connection": "cn_def"}),
        ),
        (
            "bearer handle wins",
            json!({"Authorization": "bearer cn_def", "X-Connection": "cn_abc"}),
            json!({"connection": "cn_def"}),
        ),
        (
            "another bearer",
            json!({"Authorization": "Bearer eyJ.x.y"}),
            json!({}),
        ),
        (
            "client-named identity and organization",
            json!({
                "X-Connection": "cn_abc",
                "X-User-Id": "https://idp.example.com|someone",
                "X-User-Email": "someone@example.com",
                "X-Tenant-Id": "00000000-0000-4000-8000-000000000001",
                "X-Organization-Id": "00000000-0000-4000-8000-000000000001",
                "X-Login-Session": "ls_abc",
                "X-Organization-Session": "os_abc",
            }),
            json!({"connection": "cn_abc"}),
        ),
        (
            "sessions alone",
            json!({"X-Login-Session": "ls_abc", "X-Organization-Session": "os_abc"}),
            json!({}),
        ),
    ];
    let inputs: Vec<&Value> = cases.iter().map(|(_, h, _)| h).collect();
    let got = probe(
        python,
        "from email.message import Message\nimport gateway\nout = []\nfor h in json.loads(sys.argv[1]):\n    m = Message()\n    for k, v in h.items():\n        m[k] = v\n    out.append(gateway._handle_context(m))\nprint(json.dumps(out))",
        &json!(inputs),
    );
    for (i, (name, _, want)) in cases.iter().enumerate() {
        assert_eq!(&got[i], want, "{name}");
    }
}

struct Gateway(Child);

impl Drop for Gateway {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

impl Gateway {
    fn output(&mut self) -> String {
        let _ = self.0.kill();
        let mut out = String::new();
        if let Some(s) = self.0.stdout.as_mut() {
            let _ = s.read_to_string(&mut out);
        }
        if let Some(s) = self.0.stderr.as_mut() {
            let _ = s.read_to_string(&mut out);
        }
        out
    }

    fn dial(&mut self, addr: &str) -> TcpStream {
        let deadline = Instant::now() + Duration::from_secs(20);
        while Instant::now() < deadline {
            if let Ok(Some(status)) = self.0.try_wait() {
                panic!(
                    "the gateway exited ({status}) before it listened on {addr}:\n{}",
                    self.output()
                );
            }
            if let Ok(c) = TcpStream::connect(addr) {
                return c;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        panic!(
            "gateway did not start listening on {addr} within 20s:\n{}",
            self.output()
        );
    }
}

#[test]
fn mcp_gateway_status_line_is_http() {
    let Some(python) = python() else {
        eprintln!("skipped: python not on PATH");
        return;
    };
    let port = TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    let port_text = port.to_string();
    let mut gateway = Gateway(spawn(
        python,
        "sys.argv = ['gateway.py']\nexec(compile(_modules['gateway'], 'gateway.py', 'exec'), {'__name__': '__main__'})",
        &[],
        &[("HOST", "127.0.0.1"), ("PORT", &port_text)],
    ));
    let addr = format!("127.0.0.1:{port}");
    let mut conn = gateway.dial(&addr);
    conn.set_read_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    let body = "not-json!";
    write!(
        conn,
        "POST /mcp HTTP/1.1\r\nHost: {addr}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    )
    .unwrap();
    let mut reader = BufReader::new(conn);
    let mut status = String::new();
    reader.read_line(&mut status).unwrap();
    let status = status.trim_end();
    assert!(
        status.starts_with("HTTP/1."),
        "status line must start with the HTTP version, got {status:?}: a reserved http.server attribute was clobbered"
    );
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).unwrap();
        let line = line.trim_end();
        if line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(':')
            && name.trim().eq_ignore_ascii_case("MCP-Protocol-Version")
        {
            assert!(
                !value.contains("HTTP/"),
                "MCP-Protocol-Version header carries an HTTP version ({:?})",
                value.trim()
            );
        }
    }
}
