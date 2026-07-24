#!/bin/bash
# Engram — UserPromptSubmit hook for Claude Code
#
# On the FIRST message of a session: injects a ToolSearch instruction to force
# Claude Code to load all engram memory tools (which are deferred by default).
#
# On subsequent messages: checks when the last mem_save was for the current
# project. If it's been > 15 minutes AND the session has been active > 5
# minutes, injects a nudge reminding the agent to save.
#
# The nudge is debounced per session: once shown, it stays quiet for
# ENGRAM_NUDGE_COOLDOWN_SECS (default 900s) before it can fire again. Without
# this, an agent that genuinely has nothing to save never resets the
# last-save clock, so the reminder would fire on every single message forever.
#
# SIGNAL-GATED RECALL NUDGE (Play O, omnia v0.3 context economy): gated by
# OMNIA_SIGNAL_RECALL (default unset/off). Bash cannot parse the project's
# config.yaml, so this is a deliberate env-var exception to the config-file
# gating the Go-side v0.3 passes use. When set to a truthy value, cheap
# regex signals (new-topic / uncertainty, EN+ES) in the raw prompt trigger a
# NUDGE instructing the agent to call mem_search — never auto-injected
# results. Firing is limited by BOTH a per-topic dedup marker AND a 300s
# per-session cooldown (so a rapid Q&A session cannot nudge every turn), and
# a fired signal nudge is combined with the save-nudge below into a single
# systemMessage when both are due that turn, rather than the signal nudge
# preempting the save-nudge check entirely. See the SIGNAL-GATED RECALL
# NUDGE section below for details.
#
# MUST exit 0 always and output valid JSON — otherwise Claude Code blocks the message.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

# Windows Git Bash/MSYS2 can fail while forking helper processes under
# enterprise Defender/EDR, which makes Claude Code wait on prompt submission.
# Keep the Windows path bash-builtin-only: no jq, git, curl, date, dirname, cat,
# touch, or command substitutions. It preserves first-message tool loading and
# degrades subsequent save nudges to a fast no-op instead of risking a hang.
is_windows_bash() {
  case "${OSTYPE:-}" in
    msys*|cygwin*|win32*) return 0 ;;
  esac
  [ -n "${MSYSTEM:-}" ] || [ -n "${MINGW_PREFIX:-}" ]
}

set_json_string_value() {
  local key="$1"
  local json="$2"
  local pattern='"'"$key"'"[[:space:]]*:[[:space:]]*"([^"]*)"'
  JSON_VALUE=""
  if [[ "$json" =~ $pattern ]]; then
    JSON_VALUE="${BASH_REMATCH[1]}"
  fi
}

sanitize_session_key_part() {
  local raw="$1"
  local safe=""
  local i char
  for (( i=0; i<${#raw}; i++ )); do
    char="${raw:i:1}"
    if [[ "$char" =~ [a-zA-Z0-9_-] ]]; then
      safe+="$char"
    else
      safe+="_"
    fi
  done
  JSON_VALUE="$safe"
}

print_toolsearch_message() {
  printf '%s\n' '{"systemMessage":"CRITICAL FIRST ACTION — Execute this ToolSearch NOW before responding to the user:\nselect:mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge\n\nAfter loading tools, call mem_context to check for prior session history before responding."}'
}

if is_windows_bash && [ "${ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE:-auto}" != "0" ]; then
  INPUT=""
  while IFS= read -r LINE || [ -n "$LINE" ]; do
    INPUT+="${LINE}"$'\n'
  done

  set_json_string_value "session_id" "$INPUT"
  SESSION_ID="$JSON_VALUE"
  if [ -n "$SESSION_ID" ]; then
    sanitize_session_key_part "$SESSION_ID"
    SESSION_KEY="engram-claude-${JSON_VALUE}-tools-loaded"
  else
    SESSION_KEY="engram-claude-windows-$$-tools-loaded"
  fi
  STATE_DIR="${TMPDIR:-/tmp}"
  STATE_FILE="${STATE_DIR}/${SESSION_KEY}"

  if [ ! -f "$STATE_FILE" ]; then
    : > "$STATE_FILE" 2>/dev/null || true
    print_toolsearch_message
    exit 0
  fi

  printf '%s\n' '{}'
  exit 0
fi

# Load shared helpers after the Windows-safe fast path so Git Bash does not fork
# for dirname/pwd before deciding whether the safe path applies.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')

parse_epoch() {
  TS="$1"
  if [ -z "$TS" ]; then
    return 1
  fi

  # Drop fractional seconds without dropping timezone information.
  if [[ "$TS" == *.* ]]; then
    TS_PREFIX="${TS%%.*}"
    TS_SUFFIX="${TS#*.}"
    case "$TS_SUFFIX" in
      *Z) TS="${TS_PREFIX}Z" ;;
      *+*) TS="${TS_PREFIX}+${TS_SUFFIX#*+}" ;;
      *-*) TS="${TS_PREFIX}-${TS_SUFFIX#*-}" ;;
      *) TS="$TS_PREFIX" ;;
    esac
  fi

  # BSD date accepts numeric RFC3339 offsets with %z, but requires +HHMM.
  if [[ "$TS" =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})([+-][0-9]{2}):([0-9]{2})$ ]]; then
    TZ_TS="${BASH_REMATCH[1]}${BASH_REMATCH[2]}${BASH_REMATCH[3]}"
    date -j -f "%Y-%m-%dT%H:%M:%S%z" "$TZ_TS" "+%s" 2>/dev/null && return 0
  fi
  if [[ "$TS" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[+-][0-9]{4}$ ]]; then
    date -j -f "%Y-%m-%dT%H:%M:%S%z" "$TS" "+%s" 2>/dev/null && return 0
  fi

  if [[ "$TS" == *Z ]]; then
    Z_TS="${TS%Z}"
    date -j -u -f "%Y-%m-%dT%H:%M:%S" "$Z_TS" "+%s" 2>/dev/null && return 0
  fi

  date -j -f "%Y-%m-%dT%H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -j -f "%Y-%m-%d %H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -d "$TS" "+%s" 2>/dev/null
}

