#!/usr/bin/env bash
# Scaffolds the advanced template into $1, deploys it with env=dev into the
# database $2 and adds what the reference gateways' tests need: a gateway login
# (clients_gateway / gateway) in the api role and one MCP tool.
#
#   PGMI_BIN=./pgmi PGMI_TEST_CONN=postgresql://postgres:postgres@127.0.0.1:5441/postgres \
#     internal/scaffold/testdata/gateways/prepare.sh /tmp/project clients_e2e
set -euo pipefail
: "${PGMI_BIN:?}" "${PGMI_TEST_CONN:?must end in /<maintenance database>}"
project=$1
database=$2
here=$(cd "$(dirname "$0")" && pwd)

"$PGMI_BIN" init "$project" -t advanced >/dev/null
params=$(mktemp)
echo "env=dev" >"$params"
PGMI_CONNECTION_STRING=$PGMI_TEST_CONN "$PGMI_BIN" deploy "$project" -d "$database" --overwrite --force --params-file "$params" >/dev/null
psql "${PGMI_TEST_CONN%/*}/$database" -q -v api_role="${database}_api" -f "$here/fixture.sql" >/dev/null
