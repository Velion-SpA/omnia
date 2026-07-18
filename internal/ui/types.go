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