# Default: no injection
OUTPUT="{}"

# ──────────────────────────────────────────────────────────────────────────────
# FIRST-MESSAGE DETECTION
#
# Use a state file per session to determine if this is the first user message.
# State file lives in /tmp and is keyed by session_id (falls back to project+pid).
# ──────────────────────────────────────────────────────────────────────────────

# Build a stable session key — prefer SESSION_ID, fall back to project name
if [ -n "$SESSION_ID" ]; then
  SESSION_KEY="engram-claude-${SESSION_ID}-tools-loaded"
else
  # No session ID available — only then detect project for the fallback state key.
  PROJECT=$(detect_project "$CWD")
  SAFE_PROJECT=$(printf '%s' "${PROJECT:-unknown}" | tr -cs 'a-zA-Z0-9_-' '_')
  SESSION_KEY="engram-claude-${SAFE_PROJECT}-$$-tools-loaded"
fi

STATE_FILE="/tmp/${SESSION_KEY}"

if [ ! -f "$STATE_FILE" ]; then
  # ── FIRST MESSAGE ────────────────────────────────────────────────────────────
  # Create the state file immediately to prevent repeat injections
  touch "$STATE_FILE" 2>/dev/null || true

  # Inject ToolSearch + mem_context instruction.
  print_toolsearch_message
  exit 0
fi

# ──────────────────────────────────────────────────────────────────────────────
# SIGNAL-GATED RECALL NUDGE (Play O — omnia v0.3 context economy)
#
# When OFF (default): is_signal_recall_enabled returns false, this whole block
# is skipped, and output falls through unaffected — byte-for-byte identical to
# pre-v0.3 behavior.
#
# When ON: detects two cheap, LLM-free signals in the raw prompt text —
# new-topic (prompt opens with an imperative verb, EN+ES) and uncertainty
# (question words/markers or a trailing "?", EN+ES). On a hit, sets
# SIGNAL_NUDGE_TEXT to a NUDGE instructing the agent to call mem_search with
# extracted keywords — it does NOT print/exit here. This is deliberately an
# INSTRUCTION, never injected search results — results must still flow
# through the normal mem_search path so budget/diversity/type-lens gates
# still apply; this hook only decides WHEN to suggest searching. The
# save-nudge logic below still runs afterward (see compute_save_nudge) and
# the two are combined into a single systemMessage if both fire the same
# turn, instead of the signal nudge starving the save-nudge every time.
#
# Detection runs against a Spanish-accent-normalized copy of the prompt (see
# normalize_es_accents) so matching is correct under ANY locale, including
# LC_ALL=C/POSIX where bracket-expression accent classes (e.g. [oó]) do not
# reliably match multi-byte UTF-8 characters. Extracted keywords are pulled
# from the same normalized text for the same reason.
#
# Firing is gated by TWO independent limiters:
#   1. Topic dedup (post-tool-error-recall.sh marker-file idiom): a checksum
#      of (trigger-kind + extracted terms) keys a per-session marker file, so
#      the SAME topic/uncertainty doesn't repeat within a session.
#   2. Session cooldown: a separate per-session marker records the epoch of
#      the last fired signal nudge; a NEW/different topic within
#      OMNIA_SIGNAL_RECALL_COOLDOWN_SECS (default 300s) of the last one is
#      still suppressed. Without this, distinct topic hashes (e.g. a rapid
#      back-and-forth Q&A session, each question textually different) would
#      each pass topic dedup and nudge on every turn — exactly what the
#      "must not fire every turn" requirement forbids.
#
# New signal-recall marker/cooldown files live under ${TMPDIR:-/tmp} (not the
# pre-existing save-nudge STATE_FILE, which stays as-is/out of scope), per
# design.md's `${TMPDIR:-/tmp}/omnia-signal-recall-<session>-<hash>` format.
# ──────────────────────────────────────────────────────────────────────────────

