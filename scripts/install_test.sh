#!/bin/sh
# Deterministic checksum contract tests for scripts/install.sh.

set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT INT TERM

HOST_TAR=$(command -v tar)
HOST_CP=$(command -v cp)
HOST_MKDIR=$(command -v mkdir)
HOST_RM=$(command -v rm)
HOST_SHASUM=$(command -v shasum 2>/dev/null || true)
HOST_SHA256SUM=$(command -v sha256sum 2>/dev/null || true)

fail() {
    printf 'install tests: %s\n' "$*" >&2
    exit 1
}

link_command() {
    name=$1
    path=$2
    ln -s "$path" "$COMMAND_DIR/$name"
}

digest() {
    if [ -n "$HOST_SHA256SUM" ]; then
        "$HOST_SHA256SUM" "$1" | awk '{print $1}'
    else
        "$HOST_SHASUM" -a 256 "$1" | awk '{print $1}'
    fi
}

make_case() {
    case_name=$1
    case_dir="$TEST_ROOT/$case_name"
    fixture_dir="$case_dir/fixture"
    COMMAND_DIR="$case_dir/commands"
    mkdir -p "$fixture_dir/bin" "$COMMAND_DIR"
    printf '#!/bin/sh\n' > "$fixture_dir/bin/omnia"
    printf 'fixture binary\n' >> "$fixture_dir/bin/omnia"
    chmod 755 "$fixture_dir/bin/omnia"
    "$HOST_TAR" -czf "$fixture_dir/archive.tar.gz" -C "$fixture_dir/bin" omnia
    archive_digest=$(digest "$fixture_dir/archive.tar.gz")
    archive_name=omnia_0.3.2_linux_amd64.tar.gz
    case "$case_name" in
        valid)
            printf '%s  %s\n' "$archive_digest" "$archive_name" > "$fixture_dir/checksums.txt"
            ;;
        uppercase)
            archive_digest_upper=$(printf '%s\n' "$archive_digest" | awk '{print toupper($0)}')
            printf '%s  %s\n' "$archive_digest_upper" "$archive_name" > "$fixture_dir/checksums.txt"
            ;;
        mismatch)
            printf '%064d  %s\n' 0 "$archive_name" > "$fixture_dir/checksums.txt"
            ;;
        missing-manifest)
            :
            ;;
        missing-entry)
            printf '%s  omnia_0.3.2_darwin_amd64.tar.gz\n' "$archive_digest" > "$fixture_dir/checksums.txt"
            ;;
        invalid-digest)
            printf 'not-a-sha256-digest  %s\n' "$archive_name" > "$fixture_dir/checksums.txt"
            ;;
        verifier-failure)
            printf '%s  %s\n' "$archive_digest" "$archive_name" > "$fixture_dir/checksums.txt"
            ;;
    esac

    # The child process has a controlled PATH. Include only the commands the
    # installer needs, plus test doubles for platform, download, and tar.
    link_command awk "$(command -v awk)"
    link_command chmod "$(command -v chmod)"
    link_command cp "$HOST_CP"
    link_command grep "$(command -v grep)"
    link_command id "$(command -v id)"
    link_command mkdir "$HOST_MKDIR"
    link_command mktemp "$(command -v mktemp)"
    link_command mv "$(command -v mv)"
    link_command rm "$HOST_RM"
    link_command uname "$fixture_dir/uname"
    link_command curl "$fixture_dir/curl"
    link_command tar "$fixture_dir/tar"

    cat > "$fixture_dir/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
    -s) printf 'Linux\n' ;;
    -m) printf 'x86_64\n' ;;
    *) exit 1 ;;
esac
EOF
    cat > "$fixture_dir/curl" <<'EOF'
#!/bin/sh
url=
out=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) out=$2; shift 2 ;;
        *) url=$1; shift ;;
    esac
done
name=${url##*/}
case "$name" in
    checksums.txt)
        [ "${INSTALL_CASE:-}" != missing-manifest ] || exit 22
        [ -f "$FIXTURE_DIR/checksums.txt" ] || exit 22
        cp "$FIXTURE_DIR/checksums.txt" "$out"
        ;;
    *.tar.gz) cp "$FIXTURE_DIR/archive.tar.gz" "$out" ;;
    *) printf 'unexpected URL: %s\n' "$url" >&2; exit 22 ;;
esac
EOF
    cat > "$fixture_dir/tar" <<'EOF'
#!/bin/sh
case " $* " in
    *' -xzf '*) : > "$TAR_MARKER" ;;
