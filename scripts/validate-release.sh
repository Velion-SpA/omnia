#!/bin/sh
# Validate a local GoReleaser bundle and emit deterministic release provenance.
#
# Usage: validate-release.sh [--tag TAG] [--commit SHA] [--dist DIR]
#                         [--evidence FILE]
#
# The validator performs local, reproducible checks only. GitHub publication,
# tap mutation, Linux runtime, and cloud acceptance remain explicit external
# gates in the emitted evidence.
set -eu

EXPECTED_TOOLCHAIN=go1.26.4
TAG=${GITHUB_REF_NAME:-}
COMMIT=${GITHUB_SHA:-}
DIST_DIR=${DIST_DIR:-dist}
EVIDENCE=${EVIDENCE:-}
FORMULA_PATH=${FORMULA_PATH:-}

fail() {
    printf 'validate-release: %s\n' "$*" >&2
    exit 1
}

usage() {
    printf '%s\n' "usage: $0 [--tag TAG] [--commit SHA] [--dist DIR] [--evidence FILE]" >&2
    exit 2
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --tag)
            [ "$#" -ge 2 ] || usage
            TAG=$2
            shift 2
            ;;
        --commit)
            [ "$#" -ge 2 ] || usage
            COMMIT=$2
            shift 2
            ;;
        --dist)
            [ "$#" -ge 2 ] || usage
            DIST_DIR=$2
            shift 2
            ;;
        --evidence)
            [ "$#" -ge 2 ] || usage
            EVIDENCE=$2
            shift 2
            ;;
        --help|-h)
            usage
            ;;
        *)
            usage
            ;;
    esac
done

[ -n "$TAG" ] || fail "tag is required (pass --tag or set GITHUB_REF_NAME)"
[ -n "$COMMIT" ] || fail "commit is required (pass --commit or set GITHUB_SHA)"
case "$TAG" in
    *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._/:+-]*) fail "invalid tag: $TAG" ;;
esac
case "$COMMIT" in
    *[!0123456789abcdefABCDEF]*) fail "invalid commit: $COMMIT" ;;
esac
COMMIT=$(printf '%s\n' "$COMMIT" | awk '{print tolower($0)}')
[ -d "$DIST_DIR" ] || fail "distribution directory is unavailable: $DIST_DIR"
[ -n "$EVIDENCE" ] || EVIDENCE="$DIST_DIR/release-provenance.json"

# Bind the claimed tag to the checked-out commit. This deliberately uses the
# checkout's git object database rather than trusting caller-provided metadata.
if ! resolved_commit=$(git rev-parse --verify "${TAG}^{commit}" 2>/dev/null); then
    fail "tag does not resolve to commit: $TAG"
fi
[ "$resolved_commit" = "$COMMIT" ] || fail "tag commit mismatch: expected $COMMIT, got $resolved_commit"

# The release contract fixes the toolchain; do not infer support from fixtures.
command -v go >/dev/null 2>&1 || fail "Go toolchain unavailable (expected $EXPECTED_TOOLCHAIN)"
go_output=$(go version 2>/dev/null) || fail "Go toolchain unavailable (expected $EXPECTED_TOOLCHAIN)"
toolchain=$(printf '%s\n' "$go_output" | sed -n 's/^go version \([^ ]*\).*/\1/p')
[ -n "$toolchain" ] || fail "could not determine Go toolchain from: $go_output"
[ "$toolchain" = "$EXPECTED_TOOLCHAIN" ] || fail "Go toolchain mismatch: expected $EXPECTED_TOOLCHAIN, got $toolchain"

if command -v sha256sum >/dev/null 2>&1; then
    verifier=sha256sum
elif command -v shasum >/dev/null 2>&1; then
    verifier=shasum
else
    fail "SHA-256 verifier unavailable (need sha256sum or shasum)"
fi

checksums="$DIST_DIR/checksums.txt"
[ -f "$checksums" ] || fail "missing checksum manifest: checksums.txt"

