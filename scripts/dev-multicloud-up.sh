#!/usr/bin/env bash
# Stand up TWO local Omnia clouds — "work" and "personal" — to demo multi-cloud.
# Each gets its own Postgres DB, its own server port, its own account + project.
# Leaves both servers RUNNING in the background (PIDs in /tmp/omnia-mc-*.pid).
#
# Requires: local Postgres (createdb/psql), Go, curl, jq.
# Up:   scripts/dev-multicloud-up.sh
# Down: scripts/dev-multicloud-down.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PGUSER_MC="${MC_PGUSER:-$(whoami)}"
BIN="/tmp/omnia-mc-engram"
RUN="/tmp/omnia-mc"
mkdir -p "$RUN"

echo "==> build engram binary"
( cd "$ROOT" && go build -o "$BIN" ./cmd/omnia )

# cloud_alias  port  db                       account-user      account-pass        project
CLOUDS=(
  "work     18090 engram_cloud_work     benja-work     workpass123     proj-trabajo"
  "personal 18091 engram_cloud_personal benja-personal personalpass123 proj-empresa"
)

start_cloud() {
  local alias=$1 port=$2 db=$3 user=$4 pass=$5 project=$6
  echo "==> [$alias] fresh db + server on :$port"
  # NOTE: open signup is closed by default (OBL-02). This dev harness seeds accounts
  # via POST /auth/signup, so it sets ENGRAM_CLOUD_OPEN_SIGNUP=1 below to re-open it
  # for these throwaway dev servers. Do NOT do this in production; use
  # `omnia cloud bootstrap-admin` to provision the first admin instead.
  dropdb --if-exists "$db" 2>/dev/null || true
  createdb "$db"
  ENGRAM_DATABASE_URL="postgres://${PGUSER_MC}@localhost:5432/${db}?sslmode=disable" \
  ENGRAM_JWT_SECRET="mc-${alias}-jwt-secret-at-least-32-bytes-long-123456" \
  ENGRAM_CLOUD_TOKEN="mc-${alias}-admin-token" \
  ENGRAM_CLOUD_ADMIN="mc-${alias}-dashboard-admin" \
  ENGRAM_CLOUD_ALLOWED_PROJECTS="*" \
  ENGRAM_CLOUD_OPEN_SIGNUP="1" \
  ENGRAM_CLOUD_HOST="127.0.0.1" ENGRAM_PORT="$port" \
    "$BIN" cloud serve > "$RUN/$alias.log" 2>&1 &
  echo $! > "$RUN/$alias.pid"

  local S="http://127.0.0.1:$port"
  for i in $(seq 1 20); do curl -fs -o /dev/null "$S/health" && break || sleep 0.5; done

  echo "==> [$alias] create account '$user' and seed project '$project'"
  curl -fs -X POST "$S/auth/signup" -H 'Content-Type: application/json' \
    -d "$(printf '{"username":"%s","email":"%s@%s","password":"%s"}' "$user" "$user" "$alias" "$pass")" >/dev/null
  # owner membership for the account's project
  psql -q "$db" -c "INSERT INTO cloud_memberships(account_id,project,perms,role) SELECT id,'$project',15,'owner' FROM cloud_users WHERE username='$user';"
}

for row in "${CLOUDS[@]}"; do start_cloud $row; done

echo
echo "================ TWO CLOUDS RUNNING ================"
for row in "${CLOUDS[@]}"; do
  read -r alias port db user pass project <<<"$row"
  echo "  $alias  -> http://127.0.0.1:$port   account=$user  project=$project  (pid $(cat "$RUN/$alias.pid"))"
done
echo "Logs: $RUN/<alias>.log   Stop: scripts/dev-multicloud-down.sh"
echo "Passwords: work=workpass123  personal=personalpass123"
