package dashboard

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/ui"
)

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

// buildCloudOverview assembles the shared ui.OverviewData from cloud store rows.
// `observations` should be the most-recent rows already scoped to the principal.
func buildCloudOverview(stats []cloudstore.DashboardProjectRow, observations []cloudstore.DashboardObservationRow) ui.OverviewData {
	total := 0
	for _, s := range stats {
		total += s.Observations
	}

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

	projects := make([]ui.ProjectStats, 0, len(stats))
	for _, s := range stats {
		ps := ui.ProjectStats{
			Name:  s.Project,
			Total: s.Observations,
			Href:  "/dashboard/browser?project=" + url.QueryEscape(s.Project),
		}
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

	typeCounts := map[string]int{}
	for _, o := range observations {
		t := strings.TrimSpace(o.Type)
		if t == "" {
			t = "untyped"
		}
		typeCounts[t]++
	}
	byType := make([]ui.TypeCount, 0, len(typeCounts))
	for name, cnt := range typeCounts {
		byType = append(byType, ui.TypeCount{Name: name, Count: cnt})
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

	var feed []ui.FeedItem
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
		feed = append(feed, ui.FeedItem{DetailURL: detail, Title: title, Type: o.Type, Project: o.Project, Age: age})
	}

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
	sources := []ui.SourceStat{
		{Name: "GitHub", Sub: "PRs · commits · reviews", IconKey: "github", Count: github},
		{Name: "Discord", Sub: "digests · threads · mentions", IconKey: "discord", Count: discord},
		{Name: "Claude Code", Sub: "sessions · fixes · decisions", IconKey: "claude", Count: claude},
	}

	lastSyncAge := ""
	if !lastSync.IsZero() {
		lastSyncAge = relativeTime(lastSync)
	}

	return ui.OverviewData{
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
