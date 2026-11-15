#![cfg(test)]

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::process::{Command, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, Instant};

fn missing_database(db: &str) -> Vec<u8> {
    let mut body = Vec::new();
    for (field, value) in [
        (b'S', "FATAL"),
        (b'V', "FATAL"),
        (b'C', "3D000"),
        (b'M', &format!("database \"{db}\" does not exist")),
    ] {
        body.push(field);
        body.extend_from_slice(value.as_bytes());
        body.push(0);
    }
    body.push(0);
    let mut msg = vec![b'E'];
    msg.extend_from_slice(&u32::try_from(body.len() + 4).unwrap().to_be_bytes());
    msg.extend_from_slice(&body);
    msg
}

/// Signs every client in without a password, then answers its first message
/// with the header of a 2 GiB `ErrorResponse`. A client asking for database
/// `absent` is told it does not exist.
fn oversized_after_sign_in(socket: &mut TcpStream) {
    let mut buf = [0u8; 4096];
    let Ok(n) = socket.read(&mut buf) else { return };
    if buf[..n].windows(15).any(|w| w == b"database\0absent") {
        let _ = socket.write_all(&missing_database("absent"));
        return;
    }
    let ready = [b'R', 0, 0, 0, 8, 0, 0, 0, 0, b'Z', 0, 0, 0, 5, b'I'];
    if socket.write_all(&ready).is_err() || socket.read(&mut buf).is_err() {
        return;
    }
    let _ = socket.write_all(&[b'E', 0x7f, 0xff, 0xff, 0xf0]);
    std::thread::sleep(Duration::from_secs(5));
}

fn deploy(args: &[&str]) -> (Option<i32>, String, usize, Duration) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let connections = Arc::new(AtomicUsize::new(0));
    let seen = connections.clone();
    std::thread::spawn(move || {
        for socket in listener.incoming() {
            let Ok(mut socket) = socket else { return };
            seen.fetch_add(1, Ordering::SeqCst);
            std::thread::spawn(move || oversized_after_sign_in(&mut socket));
        }
    });
    let project =
        std::env::temp_dir().join(format!("pgmi-oversized-{}-{port}", std::process::id()));
    let _ = std::fs::remove_dir_all(&project);
    std::fs::create_dir_all(&project).unwrap();
    std::fs::write(project.join("deploy.sql"), "SELECT 1;\n").unwrap();
    let started = Instant::now();
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(&project)
        .args([
            "--connection",
            &format!("host=127.0.0.1 port={port} user=u dbname=m sslmode=disable"),
        ])
        .args(args)
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let _ = std::fs::remove_dir_all(&project);
    (
        out.status.code(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
        connections.load(Ordering::SeqCst),
        started.elapsed(),
    )
}

fn assert_refused_at_once(args: &[&str], connections: usize) {
    let (code, stderr, seen, took) = deploy(args);
    assert_eq!(code, Some(11), "{args:?}: {stderr}");
    assert!(
        stderr
            .contains("the server announced a 2147483632-byte 'E' message; pgmi refuses more than"),
        "{args:?}: {stderr}"
    );
    assert!(!stderr.contains("retrying in"), "{args:?}: {stderr}");
    assert_eq!(seen, connections, "{args:?}: {stderr}");
    assert!(took < Duration::from_secs(10), "{args:?}: {stderr}");
}

#[test]
fn an_oversized_message_after_sign_in_ends_the_deploy_at_once() {
    assert_refused_at_once(&["-d", "t"], 1);
}

#[test]
fn an_oversized_message_on_the_overwrite_maintenance_session_ends_the_deploy_at_once() {
    assert_refused_at_once(&["-d", "t", "--overwrite", "--force"], 1);
}

#[test]
fn an_oversized_message_on_the_session_that_creates_the_database_ends_the_deploy_at_once() {
    assert_refused_at_once(&["-d", "absent"], 2);
}
