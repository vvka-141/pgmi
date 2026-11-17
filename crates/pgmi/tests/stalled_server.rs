#![cfg(test)]

mod common;

use std::fmt::Write as _;
use std::process::{Command, Stdio};
use std::str::FromStr;
use std::time::{Duration, Instant};

use common::{Project, pg};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::tcp::{OwnedReadHalf, OwnedWriteHalf};
use tokio::net::{TcpListener, TcpStream};
use tokio_postgres::Config;
use tokio_postgres::config::Host;

async fn forward_until_rollback(mut from: OwnedReadHalf, mut to: OwnedWriteHalf) {
    let mut buf = Vec::new();
    let mut chunk = [0u8; 8192];
    let mut started = false;
    loop {
        let n = from.read(&mut chunk).await.unwrap_or(0);
        if n == 0 {
            return;
        }
        buf.extend_from_slice(&chunk[..n]);
        loop {
            let head = usize::from(started);
            let Some(len) = buf.get(head..head + 4) else {
                break;
            };
            let total = head + u32::from_be_bytes(len.try_into().unwrap()) as usize;
            if buf.len() < total {
                break;
            }
            let frame: Vec<u8> = buf.drain(..total).collect();
            if started && frame[0] == b'Q' && &frame[5..] == b"ROLLBACK\0" {
                std::future::pending::<()>().await;
            }
            started = true;
            if to.write_all(&frame).await.is_err() {
                return;
            }
        }
    }
}

async fn proxy(listener: TcpListener, upstream: (String, u16)) {
    while let Ok((client, _)) = listener.accept().await {
        let Ok(server) = TcpStream::connect(upstream.clone()).await else {
            return;
        };
        let (client_read, mut client_write) = client.into_split();
        let (mut server_read, server_write) = server.into_split();
        tokio::spawn(forward_until_rollback(client_read, server_write));
        tokio::spawn(async move {
            let _ = tokio::io::copy(&mut server_read, &mut client_write).await;
        });
    }
}

#[test]
fn a_rollback_the_server_never_answers_does_not_hang_the_deploy() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_stalled_rollback");
    let config = Config::from_str(pg.conn()).expect("PGMI_TEST_CONN");
    let Some(Host::Tcp(host)) = config.get_hosts().first() else {
        panic!("PGMI_TEST_CONN must name a TCP host")
    };
    let port = config.get_ports().first().copied().unwrap_or(5432);
    let rt = tokio::runtime::Runtime::new().expect("runtime");
    let listener = rt
        .block_on(TcpListener::bind("127.0.0.1:0"))
        .expect("bind the proxy");
    let local = listener.local_addr().expect("proxy address");
    rt.spawn(proxy(listener, (host.clone(), port)));
    let mut url = format!(
        "postgresql://127.0.0.1:{}/postgres?sslmode=disable&user={}",
        local.port(),
        config.get_user().unwrap_or("postgres")
    );
    if let Some(password) = config.get_password() {
        let _ = write!(url, "&password={}", String::from_utf8_lossy(password));
    }
    let project = Project::empty();
    project.write("deploy.sql", "BEGIN;\nCREATE TABLE left_open (n int);\n");
    let mut child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(project.path())
        .args(["--connection", &url, "-d", db.name(), "--timeout", "0"])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run pgmi deploy");
    let started = Instant::now();
    while child.try_wait().expect("wait for pgmi").is_none() {
        if started.elapsed() > Duration::from_secs(30) {
            let _ = child.kill();
            let out = child.wait_with_output().expect("pgmi deploy output");
            panic!(
                "pgmi still waits for the ROLLBACK after 30s\n{}",
                String::from_utf8_lossy(&out.stderr)
            );
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    let out = child.wait_with_output().expect("pgmi deploy output");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(13), "{stderr}");
    assert!(
        stderr.contains("deploy.sql ended inside an open transaction"),
        "{stderr}"
    );
    let left: bool = db
        .client()
        .one("SELECT to_regclass('left_open') IS NOT NULL", &[]);
    assert!(!left, "the open transaction was committed");
}

async fn forward_until_frozen(
    mut from: OwnedReadHalf,
    mut to: OwnedWriteHalf,
    frozen: std::sync::Arc<std::sync::atomic::AtomicBool>,
) {
    let mut chunk = [0u8; 8192];
    loop {
        let n = from.read(&mut chunk).await.unwrap_or(0);
        if n == 0 {
            return;
        }
        if chunk[..n].windows(16).any(|w| w == b"pgmi_freeze_here") {
            frozen.store(true, std::sync::atomic::Ordering::SeqCst);
        }
        if frozen.load(std::sync::atomic::Ordering::SeqCst)
            || to.write_all(&chunk[..n]).await.is_err()
        {
            std::future::pending::<()>().await;
        }
    }
}

async fn freezing_proxy(listener: TcpListener, upstream: (String, u16)) {
    let frozen = std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false));
    let mut held = Vec::new();
    while let Ok((client, _)) = listener.accept().await {
        if frozen.load(std::sync::atomic::Ordering::SeqCst) {
            held.push(client);
            continue;
        }
        let Ok(server) = TcpStream::connect(upstream.clone()).await else {
            return;
        };
        let (client_read, client_write) = client.into_split();
        let (server_read, server_write) = server.into_split();
        tokio::spawn(forward_until_frozen(
            client_read,
            server_write,
            frozen.clone(),
        ));
        tokio::spawn(forward_until_frozen(
            server_read,
            client_write,
            frozen.clone(),
        ));
    }
}

#[test]
fn a_server_that_freezes_mid_deploy_ends_the_run_within_two_and_a_half_seconds_of_the_timeout() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_frozen_server");
    let config = Config::from_str(pg.conn()).expect("PGMI_TEST_CONN");
    let Some(Host::Tcp(host)) = config.get_hosts().first() else {
        panic!("PGMI_TEST_CONN must name a TCP host")
    };
    let port = config.get_ports().first().copied().unwrap_or(5432);
    let rt = tokio::runtime::Runtime::new().expect("runtime");
    let listener = rt
        .block_on(TcpListener::bind("127.0.0.1:0"))
        .expect("bind the proxy");
    let local = listener.local_addr().expect("proxy address");
    rt.spawn(freezing_proxy(listener, (host.clone(), port)));
    let mut url = format!(
        "postgresql://127.0.0.1:{}/postgres?sslmode=disable&user={}",
        local.port(),
        config.get_user().unwrap_or("postgres")
    );
    if let Some(password) = config.get_password() {
        let _ = write!(url, "&password={}", String::from_utf8_lossy(password));
    }
    let project = Project::empty();
    project.write("deploy.sql", "SELECT 'pgmi_freeze_here';\n");
    let started = Instant::now();
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(project.path())
        .args(["--connection", &url, "-d", db.name(), "--timeout", "3s"])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .output()
        .expect("run pgmi deploy");
    let took = started.elapsed();
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(16), "{stderr}");
    assert!(
        took < Duration::from_millis(5500),
        "took {took:?}\n{stderr}"
    );
}