is_signal_recall_enabled() {
  case "${OMNIA_SIGNAL_RECALL:-}" in
    1|true|TRUE|True|yes|YES|on|ON) return 0 ;;
    *) return 1 ;;
  esac
}

# Transliterate the small set of Spanish accented letters (both cases) to
# their ASCII base letter by matching explicit UTF-8 byte sequences via sed.
# This is locale-independent: matching a literal byte sequence does not
# depend on LC_ALL/LANG being UTF-8-aware, so it is correct on both BSD sed
# (macOS) and GNU sed even under LC_ALL=C/POSIX.
normalize_es_accents() {
  sed \
    -e "s/$(printf '\303\241')/a/g" \
    -e "s/$(printf '\303\251')/e/g" \
    -e "s/$(printf '\303\255')/i/g" \
    -e "s/$(printf '\303\263')/o/g" \
    -e "s/$(printf '\303\272')/u/g" \
    -e "s/$(printf '\303\261')/n/g" \
    -e "s/$(printf '\303\201')/A/g" \
    -e "s/$(printf '\303\211')/E/g" \
    -e "s/$(printf '\303\215')/I/g" \
    -e "s/$(printf '\303\223')/O/g" \
    -e "s/$(printf '\303\232')/U/g" \
    -e "s/$(printf '\303\221')/N/g"
}

# Returns success (0) if a signal nudge already fired within the cooldown
# window recorded in $1 (the cooldown marker file), given window $2 seconds.
signal_cooldown_active() {
  local marker="$1" window="$2" last="" now
  [ -f "$marker" ] || return 1
  read -r last < "$marker" 2>/dev/null || last=""
  case "$last" in
    ''|*[!0-9]*) return 1 ;;
  esac
  now=$(date "+%s")
  [ "$(( now - last ))" -lt "$window" ]
}

SIGNAL_NUDGE_TEXT=""

