package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/velion/omnia/internal/store"
)

// reviewDueGroup is one row of the compact project/type count breakdown for
// `omnia review-due` (spaced-review / Play G, design D11: "count line + IDs
// + titles grouped by project then type").
type reviewDueGroup struct {
	Project string `json:"project"`
	Type    string `json:"type"`
	Count   int    `json:"count"`
}

// reviewDueEntry is a single due observation surfaced by `omnia review-due`.
// ID/Type/Title/Project ONLY — the spec's "MUST NOT dump any observation's
// full content field" (token-economy consistent, same convention as
// mem_review's action=list envelope in internal/mcp's handleReview).
type reviewDueEntry struct {
	ID      int64  `json:"id"`
	Project string `json:"project"`
	Type    string `json:"type"`
	Title   string `json:"title"`
}

// reviewDueReport is the full `omnia review-due --json` payload.
type reviewDueReport struct {
	Count        int              `json:"count"`
	Groups       []reviewDueGroup `json:"groups"`
	Observations []reviewDueEntry `json:"observations"`
}

// cmdReviewDue is `omnia review-due`: a read-only wrapper over
// store.ObservationsNeedingReview (spaced-review / Play G, design D11).
// Mirrors cmdForgetScan/cmdDoctor's flag-parsing + storeNew + defer Close
// style. Unlike forget-scan/conflicts, an unset --project intentionally
// means "all projects" (no cwd-detection exit), since output groups BY
// project — matching cmdContext's own optional-project convention.
func cmdReviewDue(cfg store.Config) {
	args := os.Args[2:]
	projectFlag := ""
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--json":
			jsonOut = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	// limit=0 defers to Store's own MaxContextResults default — a compact
	// listing, never an all-pairs/unbounded dump.
	observations, err := s.ObservationsNeedingReview(projectFlag, 0)
	if err != nil {
		fatal(err)
		return
	}

	report := buildReviewDueReport(observations)

	if jsonOut {
		out, err := jsonMarshalIndent(report, "", "  ")
		if err != nil {
			fatal(err)
			return
		}
		fmt.Println(string(out))
		return
	}

	if report.Count == 0 {
		fmt.Println("0 memories due for review.")
		return
	}

	fmt.Printf("Review Due — %d memories\n", report.Count)
	for _, g := range report.Groups {
		fmt.Printf("  %s/%s: %d\n", g.Project, g.Type, g.Count)
	}
	fmt.Println()
	for i, e := range report.Observations {
		fmt.Printf("[%d] #%d (%s) %s — %s\n", i+1, e.ID, e.Type, e.Project, e.Title)
	}
	fmt.Println()
	fmt.Println("Resolve: mem_review mark_reviewed <id>")
}

// buildReviewDueReport groups observations by project then type (design
// D11) and builds the compact entry list — id/type/title/project only, the
// observation's content field is never read here.
func buildReviewDueReport(observations []store.Observation) reviewDueReport {
	report := reviewDueReport{Count: len(observations)}
	counts := map[[2]string]int{}
	var order [][2]string
	for _, obs := range observations {
		project := ""
		if obs.Project != nil {
			project = *obs.Project
		}
		key := [2]string{project, obs.Type}
		if counts[key] == 0 {
			order = append(order, key)
		}
		counts[key]++
		report.Observations = append(report.Observations, reviewDueEntry{
			ID:      obs.ID,
			Project: project,
			Type:    obs.Type,
			Title:   obs.Title,
		})
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i][0] != order[j][0] {
			return order[i][0] < order[j][0]
		}
		return order[i][1] < order[j][1]
	})
	for _, key := range order {
		report.Groups = append(report.Groups, reviewDueGroup{Project: key[0], Type: key[1], Count: counts[key]})
	}
	return report
}
