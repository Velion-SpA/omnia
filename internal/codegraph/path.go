package codegraph

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func ParseFileLine(value string) (string, int, error) {
	i := strings.LastIndex(value, ":")
	if i <= 0 || i == len(value)-1 {
		return "", 0, fmt.Errorf("expected <file>:<line>")
	}
	line, err := strconv.Atoi(value[i+1:])
	if err != nil || line < 1 {
		return "", 0, fmt.Errorf("line must be a positive integer")
	}
	return value[:i], line, nil
}

// ParseBlameTarget reads a blame target where the line is OPTIONAL, returning
// line 0 for a bare path. That is the shape a caller has when it holds a file
// but no cursor — an editor integration, or a hook that runs before the file is
// read (reading it is what would produce a line).
//
// A colon only introduces a line when what follows it is a positive integer, so
// a filename that legitimately contains a colon is read as a filename rather
// than as a malformed line spec. ParseFileLine keeps its strict contract for
// callers that genuinely require a line.
func ParseBlameTarget(value string) (string, int, error) {
	i := strings.LastIndex(value, ":")
	if i <= 0 || i == len(value)-1 {
		return value, 0, nil
	}
	suffix := value[i+1:]
	line, err := strconv.Atoi(suffix)
	if err != nil {
		// Not a line at all — the colon belongs to the path.
		return value, 0, nil
	}
	if line < 1 {
		return "", 0, fmt.Errorf("line must be a positive integer")
	}
	return value[:i], line, nil
}

func Normalize(repoRoot, file string) (string, string, error) {
	dir := strings.TrimSpace(repoRoot)
	if dir == "" {
		absFile, err := filepath.Abs(file)
		if err != nil {
			return "", "", err
		}
		dir = filepath.Dir(absFile)
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("no repo context")
	}
	repoRoot = strings.TrimSpace(string(out))
	if repoRoot == "" {
		return "", "", fmt.Errorf("no repo context")
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(repoRoot, file)
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return "", "", err
	}
	// Canonicalize existing paths so /var → /private-style aliases cannot make
	// a real repository look unrelated to the repo root returned by git.
	if resolved, err := filepath.EvalSymlinks(absFile); err == nil {
		absFile = resolved
	}
	rel, err := filepath.Rel(repoRoot, absFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", fmt.Errorf("file is outside repo context")
	}
	return repoRoot, filepath.ToSlash(rel), nil
}
