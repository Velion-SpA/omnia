#!/bin/bash
# Omnia — PostToolUseFailure hook: forced-activation bugfix recall (#1399 slice 2)
#
# Design (obs #1498 / audit #1497): Omnia's Go server has NO visibility into
# tool-call outcomes — only Claude Code hooks see them. This hook closes
# that gap for RECURRING bugs: on a real tool error, it force-injects a
# compact recall of any past PROVEN fix for the same normalized error
# signature, so the agent never has to remember to search.
#
# EVENT: PostToolUseFailure (NOT PostToolUse — PostToolUse fires only on
# tool SUCCESS per code.claude.com/docs/en/hooks.md, so it can never see a
# tool error; PostToolUseFailure fires only when a tool call FAILS, which
# is exactly the event this hook exists for). Because the event only fires
# on failure, there is no separate "does this look like an error" gate
# here — the error is guaranteed present in `tool_error`.
#
# Flow:
#   1. Read `tool_error.message` / `.stderr` / `.stdout` from stdin (the
#      guaranteed-present error payload for this event) and combine them
#      into one error text. If that combined text is empty (defensive only
#      — shouldn't happen on a real PostToolUseFailure), exit 0 silently.
#   2. Pipe the error text to `omnia recall-fix`, which re-derives the
#      exact same error-shaped-line extraction used at save time
#      (store.ExtractErrorText) and returns ONLY signature-lane hits
#      (SignatureMatch==true) — never loose BM25 text hits. Empty output
#      there means "no proven prior fix" and this hook injects nothing.
#
# Dedup: once an observation id has been surfaced in this session, it is
# not re-injected on every later occurrence of the same recurring error — a
# lightweight per-session marker file (keyed by session_id + obs id) tracks
# what has already been shown.
#
# FAIL-QUIET + FAST: any problem here (malformed hook JSON, `omnia` missing
# from PATH, a slow/unreachable local DB) exits 0 with no output. This hook
# must NEVER block or slow down the agent — worst case it just doesn't
# inject a hint this one time. `timeout` bounds the one call that does real
# work (the omnia subprocess) so a stuck/slow invocation can't stall the
# tool loop.
#
# ── Manual self-check (no Claude Code needed) ───────────────────────────────
#   echo '{"tool_name":"Bash","session_id":"manual-test","cwd":"'"$PWD"'","hook_event_name":"PostToolUseFailure","tool_error":{"message":"panic: runtime error: index out of range [7] with length 2","exit_code":1,"stdout":"","stderr":"\tat main.go:42"}}' \
#     | ./post-tool-error-recall.sh | jq .
#   # No matching prior fix (or omnia unavailable) should produce NO output and exit 0:
#   echo '{"tool_name":"Bash","session_id":"manual-test","cwd":"'"$PWD"'","hook_event_name":"PostToolUseFailure","tool_error":{"message":"","exit_code":1,"stdout":"","stderr":""}}' \
#     | ./post-tool-error-recall.sh; echo "exit=$?"

set -u

OMNIA_BIN="${OMNIA_BIN:-omnia}"
RECALL_TIMEOUT_SECS="${OMNIA_RECALL_TIMEOUT_SECS:-3}"

# Load shared helpers (detect_project)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)

# Fail-quiet: if stdin wasn't valid JSON, every jq lookup below yields ""
# (via `// empty`), which naturally falls through to "no injection" instead
# of erroring.
SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)
CWD=$(printf '%s' "$INPUT" | jq -r '.cwd // empty' 2>/dev/null)

# PostToolUseFailure's guaranteed-present error payload (confirmed against
# code.claude.com/docs/en/hooks.md): tool_error.message/.exit_code/.stderr/
# .stdout. Combine message+stderr+stdout as the error source fed to
# `omnia recall-fix` — this event only fires on a real failure, so no
# separate "looks like an error" heuristic gate is needed here.
ERR_MESSAGE=$(printf '%s' "$INPUT" | jq -r '.tool_error.message // empty' 2>/dev/null)
ERR_STDERR=$(printf '%s' "$INPUT" | jq -r '.tool_error.stderr // empty' 2>/dev/null)
ERR_STDOUT=$(printf '%s' "$INPUT" | jq -r '.tool_error.stdout // empty' 2>/dev/null)

ERROR_TEXT="${ERR_MESSAGE}
${ERR_STDERR}
${ERR_STDOUT}"

# Tiny guard: nothing to recall from an empty error payload.
if [ -z "$(printf '%s' "$ERROR_TEXT" | tr -d '[:space:]')" ]; then
  exit 0
fi

# ── Authoritative Go gate: signature-lane recall only ───────────────────────
command -v "$OMNIA_BIN" >/dev/null 2>&1 || exit 0

PROJECT=$(detect_project "$CWD")

RECALL_BLOCK=""
if command -v timeout >/dev/null 2>&1; then
  RECALL_BLOCK=$(printf '%s' "$ERROR_TEXT" | timeout "${RECALL_TIMEOUT_SECS}s" "$OMNIA_BIN" recall-fix --project "$PROJECT" 2>/dev/null)
else
  RECALL_BLOCK=$(printf '%s' "$ERROR_TEXT" | "$OMNIA_BIN" recall-fix --project "$PROJECT" 2>/dev/null)
fi

[ -z "$RECALL_BLOCK" ] && exit 0

# ── Dedup: skip re-injecting ids already surfaced earlier this session ─────
STATE_DIR="${TMPDIR:-/tmp}"
SESSION_KEY="${SESSION_ID:-nosession}"
OBS_IDS=$(printf '%s' "$RECALL_BLOCK" | grep -oE 'obs #[0-9]+' | sed 's/obs #//')

ANY_NEW=0
for OBS_ID in $OBS_IDS; do
  MARKER="${STATE_DIR}/omnia-recall-seen-${SESSION_KEY}-${OBS_ID}"
  if [ ! -f "$MARKER" ]; then
    ANY_NEW=1
  fi
done

if [ "$ANY_NEW" -eq 0 ]; then
  exit 0
fi

for OBS_ID in $OBS_IDS; do
  MARKER="${STATE_DIR}/omnia-recall-seen-${SESSION_KEY}-${OBS_ID}"
  : > "$MARKER" 2>/dev/null || true
done

PREFIX="🔎 Omnia — fix(es) previo(s) para este error (mem_get_observation <id> para el detalle):"
CONTEXT="${PREFIX}
${RECALL_BLOCK}"

jq -n --arg ctx "$CONTEXT" '{hookSpecificOutput: {hookEventName: "PostToolUseFailure", additionalContext: $ctx}}'
exit 0
