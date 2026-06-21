package dashboard

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// OverviewData bundles everything the command-center home renders. It mirrors the
// local Omnia dashboard's OverviewData so the cloud home is visually identical.
type OverviewData struct {
	Projects       []ProjectStats
	TotalMemories  int
	TotalProjects  int
	LastSync       string
	LastSyncSource string
	ByType         []TypeCount
	LiveFeed       []FeedItem
	Sources        []SourceStat
}

// ProjectStats is one row of the Projects panel.
type ProjectStats struct {
	Name           string
	Total          int
	LatestUpdateAt string
	HasUpdate      bool
	IsFresh        bool
}

// TypeCount is one bar of the Memory Types panel.
type TypeCount struct {
	Name  string
	Count int
}

// FeedItem is one entry of the Live Feed panel.
type FeedItem struct {
	DetailURL string
	Title     string
	Type      string
	Project   string
	Age       string
}

// SourceStat is one row of the Sources panel.
type SourceStat struct {
	Name    string
	Sub     string
	IconKey string // "github" | "discord" | "claude"
	Count   int
}

// typePct returns the bar fill percentage for a type count relative to the max.
func typePct(count int, all []TypeCount) string {
	if len(all) == 0 || all[0].Count == 0 {
		return "0"
	}
	return fmt.Sprintf("%d", (count*100)/all[0].Count)
}

func parseCloudTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// buildCloudOverview assembles OverviewData from cloud store rows, mirroring the
// local dashboard's buildOverviewData. `observations` should be the most-recent
// rows already scoped to the principal.
func buildCloudOverview(stats []cloudstore.DashboardProjectRow, observations []cloudstore.DashboardObservationRow) OverviewData {
	total := 0
	for _, s := range stats {
		total += s.Observations
	}

	// Latest update per project + overall last sync, from the observation sample.
	latestByProject := map[string]time.Time{}
	var lastSync time.Time
	for _, o := range observations {
		if t, ok := parseCloudTime(o.CreatedAt); ok {
			if t.After(latestByProject[o.Project]) {
				latestByProject[o.Project] = t
			}
			if t.After(lastSync) {
				lastSync = t
			}
		}
	}

	// Projects sorted by count desc, then name.
	projects := make([]ProjectStats, 0, len(stats))
	for _, s := range stats {
		ps := ProjectStats{Name: s.Project, Total: s.Observations}
		if t, ok := latestByProject[s.Project]; ok {
			ps.LatestUpdateAt = relativeTime(t)
			ps.HasUpdate = true
			ps.IsFresh = isFresh(t)
		}
		projects = append(projects, ps)
	}
	sort.SliceStable(projects, func(i, j int) bool {
		if projects[i].Total != projects[j].Total {
			return projects[i].Total > projects[j].Total
		}
		return projects[i].Name < projects[j].Name
	})

	// Type breakdown from the observation sample (cap 8 bars).
	typeCounts := map[string]int{}
	for _, o := range observations {
		t := strings.TrimSpace(o.Type)
		if t == "" {
			t = "untyped"
		}
		typeCounts[t]++
	}
	byType := make([]TypeCount, 0, len(typeCounts))
	for name, cnt := range typeCounts {
		byType = append(byType, TypeCount{Name: name, Count: cnt})
	}
	sort.SliceStable(byType, func(i, j int) bool {
		if byType[i].Count != byType[j].Count {
			return byType[i].Count > byType[j].Count
		}
		return byType[i].Name < byType[j].Name
	})
	const maxTypeBars = 8
	if len(byType) > maxTypeBars {
		byType = byType[:maxTypeBars]
	}

	// Live feed: most-recent observations (cap 8).
	var feed []FeedItem
	for i, o := range observations {
		if i >= 8 {
			break
		}
		age := ""
		if t, ok := parseCloudTime(o.CreatedAt); ok {
			age = relativeTime(t)
		}
		title := strings.TrimSpace(o.Title)
		if title == "" {
			title = o.Content
			if len([]rune(title)) > 80 {
				title = string([]rune(title)[:80]) + "…"
			}
		}
		detail := fmt.Sprintf("/dashboard/observations/%s/%s/%s",
			url.PathEscape(o.Project), url.PathEscape(o.SessionID), url.PathEscape(o.SyncID))
		feed = append(feed, FeedItem{DetailURL: detail, Title: title, Type: o.Type, Project: o.Project, Age: age})
	}

	// Sources: the same three rows as the local dashboard, counts derived from the
	// observation sample's tool signal (cloud memories are mostly agent sessions,
	// so Claude Code carries the curated count).
	var github, discord, claude int
	for _, o := range observations {
		switch strings.ToLower(strings.TrimSpace(o.ToolName)) {
		case "github":
			github++
		case "discord":
			discord++
		default:
			claude++
		}
	}
	sources := []SourceStat{
		{Name: "GitHub", Sub: "PRs · commits · reviews", IconKey: "github", Count: github},
		{Name: "Discord", Sub: "digests · threads · mentions", IconKey: "discord", Count: discord},
		{Name: "Claude Code", Sub: "sessions · fixes · decisions", IconKey: "claude", Count: claude},
	}

	lastSyncAge := ""
	if !lastSync.IsZero() {
		lastSyncAge = relativeTime(lastSync)
	}

	return OverviewData{
		Projects:       projects,
		TotalMemories:  total,
		TotalProjects:  len(stats),
		LastSync:       lastSyncAge,
		LastSyncSource: "cloud",
		ByType:         byType,
		LiveFeed:       feed,
		Sources:        sources,
	}
}
