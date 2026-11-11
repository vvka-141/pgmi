#![cfg(test)]

mod common;

use common::{Project, ident, pg};

#[test]
fn a_held_overwrite_lock_refuses_a_second_overwrite_with_exit_15() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_overwrite_lock");
    let project = Project::empty();
    project.write(
        "deploy.sql",
        "CREATE TABLE IF NOT EXISTS marker (n int);\nINSERT INTO marker VALUES (1);\n",
    );
    pg.deploy(&project, &db).run().ok();
    let other = pg.admin();
    other.exec(&format!(
        "SELECT pg_advisory_lock(hashtextextended('pgmi.overwrite.' || '{}', 0))",
        db.name()
    ));
    pg.deploy(&project, &db)
        .overwrite()
        .run()
        .fails(15)
        .says(&format!(
            "another pgmi --overwrite of \"{}\" is running",
            db.name()
        ));
    let marker: i64 = db.client().one("SELECT count(*) FROM marker", &[]);
    assert_eq!(
        marker,
        1,
        "the refused overwrite dropped {}",
        ident(db.name())
    );
}
