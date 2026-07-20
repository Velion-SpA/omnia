#!/bin/bash
# Omnia — PostToolUseFailure hook: forced-activation bugfix recall (#1399 slice 2)
#
# Design (obs #1498 / audit #1497): Omnia's Go server has NO visibility into
# tool-call outcomes — only Claude Code hooks see them. On a real tool error
# this hook force-injects a compact recall of any past PROVEN fix for the SAME
# normalized error signature, so the agent never has to remember to search.
#
# EVENT: PostToolUseFailure — fires when a tool call FAILS, including a Bash
# command that exits non-zero (a failing test/build/linter), which is exactly
# the recurring-bug case this hook exists for. Because it only fires on
# failure, there is no separate "does this look like an error" gate.
#
# PAYLOAD SHAPE (verified against the live runtime, 2026-07): this build
# delivers a top-level `.error` STRING (the "Exit code N" line plus the
# command's stderr/stdout combined). Other/older builds instead use a
# `.tool_error` OBJECT (message/stderr/stdout, sometimes nested under
# .details) or an `.error` object. This script reads EVERY known shape so the
# real error text always reaches `omnia recall-fix` regardless of version.
#
# SCOPE: searches ALL projects (no --project filter). Recurring bugs reappear
# across different projects/migrations; the signature-lane match is specific
# enough on its own, so scoping to the current project would MISS the exact
# cross-migration recurrence this hook is for.
#
# FAIL-QUIET + FAST: any problem (bad JSON, `omnia` missing, slow DB) exits 0
# with no output. `timeout` bounds the one real-work call so it can never stall
# the tool loop. Relies on omnia's own datadir resolution (~/.omnia, falling
# back to ~/.engram) — deliberately does NOT hardcode OMNIA_DATA_DIR.
#
# Manual self-check (no Claude Code needed):
#   echo '{"session_id":"t","hook_event_name":"PostToolUseFailure","error":"Exit code 1\npanic: runtime error: index out of range [7] with length 2\n\tat main.go:42"}' \
#     | ./post-tool-error-recall.sh | jq .
#   # No matching prior fix (or omnia unavailable) → NO output, exit 0:
#   echo '{"session_id":"t","hook_event_name":"PostToolUseFailure","error":""}' \
#     | ./post-tool-error-recall.sh; echo "exit=$?"

set -u

OMNIA_BIN="${OMNIA_BIN:-omnia}"
RECALL_TIMEOUT_SECS="${OMNIA_RECALL_TIMEOUT_SECS:-3}"

INPUT=$(cat)

SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)

# Read every known error-payload shape and concatenate whatever is present.
# `.error` may be a string (this runtime) or an object; `.tool_error` may be an
# object (older/other builds), possibly with stderr/stdout nested under
# .details. jq's `strings`/`objects` filters make each branch a no-op when the
# field is absent or the wrong type, so this is safe across all shapes.
ERROR_TEXT=$(printf '%s' "$INPUT" | jq -r '
  [
    (.error | strings),
    (.error | objects | (.message, .stderr, .stdout, .details.stderr, .details.stdout)),
    (.tool_error | objects | (.message, .stderr, .stdout, .details.stderr, .details.stdout))
  ] | flatten | map(select(. != null and . != "")) | join("\n")
' 2>/dev/null)

# Nothing to recall from an empty error payload.
if [ -z "$(printf '%s' "$ERROR_TEXT" | tr -d '[:space:]')" ]; then
  exit 0
fi

# omnia must be on PATH; otherwise fail quiet.
command -v "$OMNIA_BIN" >/dev/null 2>&1 || exit 0

# Authoritative Go gate: recall-fix returns ONLY signature-lane hits (proven
# recurring-error matches), never loose BM25 text hits. No --project → all
# projects (cross-migration recurrence is the point).
RECALL_BLOCK=""
if command -v timeout >/dev/null 2>&1; then
  RECALL_BLOCK=$(printf '%s' "$ERROR_TEXT" | timeout "${RECALL_TIMEOUT_SECS}s" "$OMNIA_BIN" recall-fix 2>/dev/null)
else
  RECALL_BLOCK=$(printf '%s' "$ERROR_TEXT" | "$OMNIA_BIN" recall-fix 2>/dev/null)
fi

[ -z "$RECALL_BLOCK" ] && exit 0

# Dedup: skip re-injecting obs ids already surfaced earlier this session.
STATE_DIR="${TMPDIR:-/tmp}"
SESSION_KEY="${SESSION_ID:-nosession}"
OBS_IDS=$(printf '%s' "$RECALL_BLOCK" | grep -oE 'obs #[0-9]+' | sed 's/obs #//')

ANY_NEW=0
for OBS_ID in $OBS_IDS; do
  MARKER="${STATE_DIR}/omnia-recall-seen-${SESSION_KEY}-${OBS_ID}"
  [ ! -f "$MARKER" ] && ANY_NEW=1
done
[ "$ANY_NEW" -eq 0 ] && exit 0

for OBS_ID in $OBS_IDS; do
  MARKER="${STATE_DIR}/omnia-recall-seen-${SESSION_KEY}-${OBS_ID}"
  : > "$MARKER" 2>/dev/null || true
done

PREFIX="🔎 Omnia — fix(es) previo(s) para este error (mem_get_observation <id> para el detalle):"
CONTEXT="${PREFIX}
${RECALL_BLOCK}"

jq -n --arg ctx "$CONTEXT" '{hookSpecificOutput: {hookEventName: "PostToolUseFailure", additionalContext: $ctx}}'
exit 0
