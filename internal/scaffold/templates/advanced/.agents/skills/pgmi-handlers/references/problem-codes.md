# Problem codes

Every error answer is an RFC 9457 problem body:
`api.problem_response(status, title, detail, code => ..., invalid_params => ..., extensions => ...)`.
`code` is a stable machine word a client branches on; `invalid-params` is an
array of `{name, reason}` built with `api.invalid_param`. What a client does
with them (retries, which header to resend) is `gateways/PROTOCOL.md`.

## Contents

1. Application codes
2. The framework's codes
3. Uncaught errors

## 1. Application codes

- `<schema>.<entity>.<reason>`, all lower case: `core.task.not_found`,
  `core.task.closed`, `membership.member.not_found`. The schema is the
  entity's, the one its `fn_<entity>_problem` lives in, and the problem
  function answers the code the handler passes on. Never computed, never
  translated text.
- A field reason in `invalid-params` is one word (`required`, `duplicate`,
  `not_found`, `invalid`): the field already names the thing.
- Every 409 and 422 carries `code =>` or `invalid_params =>`;
  `lib/__test__/test_response_contract.sql` names any routine that answers one
  without.
- Codes starting with `pgmi.` are the framework's. A body uses one only to give
  the framework's own answer, such as `pgmi.organization_required` on an
  `authenticated` route that needs an organization.
- Extra members go in `extensions => jsonb_build_object(...)`.

## 2. The framework's codes

| Code | Status | Raised by |
|---|---|---|
| `pgmi.organization_required`, `pgmi.forbidden` | 403 | the access decision, before the body runs |
| `pgmi.invalid_path_parameter` | 400 | a malformed typed path parameter |
| `pgmi.invalid_query` + `invalid-params` | 400 | a query string that breaks `querySchema` |
| `pgmi.malformed_body`, `pgmi.invalid_body` + `invalid-params` | 400 | a body that is not JSON, or breaks `inputSchema` |
| `pgmi.invalid_pagination` + `invalid-params` | 422 | `api.pagination_params` reading a non-integer `limit`/`offset` on a route that does not declare them |
| `pgmi.transaction_isolation_too_weak` (+ `requiredIsolation`) | 428 | the route's isolation floor |
| `pgmi.transaction_read_only_required` (+ `requiredReadOnly`) | 428 | a `readOnly` route in a read-write transaction |
| `pgmi.transaction_read_write_required` | 428 | a write in a read-only transaction |

## 3. Uncaught errors

An error the body did not foresee is caught once, by the gateway. The client
gets a fixed title and detail; the logged exchange keeps the SQLSTATE. A few
SQLSTATEs map to a coded status, the rest to 500:

| SQLSTATE | Answer |
|---|---|
| `23505`, `23P01`, `55000`, `23503` on DELETE | 409 `pgmi.already_exists`, `pgmi.conflict`, `pgmi.invalid_state`, `pgmi.resource_in_use` |
| `22001`, `22003`, `22008`, `23514` | 422 `pgmi.value_too_long`, `pgmi.number_out_of_range`, `pgmi.datetime_out_of_range`, `pgmi.constraint_violation` |
| `22P02`, `22021`/`22P05`, `22023`, `22007`, `23502`, `23503` | 400 `pgmi.malformed_value`, `pgmi.invalid_encoding`, `pgmi.invalid_parameter_value`, `pgmi.malformed_datetime`, `pgmi.value_required`, `pgmi.reference_not_found` |
| `42501`, `P0002` | 403 `pgmi.forbidden`, 404 `pgmi.not_found` |
| `40001`, `40P01` | propagated, so the client gateway retries the transaction |

A coded answer from this table is a safety net, not a design: it names no
field and no rule. A handler that lets a foreseeable constraint fail here is
missing a validation or a probe.
