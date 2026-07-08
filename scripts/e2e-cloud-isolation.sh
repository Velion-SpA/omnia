#!/usr/bin/env bash
# End-to-end multi-tenant isolation test for the Engram/Omnia cloud server.
#
# Spins up the cloud server (auth mode) against a throwaway local Postgres DB,
# creates two real accounts, and proves that one account cannot read another's
# project — and that access only opens up when explicitly granted.
#
# Requirements: a running local Postgres (psql/createdb on PATH), Go, jq, curl.
# Usage: scripts/e2e-cloud-isolation.sh
set -euo pipefail

PORT="${E2E_PORT:-18080}"
DB="${E2E_DB:-engram_cloud_e2e}"
PGUSER_E2E="${E2E_PGUSER:-$(whoami)}"
S="http://127.0.0.1:${PORT}"
BIN="$(mktemp -t engram-e2e-XXXX)"
LOG="$(mktemp -t engram-e2e-log-XXXX)"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cleanup() {
  [[ -n "${SRV_PID:-}" ]] && kill "${SRV_PID}" 2>/dev/null || true
  dropdb --if-exists "${DB}" 2>/dev/null || true
  rm -f "${BIN}" "${LOG}"
}
trap cleanup EXIT

echo "==> fresh test database"
dropdb --if-exists "${DB}" 2>/dev/null || true
createdb "${DB}"

echo "==> build cloud binary"
( cd "${ROOT}" && go build -o "${BIN}" ./cmd/omnia )

echo "==> launch cloud server (auth mode)"
# Open signup is closed by default (OBL-02); this e2e test seeds accounts via
# POST /auth/signup, so it re-opens it with ENGRAM_CLOUD_OPEN_SIGNUP=1.
ENGRAM_DATABASE_URL="postgres://${PGUSER_E2E}@localhost:5432/${DB}?sslmode=disable" \
ENGRAM_JWT_SECRET="e2e-jwt-secret-at-least-32-bytes-long-1234567890" \
ENGRAM_CLOUD_TOKEN="e2e-admin-token" \
ENGRAM_CLOUD_ALLOWED_PROJECTS="_legacy_unused" \
ENGRAM_CLOUD_OPEN_SIGNUP="1" \
ENGRAM_CLOUD_HOST="127.0.0.1" ENGRAM_PORT="${PORT}" \
  "${BIN}" cloud serve > "${LOG}" 2>&1 &
SRV_PID=$!

for i in $(seq 1 20); do
  curl -fs -o /dev/null "${S}/health" && break || sleep 0.5
done

pass=0; fail=0
check() { # name actual want
  if [[ "$2" == "$3" ]]; then printf "  PASS  %-42s %s\n" "$1" "$2"; pass=$((pass+1));
  else printf "  FAIL  %-42s got %s want %s\n" "$1" "$2" "$3"; fail=$((fail+1)); fi
}
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

echo "==> signup two accounts"
check "signup alice" "$(code -X POST "$S/auth/signup" -H 'Content-Type: application/json' -d '{"username":"alice","email":"alice@e2e","password":"alicepass123"}')" 201
check "signup bob"   "$(code -X POST "$S/auth/signup" -H 'Content-Type: application/json' -d '{"username":"bob","email":"bob@e2e","password":"bobpass123"}')" 201

echo "==> login → account tokens"
TOKA=$(curl -s -X POST "$S/auth/login" -H 'Content-Type: application/json' -d '{"username":"alice","password":"alicepass123"}' | jq -r .token)
TOKB=$(curl -s -X POST "$S/auth/login" -H 'Content-Type: application/json' -d '{"username":"bob","password":"bobpass123"}'     | jq -r .token)
[[ -n "$TOKA" && -n "$TOKB" ]] && echo "  got both account tokens"

echo "==> seed ownership (alice→proj-alice, bob→proj-bob)"
psql -q "${DB}" -c "INSERT INTO cloud_memberships(account_id,project,perms,role) SELECT id,'proj-alice',15,'owner' FROM cloud_users WHERE username='alice';"
psql -q "${DB}" -c "INSERT INTO cloud_memberships(account_id,project,perms,role) SELECT id,'proj-bob',15,'owner' FROM cloud_users WHERE username='bob';"
BOBID=$(psql -tA "${DB}" -c "SELECT id FROM cloud_users WHERE username='bob';")

echo "==> isolation"
check "alice reads own proj-alice"  "$(code -H "Authorization: Bearer $TOKA" "$S/sync/pull?project=proj-alice")" 200
check "alice reads bob's proj-bob"  "$(code -H "Authorization: Bearer $TOKA" "$S/sync/pull?project=proj-bob")"   403
check "bob reads own proj-bob"      "$(code -H "Authorization: Bearer $TOKB" "$S/sync/pull?project=proj-bob")"   200

echo "==> dynamic grant"
GRANT_BODY=$(printf '{"account_id":"%s","perms":1,"role":"member"}' "$BOBID")
check "alice grants bob READ"       "$(code -X POST -H "Authorization: Bearer $TOKA" -H 'Content-Type: application/json' "$S/projects/proj-alice/members" -d "$GRANT_BODY")" 201
check "bob reads proj-alice (now)"  "$(code -H "Authorization: Bearer $TOKB" "$S/sync/pull?project=proj-alice")" 200

echo
echo "RESULT: ${pass} passed, ${fail} failed"
exit $(( fail > 0 ? 1 : 0 ))
