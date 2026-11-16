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

/// What the fake server answers a signed-in client's first message with.
#[derive(Clone, Copy)]
enum Answer {
    /// The header of a 2 GiB `ErrorResponse`.
    Oversized,
    /// A row description, then 60 KB rows until `FLOOD` bytes are sent.
    Rows,
    /// Empty-query answers, 5 bytes each, until `FLOOD` bytes are sent.
    Empties,
}

/// What a flooding fake sends before it gives up: far more than pgmi may
/// keep, and little enough that a pgmi without the limit ends the test
/// instead of the machine.
const FLOOD: usize = 400 << 20;

fn flood(socket: &mut TcpStream, chunk: &[u8]) {
    let mut sent = 0;
    while sent < FLOOD && socket.write_all(chunk).is_ok() {
        sent += chunk.len();
    }
}

/// Signs every client in without a password, then answers its first message
/// with `answer`. A client asking for database `absent` is told it does not
/// exist.
fn after_sign_in(socket: &mut TcpStream, answer: Answer) {
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
    let simple_query = buf[0] == b'Q';
    match answer {
        Answer::Oversized => {
            let _ = socket.write_all(&[b'E', 0x7f, 0xff, 0xff, 0xf0]);
        }
        Answer::Rows => {
            let parsed_and_bound = [b'1', 0, 0, 0, 4, b'2', 0, 0, 0, 4];
            let description = [
                if simple_query {
                    &[][..]
                } else {
                    &parsed_and_bound[..]
                },
                &[b'T', 0, 0, 0, 26, 0, 1, b'c', 0],
                &[
                    0, 0, 0, 0, 0, 0, 0, 0, 0, 25, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 0,
                ],
            ]
            .concat();
            if socket.write_all(&description).is_err() {
                return;
            }
            let value = vec![b'x'; 60_000];
            let mut row = vec![b'D'];
            row.extend_from_slice(&u32::try_from(value.len() + 10).unwrap().to_be_bytes());
            row.extend_from_slice(&[0, 1]);
            row.extend_from_slice(&u32::try_from(value.len()).unwrap().to_be_bytes());
            row.extend_from_slice(&value);
            flood(socket, &row);
        }
        Answer::Empties => flood(socket, &[b'I', 0, 0, 0, 4].repeat(4096)),
    }
    std::thread::sleep(Duration::from_secs(5));
}

fn deploy(args: &[&str]) -> (Option<i32>, String, usize, Duration) {
    deploy_against(Answer::Oversized, "", args)
}

fn deploy_against(
    answer: Answer,
    connection: &str,
    args: &[&str],
) -> (Option<i32>, String, usize, Duration) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let connections = Arc::new(AtomicUsize::new(0));
    let seen = connections.clone();
    std::thread::spawn(move || {
        for socket in listener.incoming() {
            let Ok(mut socket) = socket else { return };
            seen.fetch_add(1, Ordering::SeqCst);
            std::thread::spawn(move || after_sign_in(&mut socket, answer));
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
            &format!("host=127.0.0.1 port={port} user=u dbname=m sslmode=disable {connection}"),
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

/// With `target_session_attrs` the first query is the probe inside the
/// connect, which must name the refusal as every other session does.
#[test]
fn an_oversized_message_during_the_target_session_attrs_probe_keeps_its_reason() {
    for attrs in [
        "read-write",
        "read-only",
        "primary",
        "standby",
        "prefer-standby",
    ] {
        let (code, stderr, seen, took) = deploy_against(
            Answer::Oversized,
            &format!("target_session_attrs={attrs}"),
            &["-d", "t"],
        );
        assert_eq!(code, Some(11), "{attrs}: {stderr}");
        assert!(
            stderr.contains(
                "the server announced a 2147483632-byte 'E' message; pgmi refuses more than"
            ),
            "{attrs}: {stderr}"
        );
        for wrong in ["pg_isready", "starting up", "retrying in"] {
            assert!(!stderr.contains(wrong), "{attrs}: {wrong:?} in: {stderr}");
        }
        assert_eq!(
            seen,
            if attrs == "prefer-standby" { 2 } else { 1 },
            "{attrs}: {stderr}"
        );
        assert!(took < Duration::from_secs(10), "{attrs}: {stderr}");
    }
}

/// pgmi keeps the rows its own queries return, so a server that answers one
/// without end is refused once the answer passes what such a query returns.
#[test]
fn an_endless_answer_to_one_of_pgmis_own_queries_ends_the_deploy_at_once() {
    for (answer, connection, args, connections) in [
        (Answer::Rows, "", &["-d", "t"][..], 1),
        (Answer::Empties, "", &["-d", "t"], 1),
        (
            Answer::Rows,
            "target_session_attrs=read-write",
            &["-d", "t"],
            1,
        ),
        (
            Answer::Empties,
            "target_session_attrs=read-write",
            &["-d", "t"],
            1,
        ),
        (Answer::Rows, "", &["-d", "t", "--overwrite", "--force"], 1),
        (Answer::Rows, "", &["-d", "absent"], 2),
    ] {
        let (code, stderr, seen, took) = deploy_against(answer, connection, args);
        let case = format!("{connection:?} {args:?}");
        assert_eq!(code, Some(11), "{case}: {stderr}");
        assert!(
            stderr.contains(
                "the server sent more than 16 MiB in answer to one of pgmi's own queries, which return a few short rows; pgmi stops there"
            ),
            "{case}: {stderr}"
        );
        for wrong in ["pg_isready", "starting up", "retrying in"] {
            assert!(!stderr.contains(wrong), "{case}: {wrong:?} in: {stderr}");
        }
        assert_eq!(seen, connections, "{case}: {stderr}");
        assert!(took < Duration::from_secs(10), "{case}: {stderr}");
    }
}
