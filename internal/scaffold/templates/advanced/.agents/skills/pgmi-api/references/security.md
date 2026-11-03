# Reviewing the API for security

Two kinds of code run in this project, with different attackers. Judge each
by its own.

## Contents

1. Deploy-time code
2. Request-time code
3. What the gateway owns
4. Findings that are not

## 1. Deploy-time code

Everything `pgmi deploy` runs: DDL, handler registrations, grants, catalogs,
seed data. It comes from the repository and runs as the deploying role.
Anyone who can change it can already run any SQL.

Flag only: a secret printed in a `RAISE NOTICE` or kept in a table after the
deploy, a secret passed as `--param` on a command line (use `PGMI_PARAM_*` or `--params-file`),
a password reaching the server log through `ALTER ROLE ... PASSWORD` under
`log_statement = ddl`.

## 2. Request-time code

Handler bodies, kernels, views and policies: every input is the attacker's.

| Flag | Because |
|---|---|
| Dynamic SQL built with `\|\|` or `format('%s')` around input | injection; use `format('%I')`, `%L` or `EXECUTE ... USING` |
| A table that is neither forced to row-level security nor exempt with a reason | another organization's rows leak |
| A row policy calling an identity reader without `(SELECT ...)` | correct but evaluated per row; `test_rls_initplan.sql` fails |
| A `SECURITY DEFINER` routine without a fixed `search_path`, or one that reaches any organization without a reach check | privilege escalation |
| A kernel that writes across organizations behind `access: public` | anyone writes |
| An identity read by name (`auth.*`, a header, a context key) | bypasses the resolved context; a deploy test fails on it |
| An access check written in the body instead of declared | the gateway, OpenAPI and the access matrix do not know it |
| 403 for a row another organization owns | confirms that it exists; answer 404 |
| `SQLERRM`, a constraint name or a table name in an answer | schema leak |
| A grant to `PUBLIC`, or to a role beyond its list | `test_exact_grants.sql` fails, and should |
| Mass assignment: a kernel that writes every key of the body | a client sets `organization_id` or `created_by_user_id`; name the columns |

## 3. What the gateway owns

TLS, rate limits, request size, CORS, the OIDC sign-in and the session
cookie. Do not flag their absence in SQL. The gateway must also drop any
client-sent session header it did not issue; `gateways/PROTOCOL.md` lists its
duties.

The database owns the rest, error sanitizing included: an error a body did
not foresee reaches the client as a fixed title and detail, and only the
logged exchange keeps the SQLSTATE.

## 4. Findings that are not

- **Handler bodies built with `format()` at registration.** A body is
  repository code, with the trust of every other deploy file; requests only
  execute registered handlers.
- **A pathological `httpMethod` regex.** Written by the developer at deploy
  time; a quality issue at most.
- **The request context in transaction-local settings.** It is cleared at
  the end of each transaction and honoured only from a login in the api
  role; a session that sets it otherwise is ignored.
