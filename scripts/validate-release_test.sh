#!/bin/sh
# Deterministic tests for scripts/validate-release.sh.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
VALIDATOR="$SCRIPT_DIR/validate-release.sh"
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT INT TERM

fail() {
    printf 'validate-release-test: %s\n' "$*" >&2
    exit 1
}

make_fixture() {
    name="$1"
    dir="$ROOT/$name"
    dist="$dir/dist"
    bin="$dir/bin"
    mkdir -p "$dist/Formula" "$bin"
    : > "$dist/Formula/omnia.rb"
    cat > "$dist/Formula/omnia.rb" <<'FORMULA'
class Omnia < Formula
  desc "Persistent memory for AI coding agents"
  homepage "https://github.com/Velion-SpA/omnia"
  url "https://github.com/Velion-SpA/omnia/releases/download/v0.3.2/omnia_0.3.2_darwin_arm64.tar.gz"
end
FORMULA
    for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64; do
        case "$target" in
            windows_*) ext=zip ;;
            *) ext=tar.gz ;;
        esac
        archive="omnia_0.3.2_${target}.${ext}"
        printf 'fixture:%s\n' "$target" > "$dist/$archive"
    done
    (cd "$dist" && sha256sum omnia_0.3.2_* > checksums.txt)
    cat > "$bin/git" <<'GIT'
#!/bin/sh
if [ "${1:-}" = rev-parse ] && [ "${2:-}" = --verify ]; then
    ref=${3:-}
    case "$ref" in
        v0.3.2\^{commit}) printf '%s\n' "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"; exit 0 ;;
        HEAD) printf '%s\n' "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"; exit 0 ;;
    esac
fi
printf 'unexpected git invocation\n' >&2
exit 1
GIT
    chmod +x "$bin/git"
    printf '%s\n' "$dir"
}

run_ok() {
    dir="$1"
    evidence="$dir/evidence.json"
    output=$(PATH="$dir/bin:$PATH" sh "$VALIDATOR" --tag v0.3.2 --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --dist "$dir/dist" --evidence "$evidence" 2>&1) || fail "expected success: $output"
    [ -s "$evidence" ] || fail "evidence was not written"
    grep -F '"tag":"v0.3.2"' "$evidence" >/dev/null || fail "tag missing from evidence"
    grep -F '"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$evidence" >/dev/null || fail "commit missing from evidence"
    grep -F '"toolchain":"go1.26.4"' "$evidence" >/dev/null || fail "toolchain missing from evidence"
    grep -F '"formula_handoff":"manual"' "$evidence" >/dev/null || fail "manual handoff missing from evidence"
    grep -F '"linux_runtime":"blocked"' "$evidence" >/dev/null || fail "linux acceptance was not marked blocked"
    grep -F '"cloud_runtime":"blocked"' "$evidence" >/dev/null || fail "cloud acceptance was not marked blocked"
    cp "$evidence" "$dir/evidence.first.json"
    PATH="$dir/bin:$PATH" sh "$VALIDATOR" --tag v0.3.2 --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --dist "$dir/dist" --evidence "$evidence" >/dev/null 2>&1 || fail "repeat validation failed"
    cmp -s "$dir/evidence.first.json" "$evidence" || fail "provenance evidence is not deterministic"
}

run_bad() {
    dir="$1"
    needle="$2"
    shift 2
    if output=$(PATH="$dir/bin:$PATH" sh "$VALIDATOR" --tag v0.3.2 --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --dist "$dir/dist" --evidence "$dir/evidence.json" "$@" 2>&1); then
        fail "expected failure containing '$needle'"
    fi
    printf '%s\n' "$output" | grep -F "$needle" >/dev/null || fail "diagnostic did not contain '$needle': $output"
}

# Complete output is accepted and emits stable, explicit evidence.
dir=$(make_fixture complete)
run_ok "$dir"

# GoReleaser v2 writes the formula below a nested Homebrew/Formula directory.
# Keep this fixture aligned with the real snapshot layout so discovery cannot
# regress to the flatter legacy paths.
dir=$(make_fixture real-goreleaser-layout)
mkdir -p "$dir/dist/homebrew/Formula"
mv "$dir/dist/Formula/omnia.rb" "$dir/dist/homebrew/Formula/omnia.rb"
rmdir "$dir/dist/Formula"
run_ok "$dir"

