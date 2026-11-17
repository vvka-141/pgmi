/*
<pgmi-meta
    id="701a29cd-d624-4a9c-b1b0-787b60828a0d"
    idempotent="true">
  <description>
    Membership value rules: email shape and normalization, clean names, dotted
    codes, organization slugs. The CHECK constraints of the structure files and
    the kernels call the same functions, so a kernel's refusal and a table's
    constraint never disagree. Sorted before the structure that uses them.
  </description>
  <sortKeys>
    <key>005/000/000</key>
  </sortKeys>
</pgmi-meta>
*/

-- CHECK constraints call these, so they must stay IMMUTABLE. Tightening one does
-- not revalidate stored rows: ship such a change with a query for the rows it
-- would now refuse.

-- Loose on purpose: the identity provider proves an address deliverable.
CREATE OR REPLACE FUNCTION membership.fn_is_well_formed_email(p_email text)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT p_email IS NOT NULL
       AND length(p_email) <= 254
       AND p_email = lower(p_email)
       AND p_email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$';
$$;

CREATE OR REPLACE FUNCTION membership.fn_normalize_email(p_email text)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT NULLIF(lower(btrim(p_email)), '');
$$;

CREATE OR REPLACE FUNCTION membership.fn_is_clean_name(p_name text, p_max_length int)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT p_name IS NOT NULL AND p_name = btrim(p_name) AND length(p_name) BETWEEN 1 AND p_max_length;
$$;

-- A stable machine code: dotted lower-case segments (org.owner, membership.member.added).
CREATE OR REPLACE FUNCTION membership.fn_is_dotted_code(p_code text)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT p_code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$' AND length(p_code) <= 100;
$$;

-- An external identity's subject is opaque to us: any provider format, pipes
-- included (samlp|connection|user), whitespace never.
CREATE OR REPLACE FUNCTION membership.fn_is_valid_subject(p_subject text)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT p_subject ~ '^\S{1,255}$';
$$;

-- Words that collide with routes and hosts.
CREATE OR REPLACE FUNCTION membership.fn_reserved_slugs()
RETURNS text[] LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ARRAY['admin', 'api', 'app', 'assets', 'auth', 'docs', 'help', 'login', 'logout', 'me',
                 'new', 'null', 'platform', 'root', 'settings', 'signin', 'signup', 'static',
                 'status', 'support', 'system', 'www'];
$$;

-- The problem code that keeps a slug from naming an organization, or NULL.
-- Whether it is taken is the unique constraint's business.
CREATE OR REPLACE FUNCTION membership.fn_slug_problem(p_slug text)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN p_slug IS NULL OR length(p_slug) NOT BETWEEN 3 AND 63 OR p_slug !~ '^[a-z0-9]+(-[a-z0-9]+)*$'
            THEN 'membership.organization.slug_invalid'
        WHEN p_slug = ANY (membership.fn_reserved_slugs())
            THEN 'membership.organization.slug_reserved'
    END;
$$;

DO $$
BEGIN
    IF NOT membership.fn_is_well_formed_email('a.b+c@example.co') OR membership.fn_is_well_formed_email('A@x.io')
       OR membership.fn_is_well_formed_email('a b@x.io') OR membership.fn_is_well_formed_email('a@x') THEN
        RAISE EXCEPTION 'TEST FAILED: membership.fn_is_well_formed_email';
    END IF;
    IF NOT membership.fn_is_valid_subject('samlp|acme-sso|alice') OR membership.fn_is_valid_subject('alice smith')
       OR membership.fn_is_valid_subject(E'alice\t') OR membership.fn_is_valid_subject('') THEN
        RAISE EXCEPTION 'TEST FAILED: membership.fn_is_valid_subject';
    END IF;
    IF membership.fn_slug_problem('acme-projects') IS NOT NULL
       OR membership.fn_slug_problem('api') IS DISTINCT FROM 'membership.organization.slug_reserved'
       OR membership.fn_slug_problem('Bad--slug') IS DISTINCT FROM 'membership.organization.slug_invalid' THEN
        RAISE EXCEPTION 'TEST FAILED: membership.fn_slug_problem';
    END IF;
END $$;
