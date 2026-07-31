#!/usr/bin/env bash
# Generates poc/testdata/orders.dump: a pg_dump custom-format backup of a
# 100k-row table, produced inside a temporary postgres:16 container.
set -euo pipefail
cd "$(dirname "$0")"

CTR=probavi-poc-seed
mkdir -p testdata
docker rm -f "$CTR" >/dev/null 2>&1 || true
trap 'docker rm -f "$CTR" >/dev/null 2>&1 || true' EXIT

docker run -d --name "$CTR" --label com.probavi.poc=1 \
  -e POSTGRES_PASSWORD=poc postgres:16 >/dev/null

# TCP check on purpose: during initdb the entrypoint runs a temporary server
# that listens on the unix socket only, so a socket-based pg_isready reports
# ready too early. TCP only answers once the final server is up.
until docker exec "$CTR" pg_isready -h 127.0.0.1 -U postgres -q 2>/dev/null; do
  sleep 0.5
done

docker exec "$CTR" psql -U postgres -v ON_ERROR_STOP=1 -q -c "
  CREATE TABLE orders (
    id         bigserial PRIMARY KEY,
    total      numeric(10,2) NOT NULL,
    created_at timestamptz   NOT NULL DEFAULT now()
  );
  INSERT INTO orders (total)
  SELECT (random() * 100)::numeric(10,2) FROM generate_series(1, 100000);"

docker exec "$CTR" pg_dump -U postgres -Fc -f /tmp/orders.dump postgres
docker cp "$CTR:/tmp/orders.dump" testdata/orders.dump >/dev/null

echo "fixture written: poc/testdata/orders.dump ($(du -h testdata/orders.dump | cut -f1))"
