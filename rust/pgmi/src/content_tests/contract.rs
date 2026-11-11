use std::collections::BTreeSet;

use regex_lite::Regex;
use serde_json::Value;

use super::{embedded, line_of, tree};
use crate::commands::ai::CONTRACT;

fn contract() -> Value {
    serde_json::from_str(CONTRACT).unwrap()
}

fn names(c: &Value, key: &str) -> Vec<String> {
    c[key]
        .as_array()
        .unwrap_or_else(|| panic!("contract has no {key}"))
        .iter()
        .map(|v| v["name"].as_str().unwrap().to_string())
        .collect()
}

fn strings(v: &Value) -> Vec<String> {
    v.as_array()
        .unwrap()
        .iter()
        .map(|s| s.as_str().unwrap().to_string())
        .collect()
}

fn session_sql() -> String {
    format!(
        "{}\n{}",
        embedded("session/schema.sql"),
        embedded("contract/api-v1.sql")
    )
}

#[test]
fn contract_views_functions_types_and_events_exist_in_the_session_sql() {
    let sql = session_sql();
    let c = contract();
    for kind in ["views", "functions", "types"] {
        for name in names(&c, kind) {
            assert!(
                sql.contains(&name),
                "{kind} {name:?} declared in the contract but not found in schema.sql or api-v1.sql"
            );
        }
    }
    for st in strings(&c["step_types"]) {
        assert!(
            sql.contains(&format!("'{st}'")),
            "step type {st:?} declared in the contract but not found as a literal in SQL"
        );
    }
    for t in c["types"].as_array().unwrap() {
        for ev in strings(&t["events"]) {
            assert!(
                sql.contains(&format!("'{ev}'")),
                "event {ev:?} of type {} not found as a literal in SQL",
                t["name"]
            );
        }
    }
}

#[test]
fn contract_view_columns_match_the_backing_tables() {
    let sql = session_sql();
    let column = Regex::new(
        r#"(?m)^\s+"?(\w+)"?\s+(?:TEXT|INTEGER|BIGINT|BOOLEAN|BOOL|UUID|INT|TIMESTAMPTZ|SERIAL)"#,
    )
    .unwrap();
    let backing = [
        ("pgmi_source_view", "_pgmi_source"),
        ("pgmi_parameter_view", "_pgmi_parameter"),
        ("pgmi_runtime_view", "_pgmi_runtime"),
        ("pgmi_test_source_view", "_pgmi_test_source"),
        ("pgmi_test_directory_view", "_pgmi_test_directory"),
        ("pgmi_source_metadata_view", "_pgmi_source_metadata"),
    ];
    let c = contract();
    for view in c["views"].as_array().unwrap() {
        let name = view["name"].as_str().unwrap();
        let Some((_, table)) = backing.iter().find(|(v, _)| *v == name) else {
            continue;
        };
        let start = sql
            .find(&format!("CREATE TEMP TABLE pg_temp.{table}"))
            .or_else(|| sql.find(&format!("CREATE TEMP TABLE {table}")))
            .unwrap_or_else(|| panic!("backing table {table} for view {name} not found in SQL"));
        let close = sql[start..]
            .find(");")
            .unwrap_or_else(|| panic!("could not find the end of CREATE TABLE for {table}"));
        let def = &sql[start..start + close];
        let cols: Vec<String> = column
            .captures_iter(def)
            .map(|m| m[1].to_string())
            .collect();
        assert!(!cols.is_empty(), "parsed zero columns from {table}");
        assert_eq!(
            strings(&view["columns"]),
            cols,
            "view {name}: contract columns != SQL columns (order matters)"
        );
    }
}

#[test]
fn contract_function_defaults_match_the_sql() {
    let sql = session_sql().to_lowercase();
    let c = contract();
    for f in c["functions"].as_array().unwrap() {
        for arg in strings(&f["args"]) {
            let Some((head, default)) = arg.split_once(" DEFAULT ") else {
                continue;
            };
            let param = head.split_whitespace().next().unwrap();
            let search = format!("{param} TEXT DEFAULT {}", default.trim()).to_lowercase();
            assert!(
                sql.contains(&search),
                "function {}: contract default {:?} for param {param} not found in SQL",
                f["name"],
                default.trim()
            );
        }
    }
}

#[test]
fn every_emitted_test_event_is_declared() {
    let emitted_re = Regex::new(r"ROW\(''([a-z_]+)''").unwrap();
    let body = embedded("contract/api-v1.sql");
    let emitted: BTreeSet<String> = emitted_re
        .captures_iter(&body)
        .map(|m| m[1].to_string())
        .collect();
    assert!(
        !emitted.is_empty(),
        "no ROW('' event '' ...) emissions found: has pgmi_test_generate been restructured?"
    );
    let c = contract();
    let declared: BTreeSet<String> = c["types"]
        .as_array()
        .unwrap()
        .iter()
        .flat_map(|t| strings(&t["events"]))
        .collect();
    assert!(!declared.is_empty(), "the contract declares no events");
    assert_eq!(
        emitted, declared,
        "events pgmi_test_generate emits differ from the events the contract declares"
    );
}

#[test]
fn embedded_plan_view_examples_filter_sql_files() {
    let plan_view_query =
        Regex::new(r"(?s)SELECT[^;]{0,400}?pgmi_plan_view[^;]{0,400}?(?:LOOP|\)\s*$)").unwrap();
    let exact_path = Regex::new(r"path\s*=\s*'").unwrap();
    let select_star = Regex::new(r"(?i)SELECT\s+(?:\w+\.)?\*").unwrap();
    let mut offenders = Vec::new();
    for (path, text) in tree("ai") {
        if !path.ends_with(".md") {
            continue;
        }
        for m in plan_view_query.find_iter(&text) {
            let q = m.as_str();
            if (!q.contains("content") && !select_star.is_match(q)) || exact_path.is_match(q) {
                continue;
            }
            if q.contains("is_sql_file") {
                continue;
            }
            offenders.push(format!(
                "{path}:{}  {}",
                line_of(&text, m.start()),
                q.split_whitespace().collect::<Vec<_>>().join(" ")
            ));
        }
    }
    assert!(
        offenders.is_empty(),
        "{} embedded pgmi_plan_view execute loop(s) omit the is_sql_file guard:\n  {}",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn embedded_docs_reference_only_real_session_objects() {
    let c = contract();
    let mut known: BTreeSet<String> = names(&c, "views")
        .into_iter()
        .chain(names(&c, "functions"))
        .collect();
    known.extend(
        ["pgmi_test", "pgmi_test_callback", "pgmi_test_event"]
            .iter()
            .map(|s| (*s).to_string()),
    );
    let reference = Regex::new(r"pg_temp\.(pgmi_\w+)").unwrap();
    let mut unknown = Vec::new();
    for (path, text) in tree("ai") {
        if !path.ends_with(".md") {
            continue;
        }
        for m in reference.captures_iter(&text) {
            let name = &m[1];
            if !known.contains(name) {
                let at = m.get(0).map_or(0, |g| g.start());
                unknown.push(format!("pg_temp.{name} - {path}:{}", line_of(&text, at)));
            }
        }
    }
    assert!(
        unknown.is_empty(),
        "embedded docs reference pg_temp object(s) the session never creates:\n  {}",
        unknown.join("\n  ")
    );
}
