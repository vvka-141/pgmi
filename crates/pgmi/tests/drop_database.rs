#![cfg(test)]

mod common;

use std::time::{Duration, Instant};

use common::{Client, Db, Pg, drop_retrying, ident, pg};

fn created(pg: &Pg, name: &str) -> Db {
    let db = pg.database(name);
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    db
}

const LIFT: &str = "ALTER SUBSCRIPTION blocker SET (slot_name = NONE); DROP SUBSCRIPTION blocker";

struct Blocker(Client);

impl Blocker {
    fn on(db: &Db) -> Self {
        let client = db.client();
        client.exec(&format!(
            "CREATE SUBSCRIPTION blocker CONNECTION 'dbname={}' PUBLICATION nothing \
             WITH (connect = false)",
            db.name()
        ));
        Blocker(client)
    }

    fn lift(&self) {
        self.0.exec(LIFT);
    }
}

impl Drop for Blocker {
    fn drop(&mut self) {
        let _ = self.0.batch(LIFT);
    }
}

fn exists(admin: &Client, name: &str) -> bool {
    admin.one(
        "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)",
        &[&name],
    )
}

#[test]
fn a_drop_refused_with_55006_is_retried_until_the_blocker_goes() {
    let Some(pg) = pg() else { return };
    let db = created(&pg, "pgmi_rs_drop_retry");
    let blocker = Blocker::on(&db);
    let admin = pg.admin();
    let refused = admin
        .batch(&format!("DROP DATABASE {} WITH (FORCE)", ident(db.name())))
        .expect_err("a subscription blocks the drop");
    assert_eq!(common::sqlstate(&refused).as_deref(), Some("55006"));

    std::thread::scope(|s| {
        s.spawn(|| {
            std::thread::sleep(Duration::from_millis(600));
            blocker.lift();
        });
        drop_retrying(&admin, db.name(), 20, Duration::from_millis(200))
            .expect("the drop succeeds once the subscription is gone");
    });
    assert!(!exists(&admin, db.name()), "{} still exists", db.name());
}

#[test]
fn a_drop_still_refused_after_the_last_try_names_the_sessions_still_connected() {
    let Some(pg) = pg() else { return };
    let db = created(&pg, "pgmi_rs_drop_holdout");
    let _blocker = Blocker::on(&db);
    let holdout = db.client();
    holdout.exec("SET application_name = 'pgmi_drop_holdout'");
    let admin = pg.admin();

    let e = drop_retrying(&admin, db.name(), 3, Duration::from_millis(100))
        .expect_err("the subscription never goes");
    assert!(e.contains("55006"), "{e}");
    assert!(e.contains("after 3 tries"), "{e}");
    assert!(e.contains("application pgmi_drop_holdout"), "{e}");
    assert!(exists(&admin, db.name()));
}

#[test]
fn a_drop_refused_for_any_other_reason_fails_on_the_first_try() {
    let Some(pg) = pg() else { return };
    let target = created(&pg, "pgmi_rs_drop_stranger_target");
    let home = created(&pg, "pgmi_rs_drop_stranger_home");
    let admin = pg.admin();
    admin.exec(
        "DROP ROLE IF EXISTS pgmi_rs_drop_stranger; \
         CREATE ROLE pgmi_rs_drop_stranger LOGIN PASSWORD 'stranger'",
    );
    let stranger = home.client_as("pgmi_rs_drop_stranger", "stranger");

    let started = Instant::now();
    let e = drop_retrying(&stranger, target.name(), 2, Duration::from_secs(10))
        .expect_err("only the owner may drop a database");
    assert!(e.contains("42501"), "{e}");
    assert!(started.elapsed() < Duration::from_secs(5), "{e}");
    assert!(exists(&admin, target.name()));

    drop(stranger);
    drop(home);
    admin.exec("DROP ROLE pgmi_rs_drop_stranger");
}
