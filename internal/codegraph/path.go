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

func Normalize(repoRoot, file string) (string, string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		dir := filepath.Dir(file)
		cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
		out, err := cmd.Output()
		if err != nil {
			return "", "", fmt.Errorf("no repo context")
		}
		repoRoot = strings.TrimSpace(string(out))
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(repoRoot, file)
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(repoRoot, absFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", fmt.Errorf("file is outside repo context")
	}
	return repoRoot, filepath.ToSlash(rel), nil
}
