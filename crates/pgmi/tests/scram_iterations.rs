#![cfg(test)]

mod common;

use common::{Project, ident, pg};

#[test]
fn a_role_hashed_with_six_hundred_thousand_scram_iterations_signs_in() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_scram_hardened");
    let admin = pg.admin();
    admin.exec(
        "DROP ROLE IF EXISTS pgmi_scram_hardened;
         SET scram_iterations = 600000;
         CREATE ROLE pgmi_scram_hardened LOGIN PASSWORD 'hardened-pw';
         RESET scram_iterations;",
    );
    admin.exec(&format!(
        "CREATE DATABASE {} OWNER pgmi_scram_hardened",
        ident(db.name())
    ));
    let p = Project::empty();
    p.write("deploy.sql", "SELECT 1;");
    let out = pg
        .deploy(&p, &db)
        .connection(&db.url_as("pgmi_scram_hardened", "hardened-pw"))
        .run();
    assert_eq!(out.code, 0, "{}", out.stderr);
}
