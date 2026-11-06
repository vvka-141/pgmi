#!/usr/bin/env bash
# Fails unless a client's tests signed in as $1: the proof that its database
# tests ran rather than skipped for want of DATABASE_URL.
set -euo pipefail
: "${PGMI_TEST_CONN:?}"
count=$(psql "${PGMI_TEST_CONN%/*}/${2:-clients_e2e}" -tAc \
    "SELECT count(*) FROM membership.external_identity WHERE issuer_id = 'dev' AND subject = '$1'")
if [ "$count" != "1" ]; then
    echo "the $1 tests did not reach the database" >&2
    exit 1
fi
