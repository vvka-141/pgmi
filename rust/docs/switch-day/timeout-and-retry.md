# Time box and retries

Staged for switch day (PGMI-835). The single source for `--timeout` and
whole-deployment retry; CLI.md (`--timeout`), CICD.md and PRODUCTION.md
("Running unattended") link here. Design: `rust/docs/decisions/0025-whole-deploy-retry.md`.

## The time box

`--timeout` (default `3m`; `pgmi.yaml` `timeout`; the flag wins) is the
wall-clock budget of the whole run: signing in, connecting, every attempt and
every wait between attempts. `--timeout 0` sets no time box.

## A retry is a redeploy

When an attempt fails for a transient reason, pgmi closes its session and
runs the deploy again from the start on a new connection: a new session, the
project files loaded again, deploy.sql from its first line. A retry runs the
deploy again, exactly like a redeploy: run-once files are skipped by their
ledger, everything else is rerunnable by design. When the answer to a
`COMMIT` is lost, the next attempt finds the work done, as any redeploy
would. PostgreSQL does not undo sequence advances, and nothing undoes effects
outside the database.

`--overwrite` drops and recreates the database once, before the first
attempt. A retry runs against the recreated database; it never drops it
again and never asks again.

### Retried

| Failure | Typical cause |
|---|---|
| SQLSTATE class 08; a connection closed, reset or refused mid-run | network, restart, failover |
| 57P01, 57P02, 57P03 | server shutdown, crash or recovery |
| 53300 | the server is at `max_connections` |
| 40001, 40P01 | serialization failure or deadlock with live traffic |
| 55P03 | a `lock_timeout` or `NOWAIT` in a lock-safe deploy |
| 25006, only while the server reports `in_hot_standby = on` | a failover left pgmi on a standby |
| a cloud sign-in that still fails after its own retries | the token endpoint is down |
| the connect retry giving up on a transient failure | the server stays unreachable |

Connecting has its own short retry inside each attempt (4 tries, about 0.1,
0.2 and 0.4 s apart). The SQLSTATE is the server's, read from the error that
ended the attempt, also when a deploy.sql handler re-raised it.

25006 counts only when the connection did not ask for a standby: with
`target_session_attrs` `read-only`, `standby` or `prefer-standby` a
read-only server is what was asked for, and the error is final.

### Never retried

- any other SQL error: syntax, constraint, permission, undefined object, a
  `RAISE EXCEPTION` of the project's own;
- every failure inside a test (`CALL pgmi_test()`): exit 13, the test named
  in the failure record;
- configuration errors (exit 10);
- a refused sign-in: a wrong password (28xxx), 401 or 403 from a token
  endpoint;
- another deploy holding the lock (exit 15);
- anything after Ctrl-C or SIGTERM.

## Backoff

The wait before attempt n+1 is `min(30 s, 1 s * 2^(n-1))`, times a random
factor between 0.8 and 1.2: about 1, 2, 4, 8, 16, 30, 30 s. Inside the
default 3m box a deploy that fails fast gets about 9 attempts. With
`--timeout 0` pgmi stops after 5 attempts.

Each retried attempt prints one line on stderr:

```
pgmi: attempt 2 failed (40P01 deadlock detected); retrying in 2.1s, 2m51s left
```

With no time box the line ends `3 attempts left`.

## How a run ends

| What happened | Exit code | Message |
|---|---|---|
| an attempt succeeded | 0 | |
| a failure that is never retried | its own (11, 13, ...) | the error |
| the next wait would end after the deadline | the last error's (11, 13, ...) | `time box of 3m used up after N attempts; last error: ...` |
| the deadline passed while SQL ran | 16; the statement is cancelled on the server and rolled back | the same, once an earlier attempt failed; else `the deploy exceeded --timeout ...` |
| the deadline passed while connecting | 11 | as above |
| `--timeout 0` and 5 attempts failed | the last error's | `gave up after 5 attempts with no time box (--timeout 0); last error: ...` |
| Ctrl-C or SIGTERM, during an attempt or a wait | 130 at once | `interrupted` |

## `--json`

The record (and the `structuredContent` of `pgmi serve`'s deploy tool) adds,
once an attempt started:

- `attempts`: one object per attempt, in order, with `startedAt`,
  `durationMs`, and for a failed one `sqlstate` (when the server sent one)
  and `message`;
- `timeUsedMs`: the time box used, in milliseconds.

`exitCode`, `error` and `sqlstate` are those of the run's end.

```json
"attempts": [
  {"durationMs": 1210, "message": "execute deploy.sql: connection closed", "startedAt": "2026-11-12T10:00:00.120Z"},
  {"durationMs": 1830, "startedAt": "2026-11-12T10:00:02.410Z"}
],
"timeUsedMs": 4260
```

## Turning it off

A project whose deploy must never run twice (a tail that is not rerunnable)
sets in `pgmi.yaml`:

```yaml
retry: false
```

There is no flag: the CLI handles infrastructure only, and whether a deploy
may run twice is the project's property.

## CI: no retry step

The time box replaces a retry wrapper in the pipeline. Give the job a
`timeout-minutes` above `--timeout`, so pgmi, not the runner, decides when to
stop and reports why:

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@v5
      - name: Install pgmi (pinned, checksum-verified)
        run: |
          file="pgmi_${PGMI_VERSION#v}_linux_amd64.tar.gz"
          base="https://github.com/vvka-141/pgmi/releases/download/${PGMI_VERSION}"
          curl -fsSLO "${base}/${file}"
          curl -fsSLO "${base}/checksums.txt"
          sha256sum --ignore-missing -c checksums.txt
          tar -xzf "${file}" pgmi
          sudo install pgmi /usr/local/bin/pgmi
        env:
          PGMI_VERSION: v0.20.0
      - name: Deploy
        env:
          PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}
        run: pgmi deploy . -d myapp --compat 1 --force --timeout 10m --json > deploy-result.json
```

A failover, a restart or a deadlock with live traffic inside those 10
minutes is retried by pgmi; a broken migration fails on the first attempt
with its own exit code.
