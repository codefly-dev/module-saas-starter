#!/usr/bin/env bash
# Reproduce the source-read authorization boundary on an independent database.
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
container="source-read-test-$$-$RANDOM"
log="$(mktemp)"
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -f "$log"
}
trap cleanup EXIT
docker run -d --name "$container" -e POSTGRES_PASSWORD=source-read-local \
  -p 127.0.0.1::5432 postgres:17-alpine >/dev/null
ready=false
for attempt in $(seq 1 30); do
  if docker logs "$container" 2>&1 | grep -q 'PostgreSQL init process complete' &&
     docker exec "$container" pg_isready -U postgres >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
if [ "$ready" != true ]; then docker logs "$container"; exit 1; fi
while IFS= read -r migration; do
  if ! docker exec -i "$container" psql -U postgres -v ON_ERROR_STOP=1 < "$migration" >> "$log" 2>&1; then
    cat "$log"
    exit 1
  fi
done < <(find "$repo_root/module/services/store/migrations" -name '*.up.sql' | sort -t/ -k1,1 | awk -F/ '{print $NF " " $0}' | sort -n | cut -d' ' -f2-)
# Exercise both directions of this change before running the real authority test.
for direction in down up; do
  docker exec -i "$container" psql -U postgres -v ON_ERROR_STOP=1 \
    < "$repo_root/module/services/store/migrations/138_source_read_revisions.$direction.sql" >> "$log" 2>&1 || { cat "$log"; exit 1; }
done
# The legacy install keeps this attribute; the projection trigger must also
# work through its explicit policy when managed PostgreSQL denies bypass roles.
docker exec "$container" psql -U postgres -v ON_ERROR_STOP=1 \
  -c 'ALTER ROLE app_control_plane NOBYPASSRLS;' >> "$log" 2>&1
port="$(docker port "$container" 5432/tcp | sed 's/.*://')"
export SOURCE_READ_TEST_CONN="postgres://postgres:source-read-local@127.0.0.1:$port/postgres?sslmode=disable"
cd "$repo_root/module/services/accounts/code"
go test -race -tags integration ./pkg/adapters -run '^TestSourceReadPostgresSignedRPC$' -count=1
