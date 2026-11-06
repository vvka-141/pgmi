#!/usr/bin/env bash
# Runs the documented first-run path against a given pgmi binary: the README
# "See it work" blocks exactly as written, the README failure demo, and the
# Quickstart deploy/verify steps. Commands, SQL and pgmi.yaml are extracted from
# the docs, so a doc edit that breaks the walkthrough fails here.
#
# Usage: scripts/verify-walkthrough.sh <path-to-pgmi-binary>
# Needs docker. WALKTHROUGH_PORT overrides the documented host port (5434).
set -euo pipefail

[ $# -eq 1 ] || { echo "usage: $0 <path-to-pgmi-binary>" >&2; exit 2; }
bin="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
repo="$(cd "$(dirname "$0")/.." && pwd)"
port="${WALKTHROUGH_PORT:-5434}"
container=pgmi-demo

if docker inspect "$container" >/dev/null 2>&1; then
    echo "a container named $container already exists; remove it first" >&2
    exit 2
fi

work="$(mktemp -d)"
cleanup() {
    docker rm -f "$container" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT

mkdir "$work/bin" "$work/logs"
cp "$bin" "$work/bin/pgmi"
export PATH="$work/bin:$PATH"
logs="$work/logs"
cd "$work"

fail() { echo "WALKTHROUGH FAILED: $*" >&2; exit 1; }
step() { echo; echo "=== $*"; }
sql() { docker exec "$container" psql -U postgres -d "$1" -Atc "$2"; }

# Prints the first N (default 1) fenced blocks of the given language after the
# first line matching the anchor.
block_after() {
    awk -v anchor="$2" -v fence='```'"$3" -v want="${4:-1}" '
        !found && index($0, anchor) { found = 1; next }
        found && !inside && $0 == fence { inside = 1; next }
        inside && /^```/ { inside = 0; if (++seen == want) exit; next }
        inside { print }
    ' "$1"
}

# expect_exit <code> <logfile> <command...>
expect_exit() {
    local want="$1" log="$2"; shift 2
    local got=0
    "$@" >"$log" 2>&1 || got=$?
    cat "$log"
    [ "$got" -eq "$want" ] || fail "'$*' exited $got, want $want"
}

step "README: See it work (verbatim)"
block_after "$repo/README.md" "## See it work" bash 2 | sed "s/5434/$port/g" >readme.sh
grep -q "pgmi deploy" readme.sh || fail "no start-and-deploy bash blocks under '## See it work' in README.md"
cat readme.sh
# bash -e so a failed `docker run` (say, the port is taken) stops the blocks
# before their deploy reaches whatever server already holds the port.
# shellcheck disable=SC2016
echo 'declare -px PGMI_CONNECTION_STRING >"$WALK_ENV"' >>readme.sh
WALK_ENV="$work/env.sh" bash -e readme.sh 2>&1 | tee "$logs/readme.log" \
    || fail "the README 'See it work' blocks failed (output above)"
# shellcheck disable=SC1091
source "$work/env.sh"
grep -q "Test suite passed" "$logs/readme.log" || fail "README deploy did not complete its test suite"
summary="$(grep -o 'demo_db: [0-9]* files loaded' "$repo/README.md" | head -n1)"
[ -n "$summary" ] || fail "no 'demo_db: N files loaded' line in README.md"
grep -q "$summary" "$logs/readme.log" || fail "README deploy did not print '$summary'"

step "README: failure demo"
awk '
    /^## See it work/ { section = 1; next }
    section && /^## / { exit }
    section && $0 == "```sql" { inside = 1; first = 1; next }
    inside && /^```/ { inside = 0; next }
    inside && first {
        first = 0
        path = (sub(/^-- /, "") && $0 ~ /^(physical|semantic|__test__)\//) ? "demo/" $0 : ""
        next
    }
    inside && path != "" { print > path }
' "$repo/README.md"
[ -f demo/physical/003_audit_log.sql ] && [ -f demo/__test__/test_audit_log.sql ] \
    || fail "README failure-demo files not found"
expect_exit 13 "$logs/fail.log" pgmi deploy demo -d demo_db --param env=dev
grep -q "audit_log must contain a deploy event" "$logs/fail.log" || fail "failure demo did not report the test"
[ "$(sql demo_db "SELECT to_regclass('audit_log') IS NULL")" = t ] \
    || fail "audit_log exists after a failed deploy: the rollback did not happen"

# doc_sql <query>: the query must appear verbatim in QUICKSTART.md, then runs it.
doc_sql() {
    grep -qF "$1" "$repo/docs/QUICKSTART.md" || fail "QUICKSTART.md no longer shows: $1"
    sql myapp "$1"
}

step "Quickstart: deploy with pgmi.yaml"
unset PGMI_CONNECTION_STRING
export PGPASSWORD=postgres
pgmi init myapp --template basic >/dev/null
block_after "$repo/docs/QUICKSTART.md" "Update the database name" yaml \
    | sed "s/localhost/127.0.0.1/; s/5432/$port/" >myapp/pgmi.yaml
grep -q "database: myapp" myapp/pgmi.yaml || fail "no pgmi.yaml block after 'Update the database name' in QUICKSTART.md"
cat myapp/pgmi.yaml
cd myapp
expect_exit 0 "$logs/deploy.log" pgmi deploy . --overwrite --force --param env=dev
grep -q "Test suite passed" "$logs/deploy.log" || fail "Quickstart deploy did not complete its test suite"

step "Quickstart: verify through the view and the writer"
view="SELECT name, open_task_count, state FROM project_view;"
[ "$(doc_sql "$view")" = "Demo|0|empty" ] || fail "project_view does not show the empty Demo project"
add="SELECT title, status FROM fn_add_task((SELECT project_id FROM project WHERE name = 'Demo'), 'Write the plan');"
[ "$(doc_sql "$add")" = "Write the plan|open" ] || fail "fn_add_task did not add the task"
[ "$(doc_sql "$view")" = "Demo|1|active" ] || fail "project_view does not show the Demo project as active"
[ "$(sql myapp "SELECT count(*) FILTER (WHERE task_id IS NULL) || '/' || count(*) FROM fn_add_task((SELECT project_id FROM project WHERE name = 'Demo'), 'Write the plan')")" = 1/1 ] \
    || fail "a duplicate fn_add_task did not return NULL, as the Quickstart says"
grep -qF "fn_task_problems(<project_id>, 'Write the plan')" "$repo/docs/QUICKSTART.md" \
    || fail "QUICKSTART.md no longer shows the fn_task_problems call"
[ "$(sql myapp "SELECT * FROM fn_task_problems((SELECT project_id FROM project WHERE name = 'Demo'), 'Write the plan')")" = "title|duplicate" ] \
    || fail "fn_task_problems does not explain the duplicate as (title, duplicate)"

step "Quickstart: an edited run-once file is refused"
cp physical/002_task.sql "$work/002_task.sql"
awk '{ print } /^CREATE TABLE task \(/ { print "    due_on date," }' "$work/002_task.sql" >physical/002_task.sql
grep -q "due_on date," physical/002_task.sql || fail "could not add due_on to physical/002_task.sql"
expect_exit 13 "$logs/drift.log" pgmi deploy . --force --param env=dev
grep -q "./physical/002_task.sql: content edited" "$logs/drift.log" || fail "the edited run-once file was not named"
[ "$(sql myapp "SELECT count(*) FROM information_schema.columns WHERE table_name = 'task' AND column_name = 'due_on'")" = 0 ] \
    || fail "due_on exists after a refused deploy"

step "Quickstart: the change as a new run-once file"
cp "$work/002_task.sql" physical/002_task.sql
block_after "$repo/docs/QUICKSTART.md" "physical/003_task_due_on.sql" sql \
    | sed "s/<your new uuid>/$(cat /proc/sys/kernel/random/uuid)/" >physical/003_task_due_on.sql
grep -q "ADD COLUMN due_on" physical/003_task_due_on.sql || fail "no sql block after 'physical/003_task_due_on.sql' in QUICKSTART.md"
expect_exit 0 "$logs/due_on.log" pgmi deploy . --force --param env=dev
[ "$(sql myapp "SELECT count(*) FROM information_schema.columns WHERE table_name = 'task' AND column_name = 'due_on'")" = 1 ] \
    || fail "due_on missing after the new run-once file"
ledger="SELECT path, ran_at FROM deployment.run_once ORDER BY ran_at;"
doc_sql "$ledger" | grep -q '^\./physical/003_task_due_on\.sql|' || fail "deployment.run_once does not record the new file"
expect_exit 0 "$logs/again.log" pgmi deploy . --force --param env=dev

step "Quickstart: the CI form"
expect_exit 0 "$logs/ci.log" pgmi deploy . -d myapp --compat 1 --force --param env=prod

step "Quickstart: a failing test aborts the deploy"
awk '!done && /^BEGIN/ { print; print "    RAISE EXCEPTION '\''forced failure'\'';"; done = 1; next } { print }' \
    semantic/__test__/test_task.sql >"$work/forced.sql"
mv "$work/forced.sql" semantic/__test__/test_task.sql
grep -q "forced failure" semantic/__test__/test_task.sql || fail "could not inject the forced failure"
expect_exit 13 "$logs/forced.log" pgmi deploy . --force --param env=dev
grep -q "forced failure" "$logs/forced.log" || fail "forced failure not reported"

echo
echo "walkthrough OK"
