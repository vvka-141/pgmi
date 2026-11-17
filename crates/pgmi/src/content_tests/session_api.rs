use regex_lite::Regex;

use super::embedded;

fn api() -> String {
    embedded("contract/api-v1.sql")
}

#[test]
fn the_api_reads_internal_tables_through_pg_temp() {
    let sql = api();
    for table in [
        "_pgmi_source",
        "_pgmi_parameter",
        "_pgmi_test_source",
        "_pgmi_test_directory",
        "_pgmi_source_metadata",
    ] {
        assert!(
            sql.contains(&format!("pg_temp.{table}")),
            "api-v1.sql never names pg_temp.{table}"
        );
    }
}

#[test]
fn every_create_in_the_api_can_run_twice() {
    let bare: Vec<String> = api()
        .lines()
        .map(|l| l.trim().to_uppercase())
        .filter(|l| {
            l.starts_with("CREATE") && !l.contains("OR REPLACE") && !l.contains("CREATE TEMP")
        })
        .collect();
    assert!(
        bare.is_empty(),
        "non-idempotent CREATE in api-v1.sql: {bare:?}"
    );
}

#[test]
fn generated_test_sql_reads_the_path_from_a_setting_not_from_its_dollar_quoted_body() {
    let sql = api();
    assert!(
        !sql.contains("$__pgmi__$"),
        "a test path containing $__pgmi__$ would close the quote; pass the path through set_config"
    );
    let bodies: Vec<String> = Regex::new(r"(?s)\$pgmi_test\$(.*?)\$pgmi_test\$")
        .unwrap()
        .captures_iter(&sql)
        .map(|c| c[1].to_string())
        .collect();
    assert!(
        !bodies.is_empty(),
        "pgmi_test_generate must run each test file through a $pgmi_test$ DO block"
    );
    for b in bodies {
        assert!(
            !b.contains('%') && b.contains("current_setting(''pgmi_test.path'')"),
            "the $pgmi_test$ body must be constant and read the path from pgmi_test.path, got {b:?}"
        );
    }
}
