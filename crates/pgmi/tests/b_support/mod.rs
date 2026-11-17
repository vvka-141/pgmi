#![cfg(test)]
#![allow(dead_code)]

use serde_json::Value;

use crate::common::{Db, Outcome, Pg, Project};

pub struct Failure {
    pub code: i32,
    pub record: Value,
    pub text: String,
}

impl Failure {
    pub fn field(&self, key: &str) -> String {
        self.record[key].as_str().unwrap_or_default().to_string()
    }

    pub fn says(&self, want: &str) -> bool {
        self.text.contains(want)
    }

    pub fn hint(&self) -> String {
        self.field("hint")
    }

    pub fn sqlstate(&self) -> String {
        self.field("sqlstate")
    }
}

pub fn project(template: &str, files: &[(&str, &str)]) -> Project {
    let p = Project::init(template);
    write(&p, files);
    p
}

pub fn write(p: &Project, files: &[(&str, &str)]) {
    for (rel, content) in files {
        p.write(rel, content);
    }
}

pub fn unlink(p: &Project, rels: &[&str]) {
    for rel in rels {
        let _ = std::fs::remove_file(p.file(rel));
    }
}

pub fn deploy(
    pg: &Pg,
    p: &Project,
    db: &Db,
    overwrite: bool,
    params: &[(&str, &str)],
) -> Result<Outcome, Failure> {
    let mut d = pg.deploy(p, db).params(params).arg("--json");
    if overwrite {
        d = d.overwrite();
    }
    let out = d.run();
    if out.code == 0 {
        return Ok(out);
    }
    let record: Value = serde_json::from_str(&out.stdout).unwrap_or(Value::Null);
    let mut text = out.stderr.clone();
    for key in ["error", "detail", "hint", "message"] {
        if let Some(s) = record[key].as_str() {
            text.push('\n');
            text.push_str(s);
        }
    }
    Err(Failure {
        code: out.code,
        record,
        text,
    })
}

pub fn ok(r: Result<Outcome, Failure>, what: &str) -> Outcome {
    match r {
        Ok(o) => o,
        Err(f) => panic!("{what}: exit {}\n{}", f.code, f.text),
    }
}

pub fn refused(r: Result<Outcome, Failure>, what: &str) -> Failure {
    match r {
        Ok(o) => panic!("{what}: deploy succeeded\n{}", o.stderr),
        Err(f) => {
            assert_eq!(
                f.code, 13,
                "{what}: want the deploy refused by its SQL (exit 13)\n{}",
                f.text
            );
            f
        }
    }
}

pub fn create_database(pg: &Pg, db: &Db) {
    pg.admin().exec(&format!(
        "CREATE DATABASE {}",
        crate::common::ident(db.name())
    ));
}