esac
exec "$HOST_TAR" "$@"
EOF
    chmod 755 "$fixture_dir/uname" "$fixture_dir/curl" "$fixture_dir/tar"

    if [ "$case_name" != unavailable-verifier ]; then
        if [ "$case_name" = verifier-failure ]; then
            cat > "$COMMAND_DIR/sha256sum" <<'EOF'
#!/bin/sh
exit 42
EOF
        else
            cat > "$COMMAND_DIR/sha256sum" <<EOF
#!/bin/sh
exec "$HOST_SHA256SUM" "\$@"
EOF
            if [ -z "$HOST_SHA256SUM" ]; then
                cat > "$COMMAND_DIR/sha256sum" <<EOF
#!/bin/sh
exec "$HOST_SHASUM" -a 256 "\$@"
EOF
            fi
        fi
        chmod 755 "$COMMAND_DIR/sha256sum"
    fi
}

run_case() {
    case_name=$1
    case_dir="$TEST_ROOT/$case_name"
    fixture_dir="$case_dir/fixture"
    COMMAND_DIR="$case_dir/commands"
    install_dir="$case_dir/install"
    output_file="$case_dir/output"
    tar_marker="$case_dir/tar-extracted"
    make_case "$case_name"
    if PATH="$COMMAND_DIR" \
        FIXTURE_DIR="$fixture_dir" \
        INSTALL_CASE="$case_name" \
        HOST_TAR="$HOST_TAR" \
        TAR_MARKER="$tar_marker" \
        HOME="$case_dir/home" \
        OMNIA_INSTALL_DIR="$install_dir" \
        OMNIA_VERSION=v0.3.2 \
        /bin/sh "$ROOT/scripts/install.sh" >"$output_file" 2>&1; then
        status=0
    else
        status=$?
    fi
    printf '%s\t%s\t%s\n' "$case_name" "$status" "$output_file"
}

assert_success() {
    result=$1
    case_name=${result%%	*}
    case_dir="$TEST_ROOT/$case_name"
    [ "${result#*	}" != "$result" ] || fail "malformed result for $case_name"
    status=${result#*	}; status=${status%%	*}
    [ "$status" -eq 0 ] || { cat "$case_dir/output" >&2; fail "$case_name should pass"; }
    [ -x "$case_dir/install/omnia" ] || fail "$case_name did not install omnia"
    [ -f "$case_dir/tar-extracted" ] || fail "$case_name did not extract archive"
}

assert_failure() {
    result=$1
    case_name=${result%%	*}
    case_dir="$TEST_ROOT/$case_name"
    status=${result#*	}; status=${status%%	*}
    [ "$status" -ne 0 ] || { cat "$case_dir/output" >&2; fail "$case_name should fail"; }
    [ ! -e "$case_dir/tar-extracted" ] || fail "$case_name extracted before verification failed"
    [ ! -e "$case_dir/install/omnia" ] || fail "$case_name installed a binary after failure"
}

valid=$(run_case valid)
assert_success "$valid"

uppercase=$(run_case uppercase)
assert_success "$uppercase"

mismatch=$(run_case mismatch)
assert_failure "$mismatch"
grep -F 'checksum mismatch for omnia_0.3.2_linux_amd64.tar.gz' "$TEST_ROOT/mismatch/output" >/dev/null || fail 'mismatch diagnostic changed'

missing_manifest=$(run_case missing-manifest)
assert_failure "$missing_manifest"
grep -F 'checksums.txt is unavailable' "$TEST_ROOT/missing-manifest/output" >/dev/null || fail 'missing manifest diagnostic changed'

missing_entry=$(run_case missing-entry)
assert_failure "$missing_entry"
grep -F 'is not listed in checksums.txt' "$TEST_ROOT/missing-entry/output" >/dev/null || fail 'missing entry diagnostic changed'

unavailable=$(run_case unavailable-verifier)
assert_failure "$unavailable"
grep -F 'no SHA-256 verifier available' "$TEST_ROOT/unavailable-verifier/output" >/dev/null || fail 'unavailable verifier diagnostic changed'

invalid_digest=$(run_case invalid-digest)
assert_failure "$invalid_digest"
grep -F 'invalid SHA-256 digest in checksums.txt' "$TEST_ROOT/invalid-digest/output" >/dev/null || fail 'invalid digest diagnostic changed'

verifier_failure=$(run_case verifier-failure)
assert_failure "$verifier_failure"
grep -F 'SHA-256 verifier failed (sha256sum)' "$TEST_ROOT/verifier-failure/output" >/dev/null || fail 'verifier failure diagnostic changed'

printf 'install tests: PASS (valid, uppercase, mismatch, missing manifest, missing entry, invalid digest, unavailable verifier, verifier failure)\n'