if is_signal_recall_enabled; then
  PROMPT_TEXT=$(printf '%s' "$INPUT" | jq -r '.prompt // empty' 2>/dev/null)

  if [ -n "$PROMPT_TEXT" ]; then
    PROMPT_NORM=$(printf '%s' "$PROMPT_TEXT" | normalize_es_accents)
    TRIGGER_KIND=""
    APOS="'"

    # New-topic: prompt opens with an imperative verb (EN+ES), case-insensitive.
    # Spanish alternatives are written in their normalized (accent-stripped)
    # form since PROMPT_NORM already went through normalize_es_accents.
    if printf '%s' "$PROMPT_NORM" | grep -qiE '^[[:space:]]*(implement|add|fix|build|create|refactor|debug|investigate|explore|design|write|update|migrate|haceme|hace|arregla|implementa|agrega|crea|disena|arma|revisa)\b'; then
      TRIGGER_KIND="new-topic"
    else
      # Uncertainty: question words/markers (EN+ES, per design.md §3.4) or a
      # trailing question mark.
      UNCERTAINTY_RE="\b(how|why|failing|not sure|stuck|what${APOS}?s|isn${APOS}?t working|no se|como|por que|no funciona|falla)\b|\?[[:space:]]*\$"
      if printf '%s' "$PROMPT_NORM" | grep -qiE "$UNCERTAINTY_RE"; then
        TRIGGER_KIND="uncertainty"
      fi
    fi

    if [ -n "$TRIGGER_KIND" ]; then
      # Cheap keyword extraction from the normalized text: lowercase, strip
      # punctuation, drop a small EN+ES stopword list, keep the first 8
      # remaining words. No LLM involved.
      TERMS=$(printf '%s' "$PROMPT_NORM" \
        | tr '[:upper:]' '[:lower:]' \
        | tr -c '[:alnum:]' ' ' \
        | tr -s ' ' \
        | tr ' ' '\n' \
        | grep -vE '^(the|a|an|to|of|in|on|for|and|or|is|are|it|this|that|with|please|el|la|los|las|de|que|un|una|y|o|es|para|con|por|favor)$' \
        | head -n 8 \
        | tr '\n' ' ' \
        | sed 's/[[:space:]]*$//')

      if [ -n "$TERMS" ]; then
        SIGNAL_HASH=$(printf '%s' "${TRIGGER_KIND}:${TERMS}" | cksum | awk '{print $1}')
        SIGNAL_STATE_DIR="${TMPDIR:-/tmp}"
        SIGNAL_SESSION_ID="${SESSION_KEY#engram-claude-}"
        SIGNAL_SESSION_ID="${SIGNAL_SESSION_ID%-tools-loaded}"
        SIGNAL_MARKER_PREFIX="${SIGNAL_STATE_DIR}/omnia-signal-recall-${SIGNAL_SESSION_ID}"
        SIGNAL_MARKER="${SIGNAL_MARKER_PREFIX}-${SIGNAL_HASH}"
        SIGNAL_COOLDOWN_MARKER="${SIGNAL_MARKER_PREFIX}-last"
        SIGNAL_COOLDOWN_SECS="${OMNIA_SIGNAL_RECALL_COOLDOWN_SECS:-300}"

        # Dedup (same topic already nudged this session) AND cooldown (ANY
        # signal nudge fired too recently) both gate firing.
        if [ ! -f "$SIGNAL_MARKER" ] && ! signal_cooldown_active "$SIGNAL_COOLDOWN_MARKER" "$SIGNAL_COOLDOWN_SECS"; then
          : > "$SIGNAL_MARKER" 2>/dev/null || true
          SIGNAL_NOW_EPOCH=$(date "+%s")
          # NOTE: must include the trailing newline — `read` returns non-zero
          # on EOF-without-newline even though it populates the variable,
          # which would trip the `read ... || reset` guard below and silently
          # defeat the cooldown on every check.
          printf '%s\n' "$SIGNAL_NOW_EPOCH" > "$SIGNAL_COOLDOWN_MARKER" 2>/dev/null || true

          # Opportunistic, failure-silent cleanup of stale signal-recall
          # marker files (older than 1 day) so /tmp does not accumulate them
          # indefinitely across sessions.
          find "$SIGNAL_STATE_DIR" -maxdepth 1 -name 'omnia-signal-recall-*' -type f -mtime +1 -delete 2>/dev/null || true

          SIGNAL_NUDGE_TEXT="MEMORY NUDGE (${TRIGGER_KIND}): before answering, call mem_search with keywords: \"${TERMS}\" to check for related prior work."
        fi
      fi
    fi
  fi
fi

# ──────────────────────────────────────────────────────────────────────────────
# SUBSEQUENT MESSAGES — existing save-nudge logic
#
# Wrapped in a function (returns instead of exiting) so a signal nudge fired
# above does not preempt this check — it is ALWAYS evaluated, and its result
# (still in $OUTPUT, unchanged in shape/content from before this refactor) is
# combined with any fired SIGNAL_NUDGE_TEXT below. Cooldown-state semantics
# are unchanged: NUDGE_STATE_FILE is only written when the save-nudge itself
# actually fires (same condition, same file, same value, as before).
# ──────────────────────────────────────────────────────────────────────────────