# An explicit FORMULA_PATH remains authoritative even when the bundle uses a
# non-standard location.
dir=$(make_fixture explicit-formula-path)
mkdir -p "$dir/custom"
mv "$dir/dist/Formula/omnia.rb" "$dir/custom/omnia.rb"
evidence="$dir/evidence.json"
FORMULA_PATH="$dir/custom/omnia.rb" PATH="$dir/bin:$PATH" sh "$VALIDATOR" \
    --tag v0.3.2 --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
    --dist "$dir/dist" --evidence "$evidence" >/dev/null 2>&1 \
    || fail "explicit FORMULA_PATH override was rejected"
grep -F "\"path\":\"$dir/custom/omnia.rb\"" "$evidence" >/dev/null \
    || fail "explicit FORMULA_PATH was not reflected in evidence"

dir=$(make_fixture uppercase-checksum)
awk '{print toupper($1), $2}' "$dir/dist/checksums.txt" > "$dir/dist/checksums.upper"
mv "$dir/dist/checksums.upper" "$dir/dist/checksums.txt"
run_ok "$dir"

# Every omitted or mismatched release input blocks handoff before publication.
dir=$(make_fixture missing-target)
rm -f "$dir/dist/omnia_0.3.2_linux_arm64.tar.gz"
run_bad "$dir" 'missing target asset: omnia_0.3.2_linux_arm64.tar.gz'

dir=$(make_fixture checksum-omission)
sed '/omnia_0.3.2_darwin_arm64.tar.gz/d' "$dir/dist/checksums.txt" > "$dir/dist/checksums.tmp"
mv "$dir/dist/checksums.tmp" "$dir/dist/checksums.txt"
run_bad "$dir" 'missing checksum entry: omnia_0.3.2_darwin_arm64.tar.gz'

dir=$(make_fixture checksum-mismatch)
printf 'tampered\n' >> "$dir/dist/omnia_0.3.2_windows_arm64.zip"
run_bad "$dir" 'checksum mismatch: omnia_0.3.2_windows_arm64.zip'

dir=$(make_fixture wrong-tag)
run_bad "$dir" 'tag does not resolve to commit: v0.3.1' --tag v0.3.1

dir=$(make_fixture invalid-tag)
run_bad "$dir" 'invalid tag: v0.3.2"' --tag 'v0.3.2"'

dir=$(make_fixture wrong-commit)
run_bad "$dir" 'tag commit mismatch: expected bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb, got aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' --commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

dir=$(make_fixture wrong-toolchain)
cat > "$dir/bin/go" <<'GO'
#!/bin/sh
printf 'go version go1.25.0 darwin/arm64\n'
GO
chmod +x "$dir/bin/go"
run_bad "$dir" 'Go toolchain mismatch: expected go1.26.4, got go1.25.0'

dir=$(make_fixture formula-owner)
sed 's#Velion-SpA/omnia#other-owner/omnia#g' "$dir/dist/Formula/omnia.rb" > "$dir/dist/Formula/bad.rb"
mv "$dir/dist/Formula/bad.rb" "$dir/dist/Formula/omnia.rb"
run_bad "$dir" 'formula does not reference Velion-SpA/omnia'

workflow="$SCRIPT_DIR/../.github/workflows/release.yml"
[ -f "$workflow" ] || fail "release workflow is missing"
[ "$(grep -c 'goreleaser/goreleaser-action@v7' "$workflow")" -eq 1 ] || fail "workflow must have one no-publish GoReleaser preflight"
grep -F 'gh release create "$GITHUB_REF_NAME"' "$workflow" >/dev/null || fail "workflow must publish with gh release create"
grep -F -- '--verify-tag' "$workflow" >/dev/null || fail "workflow must verify the pushed tag"
grep -F '"dist/omnia_${version}_linux_amd64.tar.gz"' "$workflow" >/dev/null || fail "workflow must upload exact linux amd64 asset"
grep -F '"dist/omnia_${version}_windows_arm64.zip"' "$workflow" >/dev/null || fail "workflow must upload exact windows arm64 asset"
grep -F 'dist/checksums.txt' "$workflow" >/dev/null || fail "workflow must upload checksums"
if grep -E 'args: release --clean$' "$workflow" >/dev/null; then
    fail "workflow must not clean-rebuild after validation"
fi

printf 'validate-release-test: PASS\n'
