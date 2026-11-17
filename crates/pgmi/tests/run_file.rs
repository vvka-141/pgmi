#![cfg(test)]

mod common;

use common::{Project, ident, pg};

#[test]
fn pgmi_run_file_refuses_a_path_that_is_not_in_the_project() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_run_file_unknown_path");
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    let p = Project::empty();
    p.write(
        "deploy.sql",
        "SELECT pg_temp.pgmi_run_file('./nope.sql');\n",
    )
    .write("a.sql", "SELECT 1;");
    let out = pg.deploy(&p, &db).run();
    assert_eq!(out.code, 13, "{}", out.stderr);
    assert!(
        out.stderr
            .contains("deploy.sql runs ./nope.sql, which is not in the project"),
        "{}",
        out.stderr
    );
}
