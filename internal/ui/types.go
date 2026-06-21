package ui

// NavItem is one link in the command-center nav.
type NavItem struct {
	Href  string
	Label string
	ID    string
}

// LayoutProps configures the shared command-center shell. The same shell renders
// the local dashboard and the cloud dashboard; only these values differ.
type LayoutProps struct {
	Title      string // page title (before " — Omnia")
	BrandTitle string // wordmark, e.g. "Omnia"
	BrandSub   string // wordmark subtitle, e.g. "Unified Knowledge" / "Cloud Memory"
	BrandHref  string // brand link target ("/" or "/dashboard/")
	Nav        []NavItem
	Active     string // active nav ID
	StatusText string // status chip text, e.g. "Online" or a username
	User       string // optional signed-in user (cloud); empty hides the user block
	LogoutURL  string // optional logout POST action (cloud)
	AssetBase  string // static asset prefix, e.g. "/static" or "/dashboard/static"
}

// ── Overview (command-center home) ──────────────────────────────────────────

// OverviewData bundles everything the command-center home renders.
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
	Href           string // where the row links (browse filtered by project)
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
