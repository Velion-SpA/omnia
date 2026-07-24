package main

// import_claude_memory.go — `omnia import claude-memory <dir>` (omnia-0.3.1
// write-hygiene PR10, design obs #1668 D10, spec obs #1666
// claude-memory-import domain). Bridges Claude Code's per-project
// `~/.claude/projects/*/memory/*.md` files into omnia observations, routed
// through the SAME write-gate every other production save entry point uses
// (design D2's "zero drift" principle — this importer dogfoods
// Store.SaveObservation exactly like cmdSave/handleSave/cmdMCP do).
//
// Discovery: every `*.md` file directly inside <dir>, skipping the index
// file (MEMORY.md) and anything that isn't markdown. Each remaining file is
// expected to carry Claude Code's frontmatter shape:
//
//	---
//	name: some-slug
//	description: One-line summary
//	metadata:
//	  type: reference
//	---
//	Markdown body...
//
// Idempotency (design D10): `name` is slugified into
// `topic_key = "claude-memory/"+slug`, which drives the EXISTING topic_key
// upsert step (D3 step 1, unconditional/explicit-intent) inside
// SaveObservation — re-importing an unchanged directory therefore never
// creates a new row, it revises the existing one in place.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/velion/omnia/internal/store"
	"gopkg.in/yaml.v3"
)

// claudeMemoryIndexFile is Claude Code's memory index — a table of contents
// pointing at the other files in the directory, never itself a first-class
// memory (spec REQ "MEMORY.md Is Skipped").
const claudeMemoryIndexFile = "MEMORY.md"

// claudeMemorySource is the provenance tag every claude-memory-imported
// observation carries (spec REQ "Provenance Tag Required"). Deliberately
// NOT one of provenance.go's TrustTag* taxonomy values (user/agent/
// ingest:tool/ingest:web/ingest:doc) — classifyTrust degrades any
// unrecognized source to "unverified" by design ("attribution, not
// authentication"), which is the correct trust class for memories bridged
// in from an external tool's own free-form notes.
const claudeMemorySource = "claude-memory"

// claudeMemoryTopicKeyPrefix namespaces every imported topic_key so it can
// never collide with a topic_key created by any other omnia entry point
// (design D10: `name` slug -> `topic_key = "claude-memory/"+name`).
const claudeMemoryTopicKeyPrefix = "claude-memory/"

// claudeMemoryDefaultType is the fallback omnia observation type for any
// metadata.type value not present in claudeMemoryTypeMap (design D10 /
// tasks 10.2: "default->manual") — "manual" is omnia's own generic default
// type (mirrors mem_save's own fallback).
const claudeMemoryDefaultType = "manual"

// claudeMemoryTypeMap maps Claude Code's memory metadata.type taxonomy to
// omnia's observation type (design D10, tasks 10.2, authoritative table):
// reference/note both become "discovery" — a non-obvious finding worth
// recalling. Any other value (including absent) falls through to
// claudeMemoryDefaultType via mapClaudeMemoryType.
var claudeMemoryTypeMap = map[string]string{
	"reference": "discovery",
	"note":      "discovery",
}

// mapClaudeMemoryType applies claudeMemoryTypeMap, case-insensitively, with
// claudeMemoryDefaultType as the fallback for anything unrecognized.
func mapClaudeMemoryType(metaType string) string {
	key := strings.ToLower(strings.TrimSpace(metaType))
	if mapped, ok := claudeMemoryTypeMap[key]; ok {
		return mapped
	}
	return claudeMemoryDefaultType
}

// claudeMemorySlugPattern collapses any run of non-alphanumeric characters
// into a single hyphen, mirroring standard slug conventions.
var claudeMemorySlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// claudeMemorySlug turns a frontmatter `name` into a deterministic slug —
// the same name ALWAYS produces the same slug, which is what makes
// topic_key-driven re-import idempotency (design D10) reliable across runs.
// An empty/symbols-only name slugifies to "" — callers must treat that as a
// malformed-file case (no usable topic_key can be built).
func claudeMemorySlug(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	slug := claudeMemorySlugPattern.ReplaceAllString(lower, "-")
	return strings.Trim(slug, "-")
}

