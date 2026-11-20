#![cfg(test)]

mod common;

use common::{Project, ident, pg};

const SHADOWS: &str = r"
CREATE FUNCTION public.shadow() RETURNS void LANGUAGE plpgsql AS
  $$BEGIN RAISE EXCEPTION 'pgmi ran a function planted in public'; END$$;
CREATE FUNCTION public.array_length(text[], integer) RETURNS integer LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN 0; END$$;
CREATE FUNCTION public.unnest(text[]) RETURNS SETOF text LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); END$$;
CREATE FUNCTION public.array_append(text[], text) RETURNS text[] LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE FUNCTION public.array_to_string(text[], text) RETURNS text LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE FUNCTION public.array_position(text[], text) RETURNS integer LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE FUNCTION public.format(text, text) RETURNS text LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE FUNCTION public.format(text, text, text, text, integer, integer) RETURNS text LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE FUNCTION public.shadow_concat(text[], text) RETURNS text[] LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE OPERATOR public.|| (LEFTARG = text[], RIGHTARG = text, FUNCTION = public.shadow_concat);
CREATE FUNCTION public.shadow_eq(text[], text[]) RETURNS boolean LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE OPERATOR public.= (LEFTARG = text[], RIGHTARG = text[], FUNCTION = public.shadow_eq);
";

const DEPLOY: &str = "
BEGIN;
SELECT * FROM pg_temp.pgmi_source_view;
SELECT * FROM pg_temp.pgmi_source_metadata_view;
SELECT * FROM pg_temp.pgmi_test_source_view;
SELECT * FROM pg_temp.pgmi_test_directory_view;
SELECT * FROM pg_temp.pgmi_plan_view;
SELECT pg_temp.pgmi_run_file('./a.sql');
CALL pgmi_test();
COMMIT;
";

const PATH_CHECK: &str = r#"
DO $$
BEGIN
    IF current_setting('search_path') <> '"$user", public' THEN
        RAISE EXCEPTION 'deploy.sql runs with search_path %', current_setting('search_path');
    END IF;
END $$;
"#;

const META: &str = "/*
<pgmi-meta id=\"1f4c3a52-8f0e-4a8e-9c49-2d3f0a5b7c11\" idempotent=\"true\">
  <sortKeys><key>001/000</key><key>002/000</key></sortKeys>
</pgmi-meta>
*/
SELECT 1;
";

#[test]
fn functions_planted_in_public_never_run_inside_pgmi_session_setup() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_shadowed_builtins");
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    db.client().exec(SHADOWS);
    let p = Project::empty();
    p.write("deploy.sql", DEPLOY)
        .write("a.sql", PATH_CHECK)
        .write("lib/sub/m.sql", META)
        .write("__test__/_setup.sql", "SELECT 1;")
        .write("__test__/test_one.sql", "SELECT 1;")
        .write("__test__/inner/test_two.sql", "SELECT 1;");
    let out = pg.deploy(&p, &db).run();
    assert_eq!(out.code, 0, "{}", out.stderr);
}

const LOCK_SHADOWS: &str = r"
CREATE FUNCTION public.shadow() RETURNS void LANGUAGE plpgsql AS
  $$BEGIN RAISE EXCEPTION 'pgmi ran an object planted in public'; END$$;
CREATE FUNCTION public.shadow_cat(name, name) RETURNS text LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE OPERATOR public.|| (LEFTARG = name, RIGHTARG = name, FUNCTION = public.shadow_cat);
";

const MANAGER_SHADOWS: &str = r"
CREATE FUNCTION public.shadow() RETURNS void LANGUAGE plpgsql AS
  $$BEGIN RAISE EXCEPTION 'pgmi ran an object planted in public'; END$$;
CREATE FUNCTION public.to_jsonb(pg_catalog.pg_database) RETURNS jsonb LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE FUNCTION public.shadow_ne(oid, integer) RETURNS boolean LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE OPERATOR public.<> (LEFTARG = oid, RIGHTARG = integer, FUNCTION = public.shadow_ne);
CREATE FUNCTION public.shadow_get(jsonb, jsonb) RETURNS text LANGUAGE plpgsql AS
  $$BEGIN PERFORM public.shadow(); RETURN NULL; END$$;
CREATE OPERATOR public.->> (LEFTARG = jsonb, RIGHTARG = jsonb, FUNCTION = public.shadow_get);
";

#[test]
fn operators_and_functions_planted_in_public_never_run_in_the_lock_or_overwrite_sql() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_shadowed_lock");
    let maintenance = pg.database("pgmi_rs_shadowed_maintenance");
    for d in [&db, &maintenance] {
        pg.admin()
            .exec(&format!("CREATE DATABASE {}", ident(d.name())));
    }
    db.client().exec(LOCK_SHADOWS);
    maintenance.client().exec(MANAGER_SHADOWS);
    let p = Project::empty();
    p.write("deploy.sql", PATH_CHECK);
    let out = pg.deploy(&p, &db).run();
    assert_eq!(out.code, 0, "{}", out.stderr);
    let (server, _) = pg
        .conn()
        .rsplit_once('/')
        .expect("PGMI_TEST_CONN names a database");
    let through = format!("{server}/{}", maintenance.name());
    let out = pg.deploy(&p, &db).connection(&through).overwrite().run();
    assert_eq!(out.code, 0, "{}", out.stderr);
}

const SETUP_PATHS: &str = r"
CREATE TABLE public.setup_path (tag text, search_path text);
CREATE FUNCTION public.record_setup_path() RETURNS event_trigger LANGUAGE plpgsql AS
  $$BEGIN
    INSERT INTO public.setup_path
    SELECT c.command_tag, pg_catalog.current_setting('search_path')
    FROM pg_catalog.pg_event_trigger_ddl_commands() c;
  END$$;
CREATE EVENT TRIGGER record_setup_path ON ddl_command_end
  EXECUTE FUNCTION public.record_setup_path();
";

#[test]
fn session_setup_runs_with_the_search_path_pinned() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_setup_search_path");
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    db.client().exec(SETUP_PATHS);
    let p = Project::empty();
    p.write("deploy.sql", PATH_CHECK);
    let out = pg.deploy(&p, &db).run();
    assert_eq!(out.code, 0, "{}", out.stderr);
    let seen: Vec<(String, String)> = db
        .client()
        .query("SELECT tag, search_path FROM public.setup_path", &[])
        .iter()
        .map(|r| (r.get(0), r.get(1)))
        .collect();
    assert!(
        seen.iter().any(|(tag, _)| tag == "CREATE TABLE"),
        "the event trigger saw no session table created: {seen:?}"
    );
    assert!(
        seen.iter().all(|(_, path)| path == "pg_catalog, pg_temp"),
        "session setup ran DDL outside the pinned search_path: {seen:?}"
    );
}
