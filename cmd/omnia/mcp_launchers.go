package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/diagnostic"
)

// mcpDeclaration is the subset of an .mcp.json we care about.
type mcpDeclaration struct {
	MCPServers map[string]struct {
		Command string `json:"command"`
	} `json:"mcpServers"`
}

// mcpDeclarationRoots are the directories searched for MCP server declarations,
// relative to the user's home. Claude Code keeps plugin-provided declarations
// under the plugin cache; a plugin update rewrites that file, which is exactly
// why a hand-edit there does not survive and the path has to be right at the
// source (#243).
var mcpDeclarationRoots = []string{
	filepath.Join(".claude", "plugins", "cache"),
}

// maxMCPScanDepth bounds the walk so a deep or looping tree cannot turn
// `omnia doctor` into a filesystem crawl.
const maxMCPScanDepth = 8

// readMCPLaunchers finds MCP server declarations and resolves each declared
// command against the filesystem.
//
// Absolute commands are checked directly. A relative command is left unresolved
// (Absolute=false) on purpose: whether it resolves depends on the PATH the host
// hands the MCP process, and that differs by launch origin — a terminal-launched
// session inherits the full user PATH while a launchd-spawned one gets only
// /usr/bin:/bin:/usr/sbin:/sbin. Guessing would produce a verdict the operator
// cannot trust.
func readMCPLaunchers(ctx context.Context) ([]diagnostic.MCPLauncher, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	var out []diagnostic.MCPLauncher
	for _, rel := range mcpDeclarationRoots {
		root := filepath.Join(home, rel)
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// An unreadable subtree is not a reason to abandon the scan: report
			// what can be read rather than nothing.
			if walkErr != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth > maxMCPScanDepth {
					return fs.SkipDir
				}
				return nil
			}
			if d.Name() != ".mcp.json" {
				return nil
			}
			out = append(out, launchersFromFile(path)...)
			return nil
		})
		// A missing root just means this host has no plugin declarations.
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ConfigPath != out[j].ConfigPath {
			return out[i].ConfigPath < out[j].ConfigPath
		}
		return out[i].ServerName < out[j].ServerName
	})
	return out, nil
}

// launchersFromFile parses one declaration file. A malformed file yields no
// launchers rather than an error: doctor reporting on the files it could read is
// more useful than refusing to report at all.
func launchersFromFile(path string) []diagnostic.MCPLauncher {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var decl mcpDeclaration
	if err := json.Unmarshal(raw, &decl); err != nil {
		return nil
	}

	out := make([]diagnostic.MCPLauncher, 0, len(decl.MCPServers))
	for name, server := range decl.MCPServers {
		command := strings.TrimSpace(server.Command)
		if command == "" {
			continue
		}
		l := diagnostic.MCPLauncher{
			ConfigPath: path,
			ServerName: name,
			Command:    command,
			Absolute:   filepath.IsAbs(command),
		}
		if l.Absolute {
			if info, statErr := os.Stat(command); statErr == nil {
				l.Exists = true
				l.Executable = !info.IsDir() && info.Mode().Perm()&0111 != 0
			}
		}
		out = append(out, l)
	}
	return out
}
