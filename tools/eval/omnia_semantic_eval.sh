#!/usr/bin/env bash
# Isolated SEMANTIC recall eval (grep-based; no rg dependency).
#
# Usage:
#   tools/eval/omnia_semantic_eval.sh
#   EVAL_SCRATCH=/tmp/my-scratch OMNIA_BIN=/opt/homebrew/bin/omnia tools/eval/omnia_semantic_eval.sh
#
# EVAL_SCRATCH defaults to a fresh mktemp dir (disposable). OMNIA_BIN defaults
# to "omnia" (resolved via PATH). This script overrides HOME to an isolated
# path so config/data live under EVAL_SCRATCH, never touching a real install.
set -u
EVAL_SCRATCH="${EVAL_SCRATCH:-$(mktemp -d)}"
OM="${OMNIA_BIN:-omnia}"
export HOME="$EVAL_SCRATCH/eval-home"
export OMNIA_PROJECT="eval-suite"
PORT="${EVAL_PORT:-7500}"
rm -rf "$HOME"; mkdir -p "$HOME/.config/omnia"
NOUP() { grep -Ev "Update available|To update|brew update|brew upgrade"; }
enc() { python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1]))" "$1"; }

cat > "$HOME/.config/omnia/config.yaml" <<'YAML'
recall:
  enabled: true
embeddings:
  enabled: true
  model: jina/jina-embeddings-v2-base-es
  base_url: http://localhost:11434
YAML

echo "== scratch home: $HOME =="
echo "== seeding corpus =="
"$OM" save "Configured Redis cache for session store" "Added Redis-backed session storage to cut Postgres load; pool size 20, TTL 30m" --type config --project eval-suite 2>&1 | NOUP | grep -o "Memory saved.*"
"$OM" save "Redis pub/sub for realtime notifications" "Used Redis pub/sub channels to fan out realtime notification events to websocket clients" --type config --project eval-suite 2>&1 | NOUP | grep -o "Memory saved.*"
"$OM" save "Chose PostgreSQL over MongoDB" "Picked PostgreSQL over MongoDB for the orders service because we need multi-row transactional integrity and strong foreign-key constraints" --type decision --project eval-suite 2>&1 | NOUP | grep -o "Memory saved.*"
"$OM" save "Adopted hexagonal architecture" "Domain core is isolated from adapters via ports; infrastructure like db and http depends inward" --type architecture --project eval-suite 2>&1 | NOUP | grep -o "Memory saved.*"

echo "== embedding (isolated) =="
"$OM" embed 2>&1 | NOUP | grep -Eo "embed:.*" | head -1
echo "  vectors in isolated embeddings.db: $(sqlite3 "$HOME/.local/share/omnia/embeddings.db" "SELECT count(*) FROM embeddings;" 2>/dev/null)"

echo "== starting isolated serve on :$PORT =="
"$OM" serve $PORT >"$EVAL_SCRATCH/eval-serve.log" 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null' EXIT
for i in $(seq 1 20); do
  [ "$(curl -s -m2 -o /dev/null -w '%{http_code}' http://127.0.0.1:$PORT/health 2>/dev/null)" = "200" ] && break
  sleep 0.5
done
echo "  health: $(curl -s -m2 -o /dev/null -w 'HTTP %{http_code}' http://127.0.0.1:$PORT/health)"

echo ""
echo "== FTS (CLI) vs SEMANTIC (served) =="
printf "%-46s | %-14s | %-14s\n" "query" "FTS/CLI" "SEMANTIC"
printf -- "-%.0s" {1..80}; echo
for q in "where are user sessions stored" "why did we pick our database" "how is the domain isolated from infrastructure" "Redis session cache store"; do
  fts=$("$OM" search "$q" --project eval-suite --limit 3 2>&1 | NOUP | grep -oE '#[0-9]+' | tr '\n' ' ')
  sem=$(curl -s -m8 "http://127.0.0.1:$PORT/search?q=$(enc "$q")&project=eval-suite&limit=3" 2>/dev/null | jq -r '(if type=="array" then . else (.results // .hits // []) end)[] | "#\(.id // .ID)"' 2>/dev/null | tr '\n' ' ')
  printf "%-46s | %-14s | %-14s\n" "$q" "${fts:-(none)}" "${sem:-(none)}"
done

echo ""
echo "== RAW served response for one paraphrase (thresholding debug) =="
curl -s -m8 "http://127.0.0.1:$PORT/search?q=$(enc "why did we pick our database")&project=eval-suite&limit=5" 2>/dev/null | jq '.' 2>/dev/null | head -c 700
echo ""
echo "== does /search take a mode/semantic flag? probe root =="
curl -s -m4 "http://127.0.0.1:$PORT/search?q=$(enc "database choice reasoning")&project=eval-suite&limit=5&mode=semantic" 2>/dev/null | jq -r 'length' 2>/dev/null