version=${TAG#v}
targets=''
assets=''
first_target=true
first_asset=true

checksum_for() {
    archive=$1
    matches=$(awk -v archive="$archive" '($2 == archive || $2 == "*" archive) { count++; value=$1 } END { if (count != 1) exit 1; print value }' "$checksums" 2>/dev/null) || fail "missing checksum entry: $archive"
    case "$matches" in
        ''|*[!0-9A-Fa-f]*) fail "invalid checksum entry: $archive" ;;
    esac
    [ "${#matches}" -eq 64 ] || fail "invalid checksum entry: $archive"
    printf '%s\n' "$matches" | awk '{print tolower($0)}'
}

actual_digest() {
    archive=$1
    if [ "$verifier" = sha256sum ]; then
        digest_output=$(sha256sum "$DIST_DIR/$archive" 2>/dev/null) || fail "SHA-256 verifier failed: $archive"
    else
        digest_output=$(shasum -a 256 "$DIST_DIR/$archive" 2>/dev/null) || fail "SHA-256 verifier failed: $archive"
    fi
    digest=$(printf '%s\n' "$digest_output" | awk 'NF { count++; value=$1 } END { if (count != 1) exit 1; print tolower(value) }') || fail "could not parse archive digest: $archive"
    case "$digest" in
        ''|*[!0-9a-f]*) fail "invalid archive digest: $archive" ;;
    esac
    [ "${#digest}" -eq 64 ] || fail "invalid archive digest: $archive"
    printf '%s\n' "$digest"
}

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
    os=${target%/*}
    arch=${target#*/}
    if [ "$os" = windows ]; then
        extension=zip
    else
        extension=tar.gz
    fi
    archive="omnia_${version}_${os}_${arch}.${extension}"
    [ -f "$DIST_DIR/$archive" ] || fail "missing target asset: $archive"
    expected=$(checksum_for "$archive")
    actual=$(actual_digest "$archive")
    [ "$expected" = "$actual" ] || fail "checksum mismatch: $archive"

    if $first_target; then
        first_target=false
    else
        targets="$targets,"
    fi
    targets="$targets\"$os/$arch\""

    if $first_asset; then
        first_asset=false
    else
        assets="$assets,"
    fi
    assets="$assets{\"name\":\"$archive\",\"target\":\"$os/$arch\",\"sha256\":\"$actual\"}"
done

if [ -z "$FORMULA_PATH" ]; then
    for candidate in \
        "$DIST_DIR/Formula/omnia.rb" \
        "$DIST_DIR/homebrew/Formula/omnia.rb" \
        "$DIST_DIR/homebrew/omnia.rb" \
        "$DIST_DIR/omnia.rb"; do
        if [ -f "$candidate" ]; then
            FORMULA_PATH=$candidate
            break
        fi
    done
fi
[ -n "$FORMULA_PATH" ] || fail "missing generated formula: omnia.rb"
[ -f "$FORMULA_PATH" ] || fail "missing generated formula: $FORMULA_PATH"
grep -F 'Velion-SpA/omnia' "$FORMULA_PATH" >/dev/null 2>&1 || fail "formula does not reference Velion-SpA/omnia"

# Keep evidence path stable relative to the bundle when possible. Absolute
# caller paths are accepted but the JSON content itself contains no timestamps.
formula_rel=$FORMULA_PATH
case "$formula_rel" in
    "$DIST_DIR"/*) formula_rel=${formula_rel#"$DIST_DIR"/} ;;
esac
case "$formula_rel" in
    *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._/-]*) fail "formula path contains unsupported characters: $formula_rel" ;;
esac

mkdir -p "$(dirname "$EVIDENCE")"
cat > "$EVIDENCE" <<JSON
{
  "tag":"$TAG",
  "commit":"$COMMIT",
  "toolchain":"$toolchain",
  "targets":[$targets],
  "assets":[$assets],
  "checksums_file":"checksums.txt",
  "formula":{"path":"$formula_rel","repository":"Velion-SpA/homebrew-tap","name":"omnia"},
  "formula_handoff":"manual",
  "external_acceptance":{"github_publication":"pending","homebrew_tap_mutation":"pending","linux_runtime":"blocked","cloud_runtime":"blocked"}
}
JSON

printf 'validate-release: PASS (%s)\n' "$EVIDENCE"
