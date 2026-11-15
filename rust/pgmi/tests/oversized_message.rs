#![cfg(test)]

use std::io::{Read, Write};
use std::net::TcpListener;
use std::process::{Command, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, Instant};

/// Signs every client in without a password, then answers its first message
/// with the header of a 2 GiB `ErrorResponse`.
fn oversized_after_sign_in(listener: &TcpListener, connections: &AtomicUsize) {
    for socket in listener.incoming() {
        let Ok(mut socket) = socket else { return };
        connections.fetch_add(1, Ordering::SeqCst);
        std::thread::spawn(move || {
            let mut buf = [0u8; 4096];
            if socket.read(&mut buf).is_err() {
                return;
            }
            let ready = [b'R', 0, 0, 0, 8, 0, 0, 0, 0, b'Z', 0, 0, 0, 5, b'I'];
            if socket.write_all(&ready).is_err() || socket.read(&mut buf).is_err() {
                return;
            }
            let _ = socket.write_all(&[b'E', 0x7f, 0xff, 0xff, 0xf0]);
            std::thread::sleep(Duration::from_secs(5));
        });
    }
}

#[test]
fn an_oversized_message_after_sign_in_ends_the_deploy_at_once() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let connections = Arc::new(AtomicUsize::new(0));
    let seen = connections.clone();
    std::thread::spawn(move || oversized_after_sign_in(&listener, &seen));
    let project = std::env::temp_dir().join(format!("pgmi-oversized-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&project);
    std::fs::create_dir_all(&project).unwrap();
    std::fs::write(project.join("deploy.sql"), "SELECT 1;\n").unwrap();
    let started = Instant::now();
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(&project)
        .args([
            "--connection",
            &format!("host=127.0.0.1 port={port} user=u dbname=d sslmode=disable"),
        ])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(13), "{stderr}");
    assert!(
        stderr
            .contains("the server announced a 2147483632-byte 'E' message; pgmi refuses more than"),
        "{stderr}"
    );
    assert!(!stderr.contains("retrying in"), "{stderr}");
    assert_eq!(connections.load(Ordering::SeqCst), 1, "{stderr}");
    assert!(started.elapsed() < Duration::from_secs(10), "{stderr}");
}
