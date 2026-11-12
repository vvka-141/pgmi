#![cfg(test)]

mod common;

use common::{Project, ident, pg};

const SHADOWS: &str = "
CREATE FUNCTION public.count(anyelement) RETURNS bigint LANGUAGE sql AS 'SELECT 0::bigint';
CREATE FUNCTION public.current_setting(anyelement) RETURNS text LANGUAGE sql AS 'SELECT NULL::text';
CREATE FUNCTION public.jsonb_array_elements(anyelement) RETURNS SETOF jsonb LANGUAGE sql AS 'SELECT NULL::jsonb WHERE false';
CREATE FUNCTION public.pg_try_advisory_lock(anyelement) RETURNS boolean LANGUAGE sql AS 'SELECT false';
CREATE FUNCTION public.hashtextextended(anyelement, anyelement) RETURNS bigint LANGUAGE sql AS 'SELECT 0::bigint';
CREATE FUNCTION public.set_config(anyelement, anyelement, anyelement) RETURNS text LANGUAGE sql AS 'SELECT NULL::text';
CREATE FUNCTION public.array_length(anyelement, anyelement) RETURNS int LANGUAGE sql AS 'SELECT 0';
CREATE FUNCTION public.coalesce(anyelement) RETURNS anyelement LANGUAGE sql AS 'SELECT $1';
CREATE FUNCTION public.string_agg(anyelement) RETURNS text LANGUAGE sql AS 'SELECT NULL::text';
CREATE FUNCTION public.jsonb_build_object(anyelement) RETURNS jsonb LANGUAGE sql AS 'SELECT NULL::jsonb';
REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
";

#[test]
fn functions_in_public_that_shadow_builtins_do_not_break_a_deploy() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_shadowed_builtins");
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    db.client().exec(SHADOWS);
    let p = Project::init("basic");
    let out = pg.deploy(&p, &db).param("env", "dev").run();
    assert_eq!(out.code, 0, "{}", out.stderr);
}
