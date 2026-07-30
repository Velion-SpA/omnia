// Package cartridge implements the repo-cartridge capability (design.md
// "Capability 6"): a precomputed, versioned per-repo digest — top-ranked
// memories, code-decision graph state (capability 1's CodeDecisionGraph),
// and trusted procedures — keyed to a repo and commit SHA, so a fresh agent
// session can load "warm" instead of cold-querying. Default-OFF, local-only
// (REQ-454), no model weights/KV-cache state ever included (REQ-455).
package cartridge

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/velion/omnia/internal/anchor"
)

// ResolveRepo resolves the git repository root and current HEAD commit SHA
// for dir, degrading to a plain error when git is missing or dir is not
// inside a git working tree (design: "git missing / no commit → cannot key
// → skip (cold start), no crash"). Callers MUST treat any error here as
// "cannot key a cartridge" and never let it crash the surrounding command.
//
// Repo-root resolution shells directly to `git rev-parse --show-toplevel`
// (mirroring internal/anchor.Probe's own unexported repoRoot method and
// internal/codegraph.Normalize's identical git probe) rather than reaching
// into internal/anchor's private surface. HeadSHA reuses the already-merged,
// exported internal/anchor.Probe.HeadSHA (anchor.go:192) directly, per
// design's explicit pointer to that method as the invalidation source.
func ResolveRepo(ctx context.Context, dir string) (repoRoot, headSHA string, err error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", fmt.Errorf("cartridge: resolve repo root: %w", err)
	}
	repoRoot = strings.TrimSpace(string(out))
	if repoRoot == "" {
		return "", "", fmt.Errorf("cartridge: not a git repository")
	}

	headSHA, err = anchor.NewProbe().HeadSHA(ctx, repoRoot)
	if err != nil {
		return "", "", fmt.Errorf("cartridge: resolve HEAD: %w", err)
	}
	return repoRoot, headSHA, nil
}
