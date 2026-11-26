#!/usr/bin/env bash
# Run the Tapas memory client and service against the real mem0 pack: its server
# image with the allowlist, pgvector, and the pack's fake model, all in Docker.
# It mirrors the pack's own image/smoke.sh. Nothing is published beyond 127.0.0.1,
# and every container, network and image made here is removed when the script ends.
#
# MEM0_PACK_REF: the mem0-pack commit to test against.
# MEM0_PACK_DIR: an existing checkout to use instead of cloning (left unmodified).
#
# Usage: scripts/test/mem0-integration.sh
set -euo pipefail

# The commit of release tag nebari-mem0-0.1.0-alpha.1 on nebari-dev/mem0-pack main.
MEM0_PACK_REF="${MEM0_PACK_REF:-af0334448b49b7535efd11efe815d511c9a49f43}"
# The pack's own pgvector pin (image/smoke.sh).
PGVECTOR_IMAGE="pgvector/pgvector:pg17@sha256:b513c48566e4777cd3370451400000c78eda6a08131d68f4c31d4a76f27bd8fd"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUFFIX="$(head -c 4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
NET="javo-mem0-it-$SUFFIX"
PG="javo-mem0-it-pg-$SUFFIX"
MODEL="javo-mem0-it-model-$SUFFIX"
SERVER="javo-mem0-it-server-$SUFFIX"
IMAGE="javo-mem0-it-server:$SUFFIX"
FAKE_IMAGE="javo-mem0-it-fake-model:$SUFFIX"
DIMS=384
API_KEY="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
tmp=""

cleanup() {
  docker rm -f "$SERVER" "$PG" "$MODEL" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker rmi "$IMAGE" "$FAKE_IMAGE" >/dev/null 2>&1 || true
  if [ -n "$tmp" ]; then rm -rf "$tmp"; fi
}
trap cleanup EXIT

fail() { echo "MEM0 INTEGRATION FAIL: $*" >&2; exit 1; }

if [ -n "${MEM0_PACK_DIR:-}" ]; then
  pack="$MEM0_PACK_DIR"
else
  tmp="$(mktemp -d)"
  pack="$tmp/mem0-pack"
  git init -q "$pack"
  git -C "$pack" fetch -q --depth 1 https://github.com/nebari-dev/mem0-pack "$MEM0_PACK_REF"
  git -C "$pack" checkout -q FETCH_HEAD
fi

echo "== build"
docker build -q -t "$IMAGE" "$pack/image" >/dev/null
docker build -q -t "$FAKE_IMAGE" "$pack/tests/e2e/fake-model" >/dev/null
docker network create "$NET" >/dev/null

# Postgres: role mem0 (no superuser) owns database mem0; the extension is created as
# superuser, as CloudNativePG's postInitApplicationSQL does.
echo "== postgres"
docker run -d --name "$PG" --network "$NET" -e POSTGRES_PASSWORD=super "$PGVECTOR_IMAGE" >/dev/null
for _ in $(seq 60); do
  docker exec "$PG" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1 && break
  sleep 1
done
psql_super() { docker exec -i "$PG" psql -v ON_ERROR_STOP=1 -U postgres -h 127.0.0.1 "$@"; }
# the entrypoint restarts postgres once during init; wait until a real query works
for _ in $(seq 30); do psql_super -c 'select 1' >/dev/null 2>&1 && break; sleep 1; done
psql_super -c "CREATE ROLE mem0 LOGIN PASSWORD 'mem0pw' NOSUPERUSER" >/dev/null
psql_super -c "CREATE DATABASE mem0 OWNER mem0" >/dev/null
psql_super -d mem0 -c "CREATE EXTENSION vector" >/dev/null

docker run -d --name "$MODEL" --network "$NET" -e REQUIRE_TOKEN=false -e DIMS="$DIMS" "$FAKE_IMAGE" >/dev/null

MODEL_URL="http://$MODEL:8080/v1"
env_args=(
  -e POSTGRES_HOST="$PG" -e POSTGRES_PORT=5432 -e POSTGRES_DB=mem0 -e APP_DB_NAME=mem0
  -e POSTGRES_USER=mem0 -e POSTGRES_PASSWORD=mem0pw
  -e ADMIN_API_KEY="$API_KEY" -e OPENAI_API_KEY=placeholder -e OPENAI_BASE_URL="$MODEL_URL"
  -e MEM0PACK_MODEL_BASE_URL="$MODEL_URL" -e MEM0PACK_LLM_MODEL=fake-llm
  -e MEM0PACK_EMBEDDING_MODEL=fake-embed -e MEM0PACK_EMBEDDING_DIMS="$DIMS"
)
# tmpfs is root-owned by default; in a pod the emptyDir gets its group from fsGroup
ro_args=(--read-only --tmpfs /tmp --tmpfs /var/lib/mem0:uid=10001,gid=10001)

echo "== migrate"
docker run --rm --name "$SERVER-migrate" --network "$NET" "${ro_args[@]}" "${env_args[@]}" \
  --entrypoint python "$IMAGE" -m mem0pack.migrate

echo "== serve"
docker run -d --name "$SERVER" --network "$NET" -p 127.0.0.1::8000 "${ro_args[@]}" "${env_args[@]}" \
  "$IMAGE" >/dev/null
PORT="$(docker port "$SERVER" 8000/tcp | head -n1 | sed 's/.*://')"
BASE="http://127.0.0.1:$PORT"

code=000
for _ in $(seq 60); do
  code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz" || true)"
  [ "$code" = 200 ] && break
  docker ps -q -f name="^$SERVER$" | grep -q . || { docker logs "$SERVER" >&2; fail "server exited"; }
  sleep 1
done
[ "$code" = 200 ] || { docker logs "$SERVER" >&2; fail "/healthz returned $code"; }
echo "healthz 200"

echo "== go test"
cd "$root"
MEM0_URL="$BASE" MEM0_API_KEY="$API_KEY" go test -tags mem0 -count=1 -run Integration ./internal/memory/ \
  || { docker logs "$SERVER" >&2; exit 1; }
