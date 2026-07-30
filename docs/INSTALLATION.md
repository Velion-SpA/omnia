[← Back to README](../README.md)

# Installation

- [Homebrew (macOS / Linux)](#homebrew-macos--linux)
- [Windows](#windows)
- [Install from source (macOS / Linux)](#install-from-source-macos--linux)
- [Download binary (all platforms)](#download-binary-all-platforms)
- [Requirements](#requirements)
- [Environment Variables](#environment-variables)
- [Windows Config Paths](#windows-config-paths)

---

## Homebrew (macOS / Linux)

```bash
brew install velion-spa/tap/omnia
```

Upgrade to latest:

```bash
brew update && brew upgrade omnia
```

> **Migrating from the legacy Engram Cask?** If you installed Engram before the Omnia rebrand, it was distributed as a Cask. Uninstall the legacy package first, then install Omnia:
> ```bash
> brew uninstall --cask engram 2>/dev/null; brew install velion-spa/tap/omnia
> ```

> **Keep `omnia serve` running across `brew upgrade`?** On macOS, `brew upgrade omnia` replaces the binary and kills any running `omnia serve` process — autosync stops silently until you relaunch it. To make autosync survive upgrades and reboots, use the launchd template in [Running as a Service → Using launchd (macOS)](../DOCS.md#using-launchd-macos). Run `omnia cloud status` afterwards: the `Local daemon:` line should report `running`.

---

## Windows

**Option A: Install from the canonical source (recommended for technical users)**

If you have Go installed, clone the canonical repository and compile locally. This avoids external module paths and keeps the source auditable:

```powershell
git clone https://github.com/Velion-SpA/omnia.git
cd omnia
go install ./cmd/omnia
# Binary goes to %GOPATH%\bin\omnia.exe (typically %USERPROFILE%\go\bin\)
```

Ensure `%GOPATH%\bin` (or `%USERPROFILE%\go\bin`) is on your `PATH`.

**Option B: Version-stamp a source build**

From the `omnia` checkout, choose one of these version-stamped build paths:

> **Want a real version string instead of `dev`?**
>
> `go install` always stamps the binary as `dev`. To get a meaningful version, pick one of these — not both. Running them both leaves two binaries on disk and `omnia version` keeps reporting `dev` because PATH still resolves to the `go install` build.
>
> **Option B1 — version-stamped `go install` (binary stays on PATH):**
>
> ```powershell
> $v = git describe --tags --always
> go install -ldflags="-X main.version=local-$v" ./cmd/omnia
> ```
>
> **Option B2 — `go build` and move the result onto PATH:**
>
> ```powershell
> $v = git describe --tags --always
> go build -ldflags="-X main.version=local-$v" -o omnia.exe ./cmd/omnia
> Move-Item -Force omnia.exe "$env:USERPROFILE\go\bin\omnia.exe"
> ```
>
> After either option, `omnia version` should print `local-<git-describe>` instead of `dev`.

**Option C: Download the prebuilt binary**

1. Go to [GitHub Releases](https://github.com/Velion-SpA/omnia/releases)
2. Download `omnia_<version>_windows_amd64.zip` (or `arm64` for ARM devices) **and the matching `checksums.txt` from the same release**
3. Verify the archive's SHA-256 digest before extracting it:
4. Extract `omnia.exe` to a folder in your `PATH` (e.g. `C:\Users\<you>\bin\`)

```powershell
# Fail closed on missing files, malformed checksum entries, or command errors.
$ErrorActionPreference = 'Stop'

# Verify the selected archive against the same-release checksums.txt.
$archives = @(Get-Item .\omnia_*_windows_*.zip)
if ($archives.Count -ne 1) { throw "expected exactly one Windows archive, found $($archives.Count)" }
$archive = $archives[0]
$checksumMatches = @(Select-String -Path .\checksums.txt -Pattern ("^\s*[0-9A-Fa-f]{64}\s+\*?" + [regex]::Escape($archive.Name) + "\s*$"))
if ($checksumMatches.Count -ne 1) { throw "expected exactly one checksum entry for $($archive.Name)" }
$checksumFields = $checksumMatches[0].Line.Trim() -split '\s+'
$expected = $checksumFields[0].ToLowerInvariant()
if ($checksumFields.Count -lt 2 -or $checksumFields[1].TrimStart('*') -ne $archive.Name -or $expected -notmatch '^[0-9a-f]{64}$') { throw "invalid checksum entry for $($archive.Name)" }
$actual = (Get-FileHash -Path $archive.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -notmatch '^[0-9a-f]{64}$') { throw "invalid SHA-256 result for $($archive.Name)" }
if ($actual -ne $expected.ToLowerInvariant()) { throw "SHA-256 mismatch for $($archive.Name)" }

# Extract only after verification succeeds, then add the directory to PATH.
Expand-Archive -Path $archive.FullName -DestinationPath "$env:USERPROFILE\bin"
# Add to PATH permanently (run once):
[Environment]::SetEnvironmentVariable("Path", "$env:USERPROFILE\bin;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

> **Antivirus false positives on prebuilt binaries**
>
> Windows Defender and other antivirus tools (ESET, Brave's built-in scanner) have flagged some
> Omnia prebuilt releases as malware (`Trojan:Script/Wacatac.H!ml` or similar). This is a
> **heuristic false positive**. The binary is built reproducibly from the public source code
> via GoReleaser and contains no malicious code.
>
> **Why does this happen?** Prebuilt binaries from small open-source projects are unsigned (code
> signing certificates cost hundreds of dollars per year). Many AV engines automatically flag
> unsigned executables from unknown publishers, especially recently compiled Go binaries. The
> same alert has been observed on Claude Code's own MSIX installer, which confirms this is an
> AV heuristic issue, not a code problem.
>
> **Maintainer stance:** We will not pay for a code signing certificate at this time. This is a
> distribution trust problem, not a security problem. The source code is fully auditable.
>
> **Recommended workaround:** Technical Windows users should prefer **Option A (`go install`)** or
> **Option B (build from source)**. Binaries you compile locally will not trigger AV alerts because
> they originate from your own machine.

> **Other Windows notes:**
> - Data is stored in `%USERPROFILE%\.omnia\omnia.db` (legacy Engram installs may still use `%USERPROFILE%\.engram` during migration)
> - Override with `OMNIA_DATA_DIR` (`ENGRAM_DATA_DIR` remains accepted for legacy migration)
> - All core features work natively: CLI, MCP server, TUI, HTTP API, Git Sync
> - No WSL required for the core binary — it's a native Windows executable

---

## Install from source (macOS / Linux)

```bash
git clone https://github.com/Velion-SpA/omnia.git
cd omnia
go install ./cmd/omnia
# Binary goes to $GOPATH/bin (typically ~/go/bin/)
```

> **Want a real version string instead of `dev`?**
>
> `go install` always stamps the binary as `dev`. To get a meaningful version, pick one of these — not both. Running them both leaves two binaries on disk and `omnia version` keeps reporting `dev` because PATH still resolves to the `go install` build.
>
> **Option 1 — version-stamped `go install` (binary stays on PATH):**
>
> ```bash
> go install -ldflags="-X main.version=local-$(git describe --tags --always)" ./cmd/omnia
> ```
>
> **Option 2 — `go build` and move the result onto PATH:**
>
> ```bash
> go build -ldflags="-X main.version=local-$(git describe --tags --always)" -o omnia ./cmd/omnia
> mv omnia "$(go env GOPATH)/bin/omnia"
> ```
>
> After either option, `omnia version` should print `local-<git-describe>` instead of `dev`.

---

## Download binary (all platforms)

Grab the latest release for your platform from [GitHub Releases](https://github.com/Velion-SpA/omnia/releases). The table below is an asset reference only: for a verified install, use `scripts/install.sh`, which fetches `checksums.txt` and refuses to extract an archive unless its SHA-256 digest matches. If you download manually, fetch the matching `checksums.txt` from the same release and verify the selected archive with `sha256sum` or `shasum -a 256` before extracting it.

| Platform | File |
|----------|------|
| macOS (Apple Silicon) | `omnia_<version>_darwin_arm64.tar.gz` |
| macOS (Intel) | `omnia_<version>_darwin_amd64.tar.gz` |
| Linux (x86_64) | `omnia_<version>_linux_amd64.tar.gz` |
| Linux (ARM64) | `omnia_<version>_linux_arm64.tar.gz` |
| Windows (x86_64) | `omnia_<version>_windows_amd64.zip` |
| Windows (ARM64) | `omnia_<version>_windows_arm64.zip` |

---

## Requirements

- **Go 1.26.4** to build from source (not needed if installing via Homebrew or downloading a binary)
- That's it. No runtime dependencies.

The binary includes SQLite (via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go, no CGO). Works natively on **macOS**, **Linux**, and **Windows** (x86_64 and ARM64).

---

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `OMNIA_DATA_DIR` | Data directory | `~/.omnia` (Windows: `%USERPROFILE%\.omnia`) |
| `OMNIA_PORT` | HTTP server port | `7437` |

---

## Windows Config Paths

When using `omnia setup`, config files are written to platform-appropriate locations:

| Agent | macOS / Linux | Windows |
|-------|---------------|---------|
| OpenCode | `~/.config/opencode/` | `%APPDATA%\opencode\` |
| Gemini CLI | `~/.gemini/` | `%APPDATA%\gemini\` |
| Codex | `~/.codex/` | `%APPDATA%\codex\` |
| Claude Code | Managed by `claude` CLI | Managed by `claude` CLI |
| VS Code | `.vscode/mcp.json` (workspace) or `~/Library/Application Support/Code/User/mcp.json` (user) | `.vscode\mcp.json` (workspace) or `%APPDATA%\Code\User\mcp.json` (user) |
| Antigravity | `~/.gemini/antigravity/mcp_config.json` | `%USERPROFILE%\.gemini\antigravity\mcp_config.json` |
| Data directory | `~/.omnia/` | `%USERPROFILE%\.omnia\` |
