// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own tests for the gateway you write.
//
// Run with REQUIRE_SYSTEM_ROOTS_TEST=1 SSL_CERT_FILE=$TLS_CA, so the test
// server's CA is a system root: sslrootcert must still be the only CA the
// gateway trusts. SSL_CERT_FILE alone cannot switch the test on, because cargo
// sets it to the system bundle for every test it runs.

use pgmi_gateway::db::create_pool;

async fn connects(url: &str) -> bool {
    let Ok((pool, _)) = create_pool(url, 10) else { return false };
    pool.get().await.is_ok()
}

#[tokio::test]
async fn sslrootcert_replaces_the_system_roots() {
    if std::env::var("REQUIRE_SYSTEM_ROOTS_TEST").is_err() {
        eprintln!("skipped: REQUIRE_SYSTEM_ROOTS_TEST is not set");
        return;
    }
    let url = std::env::var("TLS_DATABASE_URL").expect("REQUIRE_SYSTEM_ROOTS_TEST needs TLS_DATABASE_URL");
    let wrong_ca = std::env::var("TLS_WRONG_CA").unwrap();
    let verify = url.replace("sslmode=require", "sslmode=verify-full");
    assert!(connects(&format!("{verify}&sslrootcert=system")).await,
        "the test server's CA must be a system root here, or this test proves nothing");
    for mode in ["verify-ca", "verify-full"] {
        let url = url.replace("sslmode=require", &format!("sslmode={mode}"));
        assert!(!connects(&format!("{url}&sslrootcert={wrong_ca}")).await,
            "{mode} with sslrootcert must not trust a system root");
    }
}
