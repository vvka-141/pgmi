#!/usr/bin/env bash
# Starts a PostgreSQL 18 that accepts only TLS (hostssl) on port $1, with a
# server certificate for localhost signed by $2/ca.pem, and writes a second,
# unrelated CA to $2/wrong-ca.pem. The gateways' TLS tests connect with
# sslmode=require, verify-full against ca.pem, and verify-full against
# wrong-ca.pem, which must fail.
#
#   internal/scaffold/testdata/gateways/tls-postgres.sh 5460 /tmp/pgmi-tls
set -euo pipefail
port=$1
dir=$2
name=pgmi-tls-postgres-$port
mkdir -p "$dir"
cd "$dir"

openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=pgmi test CA" -keyout ca.key -out ca.pem 2>/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=unrelated CA" -keyout wrong-ca.key -out wrong-ca.pem 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" -keyout server.key -out server.csr 2>/dev/null
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' > san.ext
openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 2 -extfile san.ext -out server.crt 2>/dev/null
printf 'local all all trust\nhostssl all all all scram-sha-256\n' > pg_hba.conf
chmod 644 ./*.pem server.crt server.key pg_hba.conf

docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --name "$name" -e POSTGRES_PASSWORD=postgres -p "127.0.0.1:$port:5432" -v "$dir:/tls:ro" \
    --entrypoint sh postgres:18 -c '
        cp /tls/server.key /tmp/server.key && chown postgres:postgres /tmp/server.key && chmod 600 /tmp/server.key
        exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tls/server.crt \
            -c ssl_key_file=/tmp/server.key -c hba_file=/tls/pg_hba.conf' >/dev/null
for _ in $(seq 60); do
    if docker exec "$name" pg_isready -q -h 127.0.0.1 2>/dev/null \
        && PGPASSWORD=postgres psql "host=127.0.0.1 port=$port user=postgres dbname=postgres sslmode=require" -tAc 'SELECT 1' >/dev/null 2>&1; then
        echo "TLS-only PostgreSQL on 127.0.0.1:$port, CA $dir/ca.pem"
        exit 0
    fi
    sleep 1
done
docker logs "$name" >&2
exit 1
