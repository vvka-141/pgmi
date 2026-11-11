use std::collections::{BTreeMap, BTreeSet};

use serde_json::json;
use tokio_postgres::Client;
use tokio_postgres::types::Type;

use super::{Needs, sql_error, sql_error_needing};
use crate::error::{Error, Result};
use crate::project::checksum;
use crate::project::metadata::Meta;
use crate::project::walk::File;

fn shared_sql(path: &str) -> Result<&'static str> {
    pgmi_shared::file(path)
        .and_then(|b| std::str::from_utf8(b).ok())
        .ok_or_else(|| Error::internal(format!("embedded {path} is missing")))
}

async fn load_json(client: &Client, sql: &str, payload: serde_json::Value) -> Result<()> {
    let text = payload.to_string();
    client
        .query_typed(sql, &[(&text, Type::TEXT)])
        .await
        .map_err(|e| sql_error("load the project into the session", &e))?;
    Ok(())
}

pub(crate) struct Load<'a> {
    pub(crate) version: &'a str,
    pub(crate) params: &'a BTreeMap<String, String>,
    pub(crate) sources: &'a [File],
    pub(crate) tests: &'a [File],
    pub(crate) metadata: &'a [Meta],
}

const BATCH_BYTES: usize = 8 << 20;

pub(crate) async fn load(client: &Client, l: Load<'_>) -> Result<()> {
    client
        .batch_execute(shared_sql("session/schema.sql")?)
        .await
        .map_err(|e| sql_error_needing("create the session tables", &e, Some(Needs::Temporary)))?;
    load_json(
        client,
        "INSERT INTO pg_temp._pgmi_runtime (pgmi_version, contract_version) SELECT $1::text::jsonb->>0, '1'",
        json!([l.version]),
    )
    .await?;
    if !l.params.is_empty() {
        let rows: Vec<_> = l.params.iter().map(|(k, v)| json!([k, v])).collect();
        load_json(
            client,
            "WITH p AS (SELECT r->>0 AS k, r->>1 AS v FROM jsonb_array_elements($1::text::jsonb) r), \
             i AS (INSERT INTO pg_temp._pgmi_parameter (key, value) SELECT k, v FROM p) \
             SELECT set_config('pgmi.' || k, v, false) FROM p",
            json!(rows),
        )
        .await?;
    }
    let mut batch = Vec::new();
    let mut size = 0;
    for f in l.sources {
        size += f.content.len();
        batch.push(json!([
            f.path,
            f.content,
            checksum::raw(f.content.as_bytes()),
            checksum::normalized(f.content.as_bytes())
        ]));
        if size > BATCH_BYTES {
            register(client, std::mem::take(&mut batch)).await?;
            size = 0;
        }
    }
    if !batch.is_empty() {
        register(client, batch).await?;
    }
    if !l.metadata.is_empty() {
        let rows: Vec<_> = l
            .metadata
            .iter()
            .map(|m| json!({"path": m.path, "id": m.id, "idempotent": m.idempotent, "sort_keys": m.sort_keys, "description": m.description}))
            .collect();
        load_json(
            client,
            "INSERT INTO pg_temp._pgmi_source_metadata (path, id, idempotent, sort_keys, description) \
             SELECT m->>'path', (m->>'id')::uuid, (m->>'idempotent')::boolean, \
                    ARRAY(SELECT jsonb_array_elements_text(m->'sort_keys')), m->>'description' \
             FROM jsonb_array_elements($1::text::jsonb) m",
            json!(rows),
        )
        .await?;
    }
    if !l.tests.is_empty() {
        load_json(
            client,
            "INSERT INTO pg_temp._pgmi_test_directory (path, parent_path, depth) \
             SELECT d->>0, d->>1, (d->>2)::int FROM jsonb_array_elements($1::text::jsonb) d",
            json!(test_directories(l.tests)),
        )
        .await?;
        let rows: Vec<_> = l
            .tests
            .iter()
            .map(|f| {
                let (dir, name) = split_path(&f.path);
                let fixture = name.eq_ignore_ascii_case("_setup.sql")
                    || name.eq_ignore_ascii_case("_setup.psql");
                json!([f.path, dir, name, f.content, fixture])
            })
            .collect();
        load_json(
            client,
            "INSERT INTO pg_temp._pgmi_test_source (path, directory, filename, content, is_fixture) \
             SELECT t->>0, t->>1, t->>2, t->>3, (t->>4)::boolean FROM jsonb_array_elements($1::text::jsonb) t",
            json!(rows),
        )
        .await?;
    }
    client
        .batch_execute(shared_sql("contract/api-v1.sql")?)
        .await
        .map_err(|e| sql_error("create the session API views", &e))?;
    Ok(())
}

async fn register(client: &Client, rows: Vec<serde_json::Value>) -> Result<()> {
    load_json(
        client,
        "SELECT count(pg_temp.pgmi_register_file(f->>0, f->>1, f->>2, f->>3)) FROM jsonb_array_elements($1::text::jsonb) f",
        json!(rows),
    )
    .await
}

fn split_path(path: &str) -> (&str, &str) {
    match path.rfind('/') {
        Some(i) => (&path[..=i], &path[i + 1..]),
        None => ("./", path),
    }
}

fn test_depth(dir: &str) -> usize {
    let parts: Vec<&str> = dir.trim_end_matches('/').split('/').collect();
    let first = parts
        .iter()
        .position(|p| *p == "__test__" || *p == "__tests__")
        .unwrap_or(parts.len());
    parts.len().saturating_sub(first + 1)
}

pub(crate) fn test_directories(tests: &[File]) -> Vec<(String, Option<String>, usize)> {
    let dirs: BTreeSet<&str> = tests.iter().map(|f| split_path(&f.path).0).collect();
    dirs.iter()
        .map(|d| {
            let mut parent = None;
            let mut cut = d.trim_end_matches('/');
            while let Some(i) = cut.rfind('/') {
                cut = &cut[..i];
                let candidate = &d[..=i];
                if dirs.contains(candidate) {
                    parent = Some(candidate.to_string());
                    break;
                }
            }
            (d.to_string(), parent, test_depth(d))
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_tree() {
        let f = |p: &str| File {
            path: p.into(),
            content: String::new(),
        };
        let dirs = test_directories(&[
            f("./__test__/t.sql"),
            f("./failing/__test__/t.sql"),
            f("./failing/__test__/deep/__test__/t.sql"),
            f("./x/__tests__/sub/t.sql"),
        ]);
        assert_eq!(
            dirs,
            vec![
                ("./__test__/".into(), None, 0),
                ("./failing/__test__/".into(), None, 0),
                (
                    "./failing/__test__/deep/__test__/".into(),
                    Some("./failing/__test__/".into()),
                    2
                ),
                ("./x/__tests__/sub/".into(), None, 1),
            ]
        );
    }
}
