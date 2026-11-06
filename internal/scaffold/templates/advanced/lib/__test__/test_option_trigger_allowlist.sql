-- ============================================================================
-- Option trigger_allowlist: every trigger on a core table is listed
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy.sql.
-- A trigger is work no caller sees: a write does more than it says, and a
-- trigger that copies rows into another table keeps two truths in step by
-- hand. So each trigger on a core table is allowed by name, with the reason,
-- in pg_temp.guard_exemption() in deploy.sql:
--   ('trigger_allowlist', 'core.task trg_task_number', 'numbers each task of an organization in order')
-- A listed trigger that no longer exists fails as stale. A partition's clone
-- of its parent's trigger is the parent's.
-- ============================================================================

CREATE FUNCTION pg_temp.trigger_allowlist_violation()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT format('core.%I %I', c.relname, t.tgname)
    FROM pg_trigger t
    JOIN pg_class c ON c.oid = t.tgrelid
    WHERE c.relnamespace = 'core'::regnamespace AND NOT t.tgisinternal AND t.tgparentid = 0
$$;

DO $$
BEGIN
    IF pg_temp.guard_enabled('trigger_allowlist') THEN
        PERFORM pg_temp.enforce_guard('trigger_allowlist', ARRAY(SELECT pg_temp.trigger_allowlist_violation()));
    END IF;
END $$;

DO $$
DECLARE
    v_owner text := current_user;
    v_found text;
BEGIN
    CREATE TABLE core.trigger_probe (id int);
    CREATE FUNCTION core.trigger_probe_touch() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NEW; END $f$;
    CREATE TRIGGER trg_trigger_probe BEFORE INSERT ON core.trigger_probe FOR EACH ROW EXECUTE FUNCTION core.trigger_probe_touch();
    SELECT string_agg(v, '; ') INTO v_found FROM pg_temp.trigger_allowlist_violation() v WHERE v LIKE 'core.trigger_probe %';
    IF v_found IS DISTINCT FROM 'core.trigger_probe trg_trigger_probe' THEN
        RAISE EXCEPTION 'TEST FAILED: trigger_allowlist must name a trigger on a core table, named: %', v_found;
    END IF;
    BEGIN
        PERFORM pg_temp.enforce_guard('trigger_allowlist',
                                      ARRAY(SELECT v FROM pg_temp.trigger_allowlist_violation() v WHERE v LIKE 'core.trigger_probe %'));
        RAISE EXCEPTION 'TEST FAILED: an unlisted trigger on a core table must fail the guard';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM NOT LIKE 'Deploy guard trigger_allowlist:%core.trigger_probe trg_trigger_probe%' THEN
            RAISE;
        END IF;
    END;

    RESET ROLE;
    CREATE OR REPLACE FUNCTION pg_temp.guard_exemption()
    RETURNS TABLE (guard text, object text, reason text) LANGUAGE sql IMMUTABLE AS $f$
        VALUES ('trigger_allowlist', 'core.trigger_probe trg_trigger_probe', 'probe: allowed'),
               ('trigger_allowlist', 'core.trigger_probe trg_gone', 'probe: no such trigger')
    $f$;
    EXECUTE format('SET ROLE %I', v_owner);
    BEGIN
        PERFORM pg_temp.enforce_guard('trigger_allowlist',
                                      ARRAY(SELECT v FROM pg_temp.trigger_allowlist_violation() v WHERE v LIKE 'core.trigger_probe %'));
        RAISE EXCEPTION 'TEST FAILED: an allowlist entry naming no trigger must fail as stale';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM IS DISTINCT FROM 'Deploy guard trigger_allowlist: stale exemption(s), nothing to exempt: core.trigger_probe trg_gone' THEN
            RAISE;
        END IF;
    END;
END $$;
