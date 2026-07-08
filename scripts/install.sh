#!/bin/sh
# Omnia installer — curl | sh entry point.
#
#   curl -fsSL https://raw.githubusercontent.com/Velion-SpA/omnia/main/scripts/install.sh | sh
#
# Detects OS/arch, downloads the matching release tarball from GitHub
# Releases, verifies it against checksums.txt, and installs the `omnia`
# binary to a directory on your PATH.
#
# POSIX sh only — no bashisms. Must run under dash/ash/busybox sh as well as
# bash, since some environments invoke this via `sh install.sh`.
#
# Env overrides:
#   OMNIA_INSTALL_DIR   install target (default: $HOME/.local/bin, or
#                       /usr/local/bin when run as root)
#   OMNIA_VERSION       install this tag instead of the latest release
#                       (e.g. OMNIA_VERSION=v0.1.0)

set -eu

REPO="Velion-SpA/omnia"
BIN_NAME="omnia"

log() {
    printf '%s\n' "$*"
}

err() {
    printf 'omnia install: %s\n' "$*" >&2
    exit 1
}

need_cmd() {
    if ! command -v "$1" >/dev/null 2>&1; then
        err "missing required command: $1"
    fi
}

detect_os() {
    os=$(uname -s)
    case "$os" in
        Linux) echo "linux" ;;
        Darwin) echo "darwin" ;;
        *) err "unsupported OS: $os (omnia ships prebuilt binaries for linux and darwin only; try 'go install github.com/${REPO}/cmd/omnia@latest')" ;;
    esac
}

detect_arch() {
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) err "unsupported architecture: $arch" ;;
    esac
}

# download URL body
fetch() {
    url="$1"
    out="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$out"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$out"
    else
        err "need curl or wget to download the release"
    fi
}

fetch_stdout() {
    url="$1"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "$url"
    else
        err "need curl or wget to query the GitHub API"
    fi
}

latest_tag() {
    fetch_stdout "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | head -n1 \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'
}

main() {
    need_cmd uname
    need_cmd tar

    os=$(detect_os)
    arch=$(detect_arch)

    tag="${OMNIA_VERSION:-}"
    if [ -z "$tag" ]; then
        log "Looking up the latest omnia release..."
        tag=$(latest_tag)
        [ -n "$tag" ] || err "could not determine the latest release (check https://github.com/${REPO}/releases)"
    fi
    version="${tag#v}"

    archive="omnia_${version}_${os}_${arch}.tar.gz"
    base_url="https://github.com/${REPO}/releases/download/${tag}"

    workdir=$(mktemp -d)
    trap 'rm -rf "$workdir"' EXIT INT TERM

    log "Downloading ${archive} (${tag})..."
    fetch "${base_url}/${archive}" "${workdir}/${archive}"

    if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then
        log "Verifying checksum..."
        if fetch "${base_url}/checksums.txt" "${workdir}/checksums.txt" 2>/dev/null; then
            expected=$(grep " ${archive}\$" "${workdir}/checksums.txt" | awk '{print $1}')
            if [ -n "$expected" ]; then
                if command -v sha256sum >/dev/null 2>&1; then
                    actual=$(sha256sum "${workdir}/${archive}" | awk '{print $1}')
                else
                    actual=$(shasum -a 256 "${workdir}/${archive}" | awk '{print $1}')
                fi
                if [ "$expected" != "$actual" ]; then
                    err "checksum mismatch for ${archive}: expected ${expected}, got ${actual}"
                fi
            else
                log "warning: ${archive} not listed in checksums.txt, skipping verification"
            fi
        else
            log "warning: could not fetch checksums.txt, skipping verification"
        fi
    fi

    log "Extracting..."
    tar -xzf "${workdir}/${archive}" -C "$workdir" "$BIN_NAME"

    if [ -n "${OMNIA_INSTALL_DIR:-}" ]; then
        install_dir="$OMNIA_INSTALL_DIR"
    elif [ "$(id -u)" = "0" ]; then
        install_dir="/usr/local/bin"
    else
        install_dir="$HOME/.local/bin"
    fi

    mkdir -p "$install_dir" 2>/dev/null || true
    if [ -w "$install_dir" ] || [ ! -e "$install_dir" ]; then
        mkdir -p "$install_dir"
        mv "${workdir}/${BIN_NAME}" "${install_dir}/${BIN_NAME}"
        chmod 755 "${install_dir}/${BIN_NAME}"
    else
        log "No write access to ${install_dir}, retrying with sudo..."
        need_cmd sudo
        sudo mkdir -p "$install_dir"
        sudo mv "${workdir}/${BIN_NAME}" "${install_dir}/${BIN_NAME}"
        sudo chmod 755 "${install_dir}/${BIN_NAME}"
    fi

    log ""
    log "omnia ${tag} installed to ${install_dir}/${BIN_NAME}"

    case ":$PATH:" in
        *":${install_dir}:"*) ;;
        *)
            log ""
            log "NOTE: ${install_dir} is not on your PATH. Add this to your shell profile:"
            log "  export PATH=\"${install_dir}:\$PATH\""
            ;;
    esac

    log ""
    log "Next steps:"
    log "  omnia setup                                   # interactive first-run setup"
    log "  omnia cloud add <alias> --server <url>        # add a cloud sync target"
    log "  omnia sync                                     # sync memories to the cloud"
}

main "$@"
