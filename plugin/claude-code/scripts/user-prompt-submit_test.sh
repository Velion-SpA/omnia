#!/bin/bash
# Shell test harness for user-prompt-submit.sh — signal-gated recall nudge
# (Play O, omnia v0.3 context economy).
#
# No existing *_test.sh / bats harness exists in this repo for the Claude Code
# hook scripts (they are only manually self-checked per the header comments in
# post-tool-error-recall.sh). This is the minimal executable precedent: each
# case pipes a JSON stdin payload through the real script and asserts on its
# stdout, using a fresh unique SESSION_ID per case so dedup-marker state in
# /tmp never leaks between cases or across re-runs.
#
# Run:
#   bash plugin/claude-code/scripts/user-prompt-submit_test.sh
#
# Exit 0 = all cases passed. Exit 1 = at least one case failed (details printed).

set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="${SCRIPT_DIR}/user-prompt-submit.sh"

PASS=0
FAIL=0
MARKERS_TO_CLEAN=()
MOCK_SERVER_PID=""

cleanup() {
  for m in "${MARKERS_TO_CLEAN[@]}"; do
    rm -f /tmp/"${m}"* 2>/dev/null || true
    # New signal-recall markers live under ${TMPDIR:-/tmp} (design.md §3.4),
    # which differs from /tmp on macOS (TMPDIR is a per-user /var/folders
    # path) — sweep both so nothing leaks between test runs.
    rm -f "${TMPDIR:-/tmp}/${m}"* 2>/dev/null || true
  done
  if [ -n "$MOCK_SERVER_PID" ]; then
    kill "$MOCK_SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

unique_session() {
  # Unique per invocation so /tmp marker files never collide with a previous
  # (or concurrent) test run.
  printf 'pr6-test-%s-%s-%s' "$$" "$RANDOM" "$(date +%s)"
}

# Pre-touch the "tools-loaded" state file for a session so the run under test
# is treated as a SUBSEQUENT message, not the first message of the session
# (first-message always returns the ToolSearch instruction, unrelated to this
# feature).
prime_subsequent_message() {
  local session_id="$1"
  local state_file="/tmp/engram-claude-${session_id}-tools-loaded"
  : > "$state_file" 2>/dev/null || true
  MARKERS_TO_CLEAN+=("engram-claude-${session_id}")
  MARKERS_TO_CLEAN+=("omnia-signal-recall-${session_id}")
}

run_hook() {
  # run_hook <stdin-payload> <env-assignment-or-empty>
  local payload="$1"
  local env_kv="${2:-}"
  if [ -n "$env_kv" ]; then
    printf '%s' "$payload" | env "$env_kv" "$SCRIPT" 2>/dev/null
  else
    printf '%s' "$payload" | "$SCRIPT" 2>/dev/null
  fi
}

assert_contains() {
  local case_name="$1" haystack="$2" needle="$3"
  if printf '%s' "$haystack" | grep -qF "$needle"; then
    PASS=$((PASS + 1))
    echo "PASS: $case_name"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $case_name — expected output to contain: $needle"
    echo "      got: $haystack"
  fi
}

assert_equals() {
  local case_name="$1" actual="$2" expected="$3"
  if [ "$actual" = "$expected" ]; then
    PASS=$((PASS + 1))
    echo "PASS: $case_name"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $case_name — expected: $expected"
    echo "      got:      $actual"
  fi
}

# ── Case 1: new-topic prompt with OMNIA_SIGNAL_RECALL on -> nudge ──────────
SID1=$(unique_session)
prime_subsequent_message "$SID1"
PAYLOAD1=$(jq -n --arg sid "$SID1" '{session_id:$sid, prompt:"implement a new caching layer for the API", cwd:"/tmp"}')
OUT1=$(run_hook "$PAYLOAD1" "OMNIA_SIGNAL_RECALL=1")
assert_contains "new-topic nudge fires" "$OUT1" "MEMORY NUDGE (new-topic)"
assert_contains "new-topic nudge mentions mem_search" "$OUT1" "mem_search"

# ── Case 2: same trigger repeated in the same session -> second call silent ─
SID2=$(unique_session)
prime_subsequent_message "$SID2"
PAYLOAD2=$(jq -n --arg sid "$SID2" '{session_id:$sid, prompt:"implement a new caching layer for the API", cwd:"/tmp"}')
FIRST=$(run_hook "$PAYLOAD2" "OMNIA_SIGNAL_RECALL=1")
SECOND=$(run_hook "$PAYLOAD2" "OMNIA_SIGNAL_RECALL=1")
assert_contains "dedup: first call still nudges" "$FIRST" "MEMORY NUDGE"
assert_equals "dedup: second identical call is silent" "$SECOND" "{}"

# ── Case 3: uncertainty prompt with OMNIA_SIGNAL_RECALL on -> nudge ─────────
SID3=$(unique_session)
prime_subsequent_message "$SID3"
PAYLOAD3=$(jq -n --arg sid "$SID3" '{session_id:$sid, prompt:"why is the build failing on CI?", cwd:"/tmp"}')
OUT3=$(run_hook "$PAYLOAD3" "OMNIA_SIGNAL_RECALL=1")
assert_contains "uncertainty nudge fires" "$OUT3" "MEMORY NUDGE (uncertainty)"

# ── Case 4: benign prompt, no signal -> {} ──────────────────────────────────
SID4=$(unique_session)
prime_subsequent_message "$SID4"
PAYLOAD4=$(jq -n --arg sid "$SID4" '{session_id:$sid, prompt:"thanks, that looks good", cwd:"/tmp"}')
OUT4=$(run_hook "$PAYLOAD4" "OMNIA_SIGNAL_RECALL=1")
assert_equals "benign prompt produces no nudge" "$OUT4" "{}"

# ── Case 5: malformed stdin -> exit 0, valid JSON, no crash ─────────────────
# NOTE: with unparseable JSON, jq cannot even extract .session_id, so the
# PRE-EXISTING (unrelated to this PR) fallback key logic applies and the
# script takes the first-message ToolSearch path (its fallback key embeds the
# script's own $$ PID, which is fresh every invocation and can't be
# pre-primed). That is fine: the invariant this case protects is PR6's own
# requirement — no crash, exit 0, valid JSON — not any specific payload.
SID5=$(unique_session)
prime_subsequent_message "$SID5"
printf '%s' '{not valid json at all' | env "OMNIA_SIGNAL_RECALL=1" "$SCRIPT" >/tmp/pr6-test-case5-out 2>/dev/null
EXIT5=$?
OUT5=$(cat /tmp/pr6-test-case5-out 2>/dev/null)
rm -f /tmp/pr6-test-case5-out 2>/dev/null || true
assert_equals "malformed stdin exits 0" "$EXIT5" "0"
if printf '%s' "$OUT5" | jq empty >/dev/null 2>&1; then
  PASS=$((PASS + 1))
  echo "PASS: malformed stdin yields valid JSON"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: malformed stdin yields valid JSON — got: $OUT5"
fi

# ── Case 6: OMNIA_SIGNAL_RECALL unset -> always {}, even for a signal-shaped
#            prompt (proves the default-off no-op) ─────────────────────────
SID6=$(unique_session)
prime_subsequent_message "$SID6"
PAYLOAD6=$(jq -n --arg sid "$SID6" '{session_id:$sid, prompt:"implement a new caching layer for the API", cwd:"/tmp"}')
OUT6_ON=$(run_hook "$PAYLOAD6" "OMNIA_SIGNAL_RECALL=1")
SID6B=$(unique_session)
prime_subsequent_message "$SID6B"
PAYLOAD6B=$(jq -n --arg sid "$SID6B" '{session_id:$sid, prompt:"implement a new caching layer for the API", cwd:"/tmp"}')
OUT6_OFF=$(run_hook "$PAYLOAD6B" "")
assert_contains "sanity: same prompt WOULD nudge when on" "$OUT6_ON" "MEMORY NUDGE"
assert_equals "OMNIA_SIGNAL_RECALL unset -> always {}" "$OUT6_OFF" "{}"

# ── Case 7 (off-path regression): OFF-path output byte-for-byte identical to
#    the pre-PR6 script for the same input, using git to diff the working
#    script against its base-branch revision, across several adversarial
#    inputs (embedded quotes/backslash, embedded newline, a very long prompt,
#    a JSON-looking prompt) in addition to a benign one. Skipped gracefully if
#    git/base ref is unavailable (e.g. running outside the repo history).
# ─────────────────────────────────────────────────────────────────────────
BASE_SCRIPT_CONTENT=$(cd "$SCRIPT_DIR" && git show 'main:plugin/claude-code/scripts/user-prompt-submit.sh' 2>/dev/null)
if [ -n "$BASE_SCRIPT_CONTENT" ]; then
  TMP_BASE_SCRIPT=$(mktemp)
  printf '%s\n' "$BASE_SCRIPT_CONTENT" > "$TMP_BASE_SCRIPT"
  chmod +x "$TMP_BASE_SCRIPT"

  LONG_PROMPT=$(python3 -c "print('lorem ipsum dolor sit amet ' * 300)" 2>/dev/null || printf 'x%.0s' $(seq 1 6000))

  ADVERSARIAL_NAMES=(
    "benign prompt"
    "embedded quotes/backslash"
    "embedded newline"
    "very long prompt (~6000+ chars)"
    "JSON-looking prompt"
  )
  ADVERSARIAL_PROMPTS=(
    'thanks, that looks good'
    'He said "it'"'"'s broken\" and left — fix it?'
    $'line one\nline two\nline three'
    "$LONG_PROMPT"
    '{"nested": ["array", 1, true], "x": null, "note": "looks like json"}'
  )

  for i in "${!ADVERSARIAL_NAMES[@]}"; do
    NAME="${ADVERSARIAL_NAMES[$i]}"
    PROMPT_VAL="${ADVERSARIAL_PROMPTS[$i]}"

    SID7=$(unique_session)
    prime_subsequent_message "$SID7"
    PAYLOAD7=$(jq -n --arg sid "$SID7" --arg p "$PROMPT_VAL" '{session_id:$sid, prompt:$p, cwd:"/tmp"}')
    OUT7_NEW=$(printf '%s' "$PAYLOAD7" | "$SCRIPT" 2>/dev/null)

    SID7B=$(unique_session)
    prime_subsequent_message "$SID7B"
    PAYLOAD7B=$(jq -n --arg sid "$SID7B" --arg p "$PROMPT_VAL" '{session_id:$sid, prompt:$p, cwd:"/tmp"}')
    OUT7_OLD=$(printf '%s' "$PAYLOAD7B" | "$TMP_BASE_SCRIPT" 2>/dev/null)

    assert_equals "OFF path byte-for-byte identical to base branch — ${NAME}" "$OUT7_NEW" "$OUT7_OLD"
  done

  rm -f "$TMP_BASE_SCRIPT" 2>/dev/null || true
else
  echo "SKIP: off-path base-branch diff (no 'main' ref available in this checkout)"
fi

# ── Case 8: cooldown suppresses a SECOND, DIFFERENT-topic trigger within the
#    same session — proves the frequency limiter (not just topic dedup)
#    stops firing on every turn during a rapid Q&A exchange. ────────────────
SID8=$(unique_session)
prime_subsequent_message "$SID8"
PAYLOAD8A=$(jq -n --arg sid "$SID8" '{session_id:$sid, prompt:"why is auth failing", cwd:"/tmp"}')
OUT8A=$(run_hook "$PAYLOAD8A" "OMNIA_SIGNAL_RECALL=1")
PAYLOAD8B=$(jq -n --arg sid "$SID8" '{session_id:$sid, prompt:"how do I fix the migration script?", cwd:"/tmp"}')
OUT8B=$(run_hook "$PAYLOAD8B" "OMNIA_SIGNAL_RECALL=1")
assert_contains "cooldown: first (different) topic still nudges" "$OUT8A" "MEMORY NUDGE"
assert_equals "cooldown: second DIFFERENT topic within 300s is silent" "$OUT8B" "{}"

# ── Case 9: LC_ALL=C — Spanish trigger still fires under the POSIX/C locale,
#    where naive accent bracket-classes ([oó]) are not multi-byte-UTF-8-safe.
# ─────────────────────────────────────────────────────────────────────────
SID9=$(unique_session)
prime_subsequent_message "$SID9"
PAYLOAD9=$(jq -n --arg sid "$SID9" '{session_id:$sid, prompt:"¿cómo se soluciona esta migración que falla?", cwd:"/tmp"}')
OUT9=$(printf '%s' "$PAYLOAD9" | env LC_ALL=C OMNIA_SIGNAL_RECALL=1 "$SCRIPT" 2>/dev/null)
assert_contains "LC_ALL=C: Spanish uncertainty trigger still nudges" "$OUT9" "MEMORY NUDGE"

# ── Case 10: combined signal + save nudge — proves the signal nudge no
#    longer starves the save-nudge check. Spins up a minimal local HTTP mock
#    of the engram server (python3) that reports an old session-start and an
#    old last-save, so the save-nudge's own condition is genuinely due, at
#    the same time a signal trigger fires. Skipped gracefully if python3 is
#    unavailable. ────────────────────────────────────────────────────────────
if command -v python3 >/dev/null 2>&1; then
  MOCK_SCRIPT=$(mktemp)
  NOW_EPOCH_T=$(date "+%s")
  OLD_EPOCH_T=$(( NOW_EPOCH_T - 1200 ))
  OLD_TS=$(date -u -r "$OLD_EPOCH_T" "+%Y-%m-%dT%H:%M:%SZ" 2>/dev/null \
    || date -u -d "@${OLD_EPOCH_T}" "+%Y-%m-%dT%H:%M:%SZ" 2>/dev/null)

  cat > "$MOCK_SCRIPT" <<'PYEOF'
import http.server, json, sys

port = int(sys.argv[1])
ts = sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass
    def do_GET(self):
        if self.path.startswith("/sessions/"):
            body = json.dumps({"started_at": ts}).encode()
        elif self.path.startswith("/observations"):
            body = json.dumps([{"created_at": ts}]).encode()
        else:
            body = b"{}"
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

http.server.HTTPServer(("127.0.0.1", port), Handler).serve_forever()
PYEOF

  MOCK_PORT=$(( (RANDOM % 20000) + 20000 ))
  if [ -n "$OLD_TS" ]; then
    python3 "$MOCK_SCRIPT" "$MOCK_PORT" "$OLD_TS" >/dev/null 2>&1 &
    MOCK_SERVER_PID=$!

    READY=0
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      if curl -sf "http://127.0.0.1:${MOCK_PORT}/observations" --max-time 0.2 >/dev/null 2>&1; then
        READY=1
        break
      fi
      sleep 0.1
    done

    if [ "$READY" -eq 1 ]; then
      SID10=$(unique_session)
      prime_subsequent_message "$SID10"
      PAYLOAD10=$(jq -n --arg sid "$SID10" '{session_id:$sid, prompt:"why is the deploy failing", cwd:"/tmp"}')
      OUT10=$(printf '%s' "$PAYLOAD10" | env OMNIA_SIGNAL_RECALL=1 ENGRAM_PORT="$MOCK_PORT" "$SCRIPT" 2>/dev/null)
      assert_contains "combined nudge: signal half present" "$OUT10" "MEMORY NUDGE"
      assert_contains "combined nudge: save-nudge half present (no starvation)" "$OUT10" "MEMORY REMINDER"
    else
      echo "SKIP: combined signal+save nudge (mock server did not become ready)"
    fi

    kill "$MOCK_SERVER_PID" 2>/dev/null || true
    wait "$MOCK_SERVER_PID" 2>/dev/null || true
    MOCK_SERVER_PID=""
  else
    echo "SKIP: combined signal+save nudge (could not compute a portable past timestamp)"
  fi
  rm -f "$MOCK_SCRIPT" 2>/dev/null || true
else
  echo "SKIP: combined signal+save nudge (python3 unavailable for the mock server)"
fi

# ── Case 11: new signal-recall marker/cooldown files land under
#    ${TMPDIR:-/tmp} (design.md §3.4 location), not hardcoded /tmp. ─────────
SID11=$(unique_session)
prime_subsequent_message "$SID11"
PAYLOAD11=$(jq -n --arg sid "$SID11" '{session_id:$sid, prompt:"implement a new retry policy", cwd:"/tmp"}')
OUT11=$(run_hook "$PAYLOAD11" "OMNIA_SIGNAL_RECALL=1")
assert_contains "marker-location: nudge fired" "$OUT11" "MEMORY NUDGE"
MARKER_HITS=$(find "${TMPDIR:-/tmp}" -maxdepth 1 -name "omnia-signal-recall-${SID11}*" -type f 2>/dev/null | wc -l | tr -d ' ')
if [ "$MARKER_HITS" -ge 1 ]; then
  PASS=$((PASS + 1))
  echo "PASS: marker files created under \${TMPDIR:-/tmp} (found ${MARKER_HITS})"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: marker files created under \${TMPDIR:-/tmp} — found 0"
fi

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
[ "$FAIL" -eq 0 ]