compute_save_nudge() {
  # Detect project only after the first-message path has had a chance to return.
  if [ -z "${PROJECT:-}" ]; then
    PROJECT=$(detect_project "$CWD")
  fi

  # Bail early if we can't determine the project
  if [ -z "$PROJECT" ]; then
    return 0
  fi

  # Get session start time to check if session is > 5 minutes old
  SESSION_START=""
  if [ -n "$SESSION_ID" ]; then
    SESSION_START=$(curl -sf "${ENGRAM_URL}/sessions/${SESSION_ID}" --max-time 0.2 2>/dev/null \
      | jq -r '.started_at // empty' 2>/dev/null)
  fi

  # Check session age — skip nudge if session is new (< 5 minutes)
  if [ -n "$SESSION_START" ]; then
    SESSION_START_EPOCH=$(parse_epoch "$SESSION_START")
    if [ -z "$SESSION_START_EPOCH" ]; then
      return 0
    fi
    NOW_EPOCH=$(date "+%s")
    SESSION_AGE_SECS=$(( NOW_EPOCH - SESSION_START_EPOCH ))

    if [ "$SESSION_AGE_SECS" -lt 300 ]; then
      # Session < 5 minutes old — no nudge yet
      return 0
    fi
  fi

  # Fetch the most recent observation for this project (any type)
  ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
  LAST_SAVE_JSON=$(curl -sf \
    "${ENGRAM_URL}/observations?project=${ENCODED_PROJECT}&limit=1&sort=created_at:desc" \
    --max-time 0.2 2>/dev/null)

  if [ -z "$LAST_SAVE_JSON" ]; then
    # Server not responding or slow — fail silently, no nudge
    return 0
  fi

  LAST_SAVE_AT=$(echo "$LAST_SAVE_JSON" | jq -r '.[0].created_at // empty' 2>/dev/null)

  if [ -z "$LAST_SAVE_AT" ]; then
    # No observations yet — no nudge (session might just be starting)
    return 0
  fi

  # Parse last save timestamp and compare to now
  LAST_EPOCH=$(parse_epoch "$LAST_SAVE_AT")
  if [ -z "$LAST_EPOCH" ]; then
    return 0
  fi
  NOW_EPOCH=$(date "+%s")
  ELAPSED=$(( NOW_EPOCH - LAST_EPOCH ))

  # Nudge if last save was > 15 minutes ago (900 seconds), but debounce so we do
  # not repeat the reminder on every message while the agent has nothing to save.
  if [ "$ELAPSED" -gt 900 ]; then
    NUDGE_COOLDOWN="${ENGRAM_NUDGE_COOLDOWN_SECS:-900}"
    NUDGE_STATE_FILE="${STATE_FILE%-tools-loaded}-last-nudge"

    LAST_NUDGE_EPOCH=""
    if [ -f "$NUDGE_STATE_FILE" ]; then
      read -r LAST_NUDGE_EPOCH < "$NUDGE_STATE_FILE" 2>/dev/null || LAST_NUDGE_EPOCH=""
    fi
    # Ignore a corrupt/non-numeric state file — treat as "never nudged".
    case "$LAST_NUDGE_EPOCH" in
      ''|*[!0-9]*) LAST_NUDGE_EPOCH="" ;;
    esac

    if [ -z "$LAST_NUDGE_EPOCH" ] || [ "$(( NOW_EPOCH - LAST_NUDGE_EPOCH ))" -ge "$NUDGE_COOLDOWN" ]; then
      printf '%s' "$NOW_EPOCH" > "$NUDGE_STATE_FILE" 2>/dev/null || true
      OUTPUT=$(jq -n \
        '{"systemMessage": "MEMORY REMINDER: It'\''s been over 15 minutes since your last save. If you'\''ve made decisions, discoveries, or completed significant work, call mem_save now."}')
    fi
  fi

  return 0
}

compute_save_nudge

# Combine a fired signal nudge with a fired save nudge into ONE systemMessage
# when both are due this turn; emit the signal nudge alone when only it
# fired; otherwise fall through to the save-nudge's own output ($OUTPUT,
# "{}" when neither fired) — byte-for-byte identical to pre-v0.3 behavior
# whenever SIGNAL_NUDGE_TEXT is empty (signal-gated recall OFF or no trigger).
if [ -n "$SIGNAL_NUDGE_TEXT" ]; then
  if [ "$OUTPUT" != "{}" ]; then
    SAVE_NUDGE_TEXT=$(printf '%s' "$OUTPUT" | jq -r '.systemMessage')
    COMBINED_TEXT="${SIGNAL_NUDGE_TEXT}

${SAVE_NUDGE_TEXT}"
    jq -n --arg msg "$COMBINED_TEXT" '{"systemMessage": $msg}'
  else
    jq -n --arg msg "$SIGNAL_NUDGE_TEXT" '{"systemMessage": $msg}'
  fi
else
  echo "$OUTPUT"
fi
exit 0
