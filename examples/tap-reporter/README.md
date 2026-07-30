# TAP 14 Reporter

Demonstrates a custom test callback that produces [TAP 14](https://testanything.org/) output from pgmi's test event stream.

## What it shows

- Custom `pgmi_tap_callback` replaces the default NOTICE-based reporter
- Plan line (`1..N`) emitted at `suite_start` via `pgmi_test_plan()` — TAP consumers detect incomplete runs if the suite aborts
- Non-transactional sequence (`pg_temp._pgmi_tap_seq`) keeps test numbers monotonic across savepoint rollbacks

## Run it

```bash
pgmi deploy . -d tap_demo --force \
  -c "postgresql://postgres:postgres@127.0.0.1:5432/postgres"
```

## Output

```
TAP version 14
1..2
ok 1 - ./__test__/test_insert.sql
ok 2 - ./__test__/test_update.sql
```

## How it works

`deploy.sql` loads `tap_callback.sql` from `pgmi_source_view`, creating the callback function and its backing sequence. Then `CALL pgmi_test(NULL, 'pg_temp.pgmi_tap_callback')` runs tests using the TAP callback instead of the default.

The callback handles three events:
- **`suite_start`**: queries `pgmi_test_plan()` for the test count, emits `TAP version 14` and `1..N`
- **`test_end`**: emits `ok N - path` (incremented via the sequence)
- All other events: ignored (fixtures, teardowns produce no TAP output)

If a test fails, pgmi's fail-fast model aborts the suite — the `test_end` event never fires for the failing test, and subsequent tests never run. A TAP consumer comparing the plan (`1..N`) against received `ok` lines detects the incomplete run.
