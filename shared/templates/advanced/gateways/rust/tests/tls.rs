// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own tests for the gateway you write.
//
// TLS_DATABASE_URL names a TLS-only server with sslmode=require
// (internal/scaffold/testdata/gateways/tls-postgres.sh in the pgmi repository);
// TLS_CA and TLS_WRONG_CA are its CA and an unrelated one.
// Without them the test returns early and says so; CI sets REQUIRE_TLS_TEST.

use pgmi_gateway::db::{create_pool, split_tls};

#[test]
fn sslmode_and_sslrootcert_leave_the_url() {
    let (url, mode, root) = split_tls("postgresql://u:p@h:5433/d?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem&options=-c%20x%3Dy");
    assert_eq!(url, "postgresql://u:p@h:5433/d?options=-c%20x%3Dy");
    assert_eq!(mode.as_deref(), Some("verify-full"));
    assert_eq!(root.as_deref(), Some("/etc/ca.pem"));
    assert!(create_pool("postgresql://u@h/d?sslmode=verify", 10).is_err());
    assert!(create_pool("postgresql://u@h/d?sslmode=verify-ca", 10).is_err(), "verify-ca without sslrootcert must refuse");
    assert!(create_pool("postgresql://u@h/d?sslmode=verify-full", 10).is_err(), "verify-full without sslrootcert must refuse");
    assert!(create_pool("postgresql://u@h/d?sslmode=require&sslrootcert=system", 10).is_err(), "sslrootcert=system is verify-full only");
    assert!(create_pool("postgresql://u@h/d?sslrootcert=system", 10).is_ok());
}

async fn encrypted(url: &str) -> Result<bool, Box<dyn std::error::Error + Send + Sync>> {
    let (pool, _) = create_pool(url, 10)?;
    let client = pool.get().await?;
    let row = client.query_one("SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()", &[]).await?;
    Ok(row.get(0))
}

#[tokio::test]
async fn tls_follows_libpq_sslmode() {
    let Ok(url) = std::env::var("TLS_DATABASE_URL") else {
        assert!(std::env::var("REQUIRE_TLS_TEST").is_err(), "REQUIRE_TLS_TEST is set but TLS_DATABASE_URL is not");
        eprintln!("skipped: TLS_DATABASE_URL is not set");
        return;
    };
    let ca = std::env::var("TLS_CA").unwrap();
    let wrong_ca = std::env::var("TLS_WRONG_CA").unwrap();
    assert!(encrypted(&url).await.unwrap(), "sslmode=require must encrypt");
    let verify = url.replace("sslmode=require", "sslmode=verify-full");
    assert!(encrypted(&format!("{verify}&sslrootcert={ca}")).await.unwrap(), "verify-full against the server's CA must connect");
    assert!(encrypted(&format!("{verify}&sslrootcert={wrong_ca}")).await.is_err(), "verify-full against an unrelated CA must fail");
}
