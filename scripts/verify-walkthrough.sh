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
grep -q "Test suite completed" "$logs/readme.log" || fail "README deploy did not complete its test suite"
grep -q "demo_db: 7 files loaded" "$logs/readme.log" || fail "README deploy summary line missing"

step "README: failure demo"
awk '
    /^## See it work/ { section = 1; next }
    section && /^## / { exit }
    section && $0 == "```sql" { inside = 1; first = 1; next }
    inside && /^```/ { inside = 0; next }
    inside && first {
        first = 0
        path = (sub(/^-- /, "") && $0 ~ /^(migrations|__test__)\//) ? "demo/" $0 : ""
        next
    }
    inside && path != "" { print > path }
' "$repo/README.md"
[ -f demo/migrations/003_audit_log.sql ] && [ -f demo/__test__/test_audit_log.sql ] \
    || fail "README failure-demo files not found"
expect_exit 13 "$logs/fail.log" pgmi deploy demo -d demo_db
grep -q "audit_log must contain a deploy event" "$logs/fail.log" || fail "failure demo did not report the test"
[ "$(sql demo_db "SELECT to_regclass('audit_log') IS NULL")" = t ] \
    || fail "audit_log exists after a failed deploy: the rollback did not happen"

step "Quickstart: deploy with pgmi.yaml"
unset PGMI_CONNECTION_STRING
export PGPASSWORD=postgres
pgmi init myapp --template basic >/dev/null
block_after "$repo/docs/QUICKSTART.md" "Update the database name" yaml \
    | sed "s/localhost/127.0.0.1/; s/5432/$port/" >myapp/pgmi.yaml
grep -q "database: myapp" myapp/pgmi.yaml || fail "no pgmi.yaml block after 'Update the database name' in QUICKSTART.md"
cat myapp/pgmi.yaml
cd myapp
expect_exit 0 "$logs/deploy.log" pgmi deploy . --overwrite --force
grep -q "Test suite completed" "$logs/deploy.log" || fail "Quickstart deploy did not complete its test suite"
[ "$(sql myapp "SELECT count(*) FROM get_user('admin@example.com')")" = 1 ] \
    || fail "get_user('admin@example.com') did not return the seeded admin"

step "Quickstart: --param admin_email"
expect_exit 0 "$logs/param.log" pgmi deploy . --overwrite --force --param admin_email=you@example.com
[ "$(sql myapp "SELECT count(*) FROM get_user('you@example.com')")" = 1 ] \
    || fail "--param admin_email did not reach deploy.sql"

step "Quickstart: add migrations/003_orders.sql"
block_after "$repo/docs/QUICKSTART.md" "migrations/003_orders.sql" sql >migrations/003_orders.sql
[ -s migrations/003_orders.sql ] || fail "no sql block after 'migrations/003_orders.sql' in QUICKSTART.md"
expect_exit 0 "$logs/orders.log" pgmi deploy . --overwrite --force
[ "$(sql myapp "SELECT to_regclass('\"order\"') IS NOT NULL")" = t ] || fail "order table missing"

step "Quickstart: incremental deploy and the CI form"
expect_exit 0 "$logs/incr.log" pgmi deploy .
expect_exit 0 "$logs/ci.log" pgmi deploy . -d myapp --compat 1 --force

step "Quickstart: a failing test aborts the deploy"
awk '!done && /^BEGIN/ { print; print "    RAISE EXCEPTION '\''forced failure'\'';"; done = 1; next } { print }' \
    __test__/test_user_crud.sql >"$work/forced.sql"
mv "$work/forced.sql" __test__/test_user_crud.sql
grep -q "forced failure" __test__/test_user_crud.sql || fail "could not inject the forced failure"
expect_exit 13 "$logs/forced.log" pgmi deploy .
grep -q "forced failure" "$logs/forced.log" || fail "forced failure not reported"

echo
echo "walkthrough OK"