// claudeMemoryFrontmatter mirrors Claude Code's per-memory-file YAML
// frontmatter shape. Only the 3 fields import needs are modeled — other
// metadata keys (node_type, originSessionId, modified) belong to Claude
// Code itself and are ignored here.
type claudeMemoryFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

// parseClaudeMemoryFile splits a claude-memory file's leading `---`...`---`
// YAML frontmatter block from its markdown body (design D10: "parse
// frontmatter with the existing yaml.v3 dep... body=remainder, no new
// deps"). Returns an error for ANY malformed shape — missing/unclosed
// delimiters, invalid YAML, or a missing required `name` field — so the
// caller can skip-with-warning rather than abort the whole batch.
func parseClaudeMemoryFile(raw []byte) (claudeMemoryFrontmatter, string, error) {
	var fm claudeMemoryFrontmatter

	lines := strings.Split(string(raw), "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "---" {
		return fm, "", fmt.Errorf("file does not start with a --- frontmatter delimiter")
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return fm, "", fmt.Errorf("no closing --- frontmatter delimiter found")
	}

	fmText := strings.Join(lines[start+1:end], "\n")
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return claudeMemoryFrontmatter{}, "", fmt.Errorf("invalid frontmatter YAML: %w", err)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return claudeMemoryFrontmatter{}, "", fmt.Errorf("frontmatter missing required 'name' field")
	}

	body := strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return fm, body, nil
}

// discoverClaudeMemoryFiles lists every *.md file directly inside dir,
// sorted for deterministic processing order, skipping claudeMemoryIndexFile
// and any non-markdown file (spec REQ "MEMORY.md Is Skipped").
func discoverClaudeMemoryFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		if name == claudeMemoryIndexFile {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}

// claudeMemoryDecisionLabel maps a store.WriteGateDecision* value to the
// user-facing label reported per file. WriteGateDecisionSave and
// WriteGateDecisionRelate both mean "a new row was inserted" from the
// caller's point of view (relate additionally flags a below-threshold
// related candidate, but that is an internal judgment_required detail, not
// a different outcome for THIS report) — both surface as "imported".
func claudeMemoryDecisionLabel(decision string) string {
	switch decision {
	case store.WriteGateDecisionSave, store.WriteGateDecisionRelate:
		return "imported"
	case store.WriteGateDecisionUpdate:
		return "updated"
	case store.WriteGateDecisionNoop:
		return "noop"
	default:
		return decision
	}
}

