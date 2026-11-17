-- ============================================================================
-- Test: pg_temp.deploy_time_sql reads a file the way a deploy runs it
-- ============================================================================
-- The run-once vs re-run check (deploy/project_rules.sql) looks for statement
-- heads in what this lexer returns. One case per state and transition:
-- comments, strings, E-strings, quoted identifiers, dollar quotes read or
-- dropped, and the text that only looks like one of them.
-- ============================================================================

DO $test$
DECLARE
    v_case record;
    v_got  text;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('line comment',
             E'SELECT 1 -- x; ''y''\nFROM t',
             E'SELECT 1 \nFROM t'),
            ('block comment',
             $c$SELECT /* x; 'y' */ 1$c$,
             'SELECT   1'),
            ('nested block comment',
             '/* a /* b */ c */ SELECT 1',
             '  SELECT 1'),
            ('string with a doubled quote',
             $c$SELECT 'it''s -- not a comment', 2$c$,
             $c$SELECT '', 2$c$),
            ('E-string with an escaped quote',
             $c$SELECT E'a\'b -- x', 2$c$,
             $c$SELECT E'', 2$c$),
            ('E-string ending in an escaped backslash',
             $c$SELECT E'a\\', 'b' -- c$c$,
             $c$SELECT E'', '' $c$),
            ('E after an identifier character starts a plain string',
             $c$SELECT fooE'x\', 2 -- y$c$,
             $c$SELECT fooE'', 2 $c$),
            ('plain string ending in a backslash',
             $c$SELECT 'a\', 2 -- y$c$,
             $c$SELECT '', 2 $c$),
            ('quoted identifier',
             $c$SELECT "a""b -- c" FROM t$c$,
             $c$SELECT "a""b -- c" FROM t$c$),
            ('DO block: the body is read like the file',
             E'DO $$ BEGIN /* x */ CREATE TABLE t(); -- y\nEND $$;',
             E'DO $$ BEGIN   CREATE TABLE t(); \nEND $$;'),
            ('tagged DO block',
             $c$DO $do$ BEGIN PERFORM 'a'; END $do$;$c$,
             $c$DO $do$ BEGIN PERFORM ''; END $do$;$c$),
            ('another tag does not close a dollar quote',
             'DO $tag$ BEGIN EXECUTE $x$ CREATE TABLE t() $x$; END $tag$;',
             'DO $tag$ BEGIN EXECUTE $x$ CREATE TABLE t() $x$; END $tag$;'),
            ('tags are case-sensitive',
             'CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $tag$ SELECT 1 $TAG$ CREATE TABLE t() $tag$; SELECT 2',
             'CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $tag$$tag$; SELECT 2'),
            ('function body dropped',
             'CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $fn$ CREATE TABLE t() $fn$;',
             'CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $fn$$fn$;'),
            ('procedure body dropped',
             'CREATE OR REPLACE PROCEDURE p() LANGUAGE plpgsql AS $$ BEGIN CREATE TABLE t(); END $$;',
             'CREATE OR REPLACE PROCEDURE p() LANGUAGE plpgsql AS $$$$;'),
            ('positional parameters are not dollar quotes',
             'SELECT $1, $2 -- c',
             'SELECT $1, $2 '),
            ('a dollar inside an identifier is not a dollar quote',
             'SELECT a$b$c, d$$ FROM t /* c */',
             'SELECT a$b$c, d$$ FROM t  '),
            ('a semicolon starts a new statement head',
             'DO $$ BEGIN NULL; END $$; CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;',
             'DO $$ BEGIN NULL; END $$; CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$$$;'),
            ('CREATE FUNCTION that is not a statement head keeps its body',
             'SELECT 1 CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;',
             'SELECT 1 CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;'),
            ('minus, slash and star as operators',
             'SELECT 1 - 2 / 3 * 4',
             'SELECT 1 - 2 / 3 * 4'),
            ('empty text',
             '',
             '')
        ) AS c(name, input, want)
    LOOP
        v_got := pg_temp.deploy_time_sql(v_case.input);
        IF v_got IS DISTINCT FROM v_case.want THEN
            RAISE EXCEPTION 'TEST FAILED: %: deploy_time_sql(%) = %, want %',
                v_case.name, quote_literal(v_case.input), coalesce(quote_literal(v_got), 'NULL'), quote_literal(v_case.want);
        END IF;
    END LOOP;
END $test$;
