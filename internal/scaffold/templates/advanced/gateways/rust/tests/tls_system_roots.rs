// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own tests for the gateway you write.
//
// Run with SSL_CERT_FILE=$TLS_CA, so the test server's CA is a system root:
// sslrootcert must still be the only CA the gateway trusts. CI sets
// REQUIRE_SYSTEM_ROOTS_TEST on that command only.

use pgmi_gateway::db::create_pool;

async fn connects(url: &str) -> bool {
    let Ok((pool, _)) = create_pool(url, 10) else { return false };
    pool.get().await.is_ok()
}

#[tokio::test]
async fn sslrootcert_replaces_the_system_roots() {
    let (Ok(url), Ok(_)) = (std::env::var("TLS_DATABASE_URL"), std::env::var("SSL_CERT_FILE")) else {
        assert!(std::env::var("REQUIRE_SYSTEM_ROOTS_TEST").is_err(),
            "REQUIRE_SYSTEM_ROOTS_TEST is set but TLS_DATABASE_URL or SSL_CERT_FILE is not");
        eprintln!("skipped: TLS_DATABASE_URL and SSL_CERT_FILE are not both set");
        return;
    };
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