// claudeMemoryFileResult is what happened to ONE source file during import.
type claudeMemoryFileResult struct {
	File     string `json:"file"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	ID       int64  `json:"id,omitempty"`
}

// claudeMemoryImportReport is the full `omnia import claude-memory` summary.
type claudeMemoryImportReport struct {
	Dir    string                   `json:"dir"`
	DryRun bool                     `json:"dry_run"`
	Files  []claudeMemoryFileResult `json:"files"`
	Totals map[string]int           `json:"totals"`
}

// cmdImportClaudeMemory is `omnia import claude-memory <dir> [--project P]
// [--dry-run]`. Every successfully-parsed file is routed through
// store.SaveObservation (design D2/D10 "dogfooding" — the SAME write-gate
// every other production save entry point uses); malformed frontmatter is
// skipped with a warning, never aborting the rest of the batch.
func cmdImportClaudeMemory(cfg store.Config) {
	args := os.Args[3:]
	dir := ""
	projectFlag := ""
	dryRun := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--dry-run":
			dryRun = true
		default:
			if dir == "" && !strings.HasPrefix(args[i], "--") {
				dir = args[i]
			}
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "usage: omnia import claude-memory <dir> [--project P] [--dry-run]")
		exitFunc(1)
		return
	}

	files, err := discoverClaudeMemoryFiles(dir)
	if err != nil {
		fatal(fmt.Errorf("read %s: %w", dir, err))
		return
	}

	report := claudeMemoryImportReport{
		Dir:    dir,
		DryRun: dryRun,
		Totals: map[string]int{},
	}

	var s *store.Store
	sessionID := "claude-memory-import"
	if projectFlag != "" {
		sessionID = "claude-memory-import-" + projectFlag
	}

	if !dryRun {
		// Same write_hygiene.* wiring block cmdSave uses (design D2: "EVERY
		// save entry point... gets identical dedup — zero drift"). Must run
		// BEFORE storeNew — store.Config is immutable after New. A missing/
		// unreadable config.yaml leaves cfg untouched (same documented
		// degrade-to-gate-off behavior every other wired call site has).
		if appCfg, appCfgErr := loadAppConfigWithRecallAutodetect(); appCfgErr == nil {
			cfg.WriteHygieneEnabled = appCfg.WriteHygiene.Enabled
			cfg.NoopThreshold = appCfg.WriteHygiene.NoopThreshold
			cfg.UpdateThreshold = appCfg.WriteHygiene.UpdateThreshold
			cfg.ShrinkGuard = appCfg.WriteHygiene.ShrinkGuard
			cfg.CandidateLimit = appCfg.WriteHygiene.CandidateLimit
		}

		s, err = storeNew(cfg)
		if err != nil {
			fatal(err)
			return
		}
		defer s.Close()

		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			cwd = dir
		}
		if err := s.CreateSession(sessionID, projectFlag, cwd); err != nil {
			fatal(err)
			return
		}
	}

	for _, name := range files {
		path := filepath.Join(dir, name)
		result := claudeMemoryFileResult{File: name}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			result.Decision = "skipped"
			result.Reason = readErr.Error()
			report.Files = append(report.Files, result)
			report.Totals["skipped"]++
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", name, readErr)
			continue
		}

		fm, body, parseErr := parseClaudeMemoryFile(raw)
		if parseErr != nil {
			result.Decision = "skipped"
			result.Reason = parseErr.Error()
			report.Files = append(report.Files, result)
			report.Totals["skipped"]++
			fmt.Fprintf(os.Stderr, "warning: %s: malformed frontmatter (%v), skipping\n", name, parseErr)
			continue
		}

		slug := claudeMemorySlug(fm.Name)
		if slug == "" {
			result.Decision = "skipped"
			result.Reason = "name produced an empty topic_key slug"
			report.Files = append(report.Files, result)
			report.Totals["skipped"]++
			fmt.Fprintf(os.Stderr, "warning: %s: empty slug from name %q, skipping\n", name, fm.Name)
			continue
		}

		title := strings.TrimSpace(fm.Description)
		if title == "" {
			title = strings.TrimSpace(fm.Name)
		}

		if dryRun {
			result.Decision = "would-import"
			result.Reason = "dry-run: no store mutation performed"
			report.Files = append(report.Files, result)
			report.Totals["would-import"]++
			continue
		}

		saveResult, saveErr := s.SaveObservation(store.AddObservationParams{
			SessionID: sessionID,
			Type:      mapClaudeMemoryType(fm.Metadata.Type),
			Title:     title,
			Content:   body,
			Project:   projectFlag,
			Scope:     "project",
			TopicKey:  claudeMemoryTopicKeyPrefix + slug,
			Source:    claudeMemorySource,
		})
		if saveErr != nil {
			result.Decision = "skipped"
			result.Reason = saveErr.Error()
			report.Files = append(report.Files, result)
			report.Totals["skipped"]++
			fmt.Fprintf(os.Stderr, "warning: %s: save failed (%v), skipping\n", name, saveErr)
			continue
		}

		label := claudeMemoryDecisionLabel(saveResult.Decision)
		result.Decision = label
		result.ID = saveResult.ID
		report.Files = append(report.Files, result)
		report.Totals[label]++
	}

	printClaudeMemoryImportReport(report)
}

// printClaudeMemoryImportReport writes the human-readable summary. --json
// is intentionally NOT offered in this release — the report is small and
// mirrors cmdImport's own plain-text-only precedent.
func printClaudeMemoryImportReport(report claudeMemoryImportReport) {
	if report.DryRun {
		fmt.Printf("Dry-run: omnia import claude-memory %s (no changes written)\n", report.Dir)
	} else {
		fmt.Printf("Imported from %s\n", report.Dir)
	}
	for _, f := range report.Files {
		if f.Reason != "" {
			fmt.Printf("  %-30s %-12s %s\n", f.File, f.Decision, f.Reason)
		} else {
			fmt.Printf("  %-30s %-12s\n", f.File, f.Decision)
		}
	}
	fmt.Println()
	keys := make([]string, 0, len(report.Totals))
	for k := range report.Totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %s: %d\n", k, report.Totals[k])
	}
}
