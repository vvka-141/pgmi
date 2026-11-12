#![cfg(all(test, target_os = "linux"))]

mod fakes;

use std::process::Command;

use fakes::Fakes;

#[test]
fn a_service_principal_secret_signs_in_and_its_token_is_the_password() {
    let root = std::env::temp_dir().join(format!("pgmi-azure-secret-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(root.join("project")).unwrap();
    std::fs::create_dir_all(root.join("home")).unwrap();
    std::fs::write(root.join("project/deploy.sql"), "SELECT 1;\n").unwrap();
    let fakes = Fakes::start(&root).unwrap();
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(root.join("project"))
        .arg("--azure")
        .args([
            "--connection",
            &format!(
                "host=localhost port={} user=sp@tenant dbname=postgres sslmode=require",
                fakes.get("{recorder_port}")
            ),
        ])
        .args(["-d", "app"])
        .env_clear()
        .env("HOME", root.join("home"))
        .env("PATH", "/nonexistent")
        .env("SSL_CERT_FILE", root.join("certs/ca.crt"))
        .env("AZURE_AUTHORITY_HOST", fakes.get("{fake_https}"))
        .env("AZURE_TENANT_ID", "11111111-1111-1111-1111-111111111111")
        .env("AZURE_CLIENT_ID", "33333333-3333-3333-3333-333333333333")
        .env("AZURE_CLIENT_SECRET", "sp-secret-879")
        .output()
        .unwrap();
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(11), "{stderr}");
    assert!(
        stderr.contains("recorded by the black-box recorder"),
        "{stderr}"
    );
    let https = std::fs::read_to_string(fakes.get("{fake_https_log}")).unwrap();
    assert!(
        https.lines().any(|l| l
            .starts_with("POST /11111111-1111-1111-1111-111111111111/oauth2/v2.0/token ")
            && l.contains("client_id=33333333-3333-3333-3333-333333333333")
            && l.contains("client_secret=sp-secret-879")),
        "{https}"
    );
    let recorded: serde_json::Value = serde_json::from_str(
        std::fs::read_to_string(fakes.get("{recorder_log}"))
            .unwrap()
            .lines()
            .next()
            .unwrap(),
    )
    .unwrap();
    assert_eq!(recorded["user"], "sp@tenant");
    assert_eq!(recorded["password"], fakes.get("{azure_token}").as_str());
    drop(fakes);
    std::fs::remove_dir_all(&root).unwrap();
}
