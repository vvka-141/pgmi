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
