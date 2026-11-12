# 0025: Whole-deployment retry inside the --timeout time box

Status: accepted, 2026-11-12 (owner: "1a 2a 3a"; the --overwrite decision accepted). Ticket: PGMI-882 (design); build: PGMI-886.
Owner intent 2026-11-11: pipelines need no retry step of their own; always
retry from the start; `--timeout` is the time box and its meaning is exact.

## Today

Only connecting is retried (4 tries, waits 0.1, 0.2, 0.4 s, transient
connection errors only). `--timeout` (default 3m, `0` = no limit, flag beats
`pgmi.yaml`) bounds everything after the arguments and `pgmi.yaml` are read:
loading, sign-in, connecting, the overwrite prompt, the session and
deploy.sql. Expiring while connecting is exit 11; while SQL runs, pgmi
cancels the statement, rolls back and exits 16. Same in Go and Rust except a
frozen server (PGMI-887). Full report: PGMI-882 comment of 2026-11-12.

## Decision

### A retry is a redeploy

When an attempt fails for a transient reason, pgmi closes its session and
runs the deploy again from the start, on a new connection: a new session,
the files loaded again into it, deploy.sql from its first line. That is what
every redeploy already does, so a retry adds no rule for projects: run-once
files are skipped by their ledger, everything else is rerunnable by design.
COMMIT is atomic; when the reply to a COMMIT is lost, the next attempt finds
the work done, as any redeploy would. The docs say: "a retry runs the deploy
again, exactly like a redeploy". PostgreSQL does not undo sequence advances
or effects outside the database (the existing note stays).

`--overwrite` drops and recreates once, before the first attempt; a retry
runs against the recreated database and never drops it again or asks again.

### What is retried

| Failure | Where it comes from |
|---|---|
| class 08 (connection exception), connection closed, reset or refused mid-run | network, restart, failover |
| 57P01 admin_shutdown, 57P02 crash_shutdown, 57P03 cannot_connect_now | server restart or recovery |
| 53300 too_many_connections | a full server |
| 40001 serialization_failure, 40P01 deadlock_detected | concurrency with live traffic |
| 55P03 lock_not_available | a `lock_timeout` or `NOWAIT` in a lock-safe deploy |
| 25006 read_only_sql_transaction, only while the server reports `in_hot_standby = on` | a failover left pgmi on a standby |
| a cloud sign-in or token renewal that failed after its own retries (PGMI-888) | a token endpoint down for longer |
| the connect retry itself giving up on a transient error | as above, over a longer window |

The SQLSTATE is the server's, read from the error that ended the attempt,
whatever wrapped it (a deploy.sql handler that re-raises keeps it, the
template's wrapper does since PGMI-191).

**Never retried:** any other SQL error (syntax, constraint, permission,
undefined object, a `RAISE EXCEPTION` of the project's own), every failure
inside a test (`pgmi_test()`; exit 13 with the test named in the failure
record), configuration errors (exit 10), a refused sign-in (wrong password,
401/403), and anything after a signal. 25006 on a server that is not a
standby is the project's own mistake and is final.

### The time box

`--timeout` is the wall-clock budget for the whole run: connecting, sign-in,
every attempt and every wait between attempts.

- A wait never sleeps past the deadline. If the next wait would end after it,
  pgmi stops at once and fails with the last attempt's error.
- A deadline in the middle of an attempt does what it does today: cancel the
  statement on the server, roll back, exit 16. The message counts the
  attempts: `time box of 10m used up after 3 attempts; last error: ...`.
- When the attempts end on a final error inside the box, the exit code is that
  error's (11, 13, ...), not 16.
- Default stays 3m. `--timeout 0` (no limit): retries stop after 5 attempts
  (owner choice 2).

### Backoff

Exponential with full-range jitter: the wait before attempt n+1 is
`min(30 s, 1 s * 2^(n-1))`, multiplied by a random factor in [0.8, 1.2]. So
about 1, 2, 4, 8, 16, 30, 30 s. Inside the default 3m box a deploy that fails
fast gets about 9 attempts. Each attempt keeps the connect retry's own short
waits inside it.

### Reporting

- One stderr line per failed attempt that will be retried:
  `pgmi: attempt 2 failed (40P01 deadlock detected); retrying in 4.1 s, 2m31s left`.
- Notices of every attempt are printed as they arrive, as today.
- `--json` adds `attempts` (each with its start, duration, SQLSTATE and
  message) and `timeUsedMs`; `exitCode` is the final one. `pgmi serve`'s
  deploy tool returns the same in `structuredContent`.
- Ctrl-C or SIGTERM ends the run at once, during an attempt or a wait: exit
  130, no further attempt.

### Off switch

The CLI stays infrastructure-only (`TestDeployCmdHasNoOrchestrationFlags`).
Retrying is a reaction to infrastructure faults, so it is on by default and
needs no flag. A project whose deploy must never run twice (a tail that is
not rerunnable) needs a way out: `retry: false` in `pgmi.yaml`, the project's
own property (owner choice 1).

### Docs plan

One section, "Time box and retries", staged as
`rust/docs/switch-day/timeout-and-retry.md`, the single source linked from
CLI.md (`--timeout`), CICD.md and PRODUCTION.md ("Running unattended"): the
time box, what is retried and what never is, backoff, exit codes, `--json`
attempts, and a GitHub Actions job with no retry step. A row in the
contract's "Behaviour changes in 0.20" table and a line in
`rust/docs/release-notes-0.20.md`.

## Owner's choices (2026-11-12)

1. Off switch: `retry: false` in `pgmi.yaml`.
2. No time box (`--timeout 0`): stop after 5 attempts.
3. Backoff: 1 s doubling to 30 s, jitter ±20%.

## Consequences

- Rust only (Go is feature-frozen); a 0.20 improvement over Go.
- Built in PGMI-886 on the fault-injection suite (PGMI-885): one test per
  table row, plus the time-box cases (stop at the deadline with 16, a wait cut
  short, Ctrl-C during a wait gives 130, a failing test never retried).
- The deploy loop owns the attempts; the connect retry (`conn/retry.rs`)
  stays as it is inside each attempt.
