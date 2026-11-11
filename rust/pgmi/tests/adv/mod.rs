#![cfg(test)]
#![allow(dead_code)]

use crate::common::{Db, Outcome, Pg, Project};

pub const PW: &str = "TestPassword123!";

pub fn scaffold(files: &[(&str, &str)]) -> Project {
    let p = Project::init("advanced");
    for (rel, content) in files {
        p.write(rel, content);
    }
    p
}

fn quoted(params: &[(&str, &str)]) -> Vec<(String, String)> {
    params
        .iter()
        .map(|(k, v)| {
            assert!(
                !v.contains('\''),
                "{k}: a test value cannot hold a single quote"
            );
            ((*k).to_string(), format!("'{v}'"))
        })
        .collect()
}

pub fn deploy(
    pg: &Pg,
    project: &Project,
    db: &Db,
    overwrite: bool,
    params: &[(&str, &str)],
) -> Outcome {
    deploy_via(pg, None, project, db, overwrite, params)
}

pub fn deploy_via(
    pg: &Pg,
    connection: Option<&str>,
    project: &Project,
    db: &Db,
    overwrite: bool,
    params: &[(&str, &str)],
) -> Outcome {
    let mut d = pg.deploy(project, db).arg("--json");
    for (k, v) in quoted(params) {
        d = d.param(&k, &v);
    }
    if overwrite {
        d = d.overwrite();
    }
    if let Some(c) = connection {
        d = d.connection(c);
    }
    d.run()
}

pub fn deployed(pg: &Pg, project: &Project, db: &Db, overwrite: bool, params: &[(&str, &str)]) {
    let out = deploy(pg, project, db, overwrite, params);
    assert_eq!(out.code, 0, "deploy failed:\n{}", out.stderr);
}

pub fn error_text(out: &Outcome) -> String {
    let r = out.record();
    ["error", "detail", "hint", "where", "failedFile"]
        .iter()
        .filter_map(|k| r[*k].as_str().map(str::to_string))
        .collect::<Vec<_>>()
        .join("\n")
}

pub fn refused(out: &Outcome) -> String {
    assert_ne!(out.code, 0, "deploy succeeded; it must be refused");
    error_text(out)
}

pub fn refused_with(out: &Outcome, want: &str) -> String {
    let text = refused(out);
    assert!(text.contains(want), "want {want:?}, got:\n{text}");
    text
}

pub fn sqlstate(out: &Outcome) -> String {
    out.record()["sqlstate"].as_str().unwrap_or("").to_string()
}

pub fn probe(id: &str, idempotent: &str, sort_key: &str, body: &str) -> String {
    format!(
        "/*\n<pgmi-meta id=\"{id}\" idempotent=\"{idempotent}\">\n  <sortKeys><key>{sort_key}</key></sortKeys>\n</pgmi-meta>\n*/\n{body}"
    )
}

pub struct Roles<'a> {
    pg: &'a Pg,
    names: Vec<String>,
}

impl<'a> Roles<'a> {
    pub fn new(pg: &'a Pg, names: &[&str]) -> Roles<'a> {
        let r = Roles {
            pg,
            names: names.iter().map(|n| (*n).to_string()).collect(),
        };
        r.drop_all();
        r
    }

    pub fn drop_all(&self) {
        let admin = self.pg.admin();
        for n in &self.names {
            let _ = admin.batch(&format!("DROP ROLE IF EXISTS {n}"));
        }
    }
}

impl Drop for Roles<'_> {
    fn drop(&mut self) {
        self.drop_all();
    }
}

static SERIAL: std::sync::Mutex<()> = std::sync::Mutex::new(());

pub fn serial() -> std::sync::MutexGuard<'static, ()> {
    SERIAL
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner)
}
